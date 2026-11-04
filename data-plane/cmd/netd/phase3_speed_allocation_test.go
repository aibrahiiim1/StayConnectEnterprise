package main

// SHARED puts one aggregate ceiling on an Entitlement and its devices borrow from it; it is not a division, so one active device gets the whole ceiling. PER_DEVICE is the default.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/onegate/onegate/data-plane/internal/shape"
	"github.com/onegate/onegate/data-plane/internal/shapeplan"
)

func allocSession(id, ip, ent, mode string) shapeplan.Session {
	return shapeplan.Session{SessionID: id, DeviceID: "dev-" + id, IP: ip, MAC: allocMAC(ip),
		Bridge: "br-guest", EntitlementID: ent, SpeedAllocation: mode,
		DownKbps: 20000, UpKbps: 5000, Entitled: true}
}

func allocMAC(ip string) string { return "aa:bb:cc:00:00:" + ip[len(ip)-2:] }

// allocWriter's DHCP evidence confirms every address, since unconfirmed ownership never reaches class construction.
func allocWriter(t *testing.T, tc *fakeTC, g *fakeGate, ips ...string) *phase3Shaping {
	t.Helper()
	rows := make([]KeaLease, 0, len(ips))
	for _, ip := range ips {
		rows = append(rows, lease(ip, allocMAC(ip)))
	}
	p := liveWriter(tc)
	p.gate = g
	p.owner = addressOwner{src: fakeLeases{rows: rows}}
	return p
}

// The renewal pass re-rates a SHARED session under its group (its floor, not the whole allowance) and re-rates the
// group itself, once per pass, when the package rate changes (audit Phase 8 M4 / Phase 6 M4).
func TestSpeedAllocation_SharedReRateKeepsTheGroupAndFollowsTheAllowance(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	a := allocSession("a", "10.0.0.11", "ent-1", "SHARED")
	b := allocSession("b", "10.0.0.12", "ent-1", "SHARED")
	if res, err := p.submit(context.Background(), envelope(1, a, b), time.Now()); err != nil || res.Failed != 0 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	a.DownKbps, b.DownKbps = 40000, 40000
	tc.mu.Lock()
	tc.calls = nil
	tc.mu.Unlock()
	if res, err := p.submit(context.Background(), envelope(2, a, b), time.Now()); err != nil || res.Failed != 0 {
		t.Fatalf("renewal pass: %+v %v", res, err)
	}
	group := shape.GroupClassid("ent-1")
	rerates, groups := 0, 0
	tc.mu.Lock()
	for _, c := range tc.calls {
		switch c.op {
		case "rerate":
			rerates++
			if c.group != group {
				t.Errorf("session %s re-rated outside its group (%q): every device would be guaranteed the whole allowance", c.ip, c.group)
			}
		case "group":
			groups++
		}
	}
	tc.mu.Unlock()
	if rerates != 2 {
		t.Fatalf("re-rates = %d, want 2", rerates)
	}
	if groups != 2 { // once per pass for the bridge and once for its ifb, not once per device
		t.Fatalf("group re-rates = %d, want 2", groups)
	}
	if rate, ok := tc.groupRate("br-guest", group); !ok || rate != 40000 {
		t.Fatalf("the group did not follow the new allowance: %d (present=%v)", rate, ok)
	}
}

// A session moved to another entitlement (staff Reset, a package switch) is rebuilt under the new entitlement's group: HTB cannot
// re-parent a class, so a re-rate alone left it capped at the old group's rate.
func TestSpeedAllocation_ASessionMovedToAnotherEntitlementJoinsItsGroup(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	s.DownKbps, s.UpKbps = 4000, 4000
	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Failed != 0 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	s.EntitlementID, s.DownKbps, s.UpKbps = "ent-fullstay", 10000, 10000
	stampStored(p, &s)
	if res, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil || res.Failed != 0 || res.Shaped != 1 {
		t.Fatalf("pass after the move: %+v %v", res, err)
	}
	group := shape.GroupClassid("ent-fullstay")
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != group {
		t.Fatalf("the session hangs under %q, want the new entitlement's group %q", got, group)
	}
	if rate, ok := tc.groupRate("br-guest", group); !ok || rate != 10000 {
		t.Fatalf("new group rate = %d (present=%v), want 10000", rate, ok)
	}
	if !g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("the moved session lost its internet access")
	}
	// Once under its own group it is left alone: no further rebuild.
	tc.mu.Lock()
	tc.calls = nil
	tc.mu.Unlock()
	if res, err := p.submit(context.Background(), envelope(3, s), time.Now()); err != nil || res.Failed != 0 {
		t.Fatalf("steady pass: %+v %v", res, err)
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for _, c := range tc.calls {
		if c.op == "delete" || c.op == "prepare" {
			t.Fatalf("a session already under its group was rebuilt again: %+v", c)
		}
	}
}

// stampStored stands in for acctd: the plan says the session's current class counters (the fake's are zero) are stored.
func stampStored(p *phase3Shaping, s *shapeplan.Session) {
	s.ObservedEpoch, s.ObservedUp, s.ObservedDown = p.epochs[classKey(s.Bridge, s.SessionID)], 0, 0
}

// A moved device is denied and its class kept until acctd has stored the class's final counters, then rebuilt: no byte goes uncounted.
func TestSpeedAllocation_ARegroupWaitsForTheFinalCountersToBeStored(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	t0 := time.Now()
	if res, err := p.submit(context.Background(), envelope(1, s), t0); err != nil || res.Shaped != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	old := tc.parentOf("br-guest", "10.0.0.11")
	s.EntitlementID = "ent-fullstay" // not yet stored for this generation
	if _, err := p.submit(context.Background(), envelope(2, s), t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("the device forwards while its class is being replaced")
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != old {
		t.Fatalf("the class was deleted before its final counters were stored (parent %q)", got)
	}
	stampStored(p, &s)
	if res, err := p.submit(context.Background(), envelope(3, s), t0.Add(10*time.Second)); err != nil || res.Shaped != 1 || res.Failed != 0 {
		t.Fatalf("pass after the counters are stored: %+v %v", res, err)
	}
	if !g.isAuthorized("br-guest", "10.0.0.11") || tc.parentOf("br-guest", "10.0.0.11") != shape.GroupClassid("ent-fullstay") {
		t.Fatal("the device was not rebuilt under its new group once accounted")
	}
}

// The stored counters are not in the plan hash, so a waiting regroup must keep the fast path off or the device stays denied for a full-walk interval.
func TestSpeedAllocation_AWaitingRegroupKeepsTheFastPathOff(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	p.fastPath = true
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	t0 := time.Now()
	if res, err := p.submit(context.Background(), envelope(1, s), t0); err != nil || res.Shaped != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	s.EntitlementID = "ent-fullstay"
	if _, err := p.submit(context.Background(), envelope(2, s), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stampStored(p, &s)
	if _, err := p.submit(context.Background(), envelope(2, s), t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !g.isAuthorized("br-guest", "10.0.0.11") || tc.parentOf("br-guest", "10.0.0.11") != shape.GroupClassid("ent-fullstay") {
		t.Fatal("the stored counters did not finish the regroup on the next submission")
	}
}

// If acctd never confirms (an older acctd, or accounting down), the wait is bounded: the device is rebuilt after regroupAccountingWait.
func TestSpeedAllocation_ARegroupWaitIsBounded(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, s), t0); err != nil {
		t.Fatal(err)
	}
	s.EntitlementID = "ent-fullstay"
	if _, err := p.submit(context.Background(), envelope(2, s), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if res, err := p.submit(context.Background(), envelope(3, s), t0.Add(time.Second+regroupAccountingWait)); err != nil || res.Shaped != 1 {
		t.Fatalf("after the bounded wait: %+v %v", res, err)
	}
	if !g.isAuthorized("br-guest", "10.0.0.11") || tc.parentOf("br-guest", "10.0.0.11") != shape.GroupClassid("ent-fullstay") {
		t.Fatal("the device stayed denied past the bounded wait")
	}
}

type failingGenerations struct{}

func (failingGenerations) AllocateClassGeneration(context.Context, string, string, string) (int64, error) {
	return 0, errors.New("database unavailable")
}

// The rebuild is fail-closed: packet authorization is revoked before the class goes, and a rebuild that cannot finish leaves the device denied,
// never forwarding through the default class unshaped and uncounted.
func TestSpeedAllocation_ARegroupRevokesBeforeTheClassGoesAndStaysDeniedOnFailure(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	g.mu.Lock()
	g.calls = nil
	g.mu.Unlock()
	tc.mu.Lock()
	tc.calls = nil
	tc.mu.Unlock()

	p.generations = failingGenerations{}
	s.EntitlementID = "ent-fullstay"
	stampStored(p, &s)
	if _, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("the device is authorized with no class to shape or count it")
	}
	g.mu.Lock()
	revoked := len(g.calls) > 0 && strings.HasPrefix(g.calls[0], "revoke ")
	g.mu.Unlock()
	tc.mu.Lock()
	deleted := false
	for _, c := range tc.calls {
		if c.op == "delete" {
			deleted = true
		}
	}
	tc.mu.Unlock()
	if !revoked || !deleted {
		t.Fatalf("revoke first then delete: revoked=%v deleted=%v", revoked, deleted)
	}
}

// With MAC binding on, the rebuilt session is authorized through the MAC-bound set in the same pass, not left denied.
func TestSpeedAllocation_ARegroupKeepsTheMACBinding(t *testing.T) {
	tc := newFakeTC()
	tc.reportParents = true
	gate, sets := newMACGate(true)
	p := allocWriter(t, tc, nil, "10.0.0.11")
	p.gate = gate
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	s.EntitlementID = "ent-fullstay"
	stampStored(p, &s)
	res, err := p.submit(context.Background(), envelope(2, s), time.Now())
	if err != nil || res.Failed != 0 || res.Shaped != 1 {
		t.Fatalf("pass after the move: %+v %v", res, err)
	}
	if ok, _ := gate.Authorized(context.Background(), "br-guest", net.ParseIP("10.0.0.11")); !ok {
		t.Fatalf("the rebuilt session is not authorized by the MAC-bound set: %v", sets.mac)
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != shape.GroupClassid("ent-fullstay") {
		t.Fatalf("parent %q, want the new group", got)
	}
}

// A bulk move is spread over passes, and every session stays shaped and authorized in each of them.
// Rebuilds are budgeted on their own: sessions that waited for their counters and are then confirmed together are rebuilt a few per pass.
func TestSpeedAllocation_ResumedRegroupsAreRebuiltAFewPerPass(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	var ips []string
	for i := 10; i < 30; i++ {
		ips = append(ips, fmt.Sprintf("10.0.0.%d", i))
	}
	p := allocWriter(t, tc, g, ips...)
	sessions := make([]shapeplan.Session, len(ips))
	for i, ip := range ips {
		sessions[i] = allocSession(fmt.Sprintf("s%d", i), ip, fmt.Sprintf("old-%d", i), "SHARED")
	}
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, sessions...), t0); err != nil {
		t.Fatal(err)
	}
	for i := range sessions {
		sessions[i].EntitlementID = fmt.Sprintf("new-%d", i)
	}
	// Not yet stored: every pass starts at most eight waits and rebuilds none.
	for n := 1; n <= 3; n++ {
		if _, err := p.submit(context.Background(), envelope(int64(1+n), sessions...), t0.Add(time.Duration(n)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.regroupWaiting) != len(ips) {
		t.Fatalf("%d sessions waiting, want all %d", len(p.regroupWaiting), len(ips))
	}
	moved := func() int {
		n := 0
		for i, ip := range ips {
			if tc.parentOf("br-guest", ip) == shape.GroupClassid(fmt.Sprintf("new-%d", i)) {
				n++
			}
		}
		return n
	}
	for i := range sessions {
		stampStored(p, &sessions[i])
	}
	for n := 0; n < 4; n++ {
		before := moved()
		if _, err := p.submit(context.Background(), envelope(int64(5+n), sessions...), t0.Add(time.Duration(5+n)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if step := moved() - before; step > maxRegroupsPerPass {
			t.Fatalf("one pass rebuilt %d resumed regroups, want at most %d", step, maxRegroupsPerPass)
		}
	}
	if moved() != len(ips) {
		t.Fatalf("%d of %d sessions moved after the counters were stored", moved(), len(ips))
	}
}

// A denial that cannot be proven leaves the class in place and makes the walk unclean, so the regroup is retried, never skipped.
func TestSpeedAllocation_AFailedRegroupDenialIsReportedAndRetried(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	p.fastPath = true
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, s), t0); err != nil {
		t.Fatal(err)
	}
	old := tc.parentOf("br-guest", "10.0.0.11")
	s.EntitlementID = "ent-fullstay"
	stampStored(p, &s)
	g.mu.Lock()
	g.failRevoke["10.0.0.11"] = true
	g.mu.Unlock()
	res, err := p.submit(context.Background(), envelope(2, s), t0.Add(time.Second))
	if err != nil || res.Failed == 0 {
		t.Fatalf("a regroup whose denial failed was reported clean: %+v %v", res, err)
	}
	if tc.parentOf("br-guest", "10.0.0.11") != old {
		t.Fatal("the class was moved although the device could not be denied")
	}
	g.mu.Lock()
	delete(g.failRevoke, "10.0.0.11")
	g.mu.Unlock()
	if _, err := p.submit(context.Background(), envelope(2, s), t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if tc.parentOf("br-guest", "10.0.0.11") != shape.GroupClassid("ent-fullstay") || !g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("the regroup was not retried once the gate recovered")
	}
}

// Regroups the per-pass cap left behind keep the fast path off, so an unchanged plan resubmitted still finishes a bulk move.
func TestSpeedAllocation_ACappedBulkMoveIsNotStalledByTheFastPath(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	var ips []string
	for i := 10; i < 30; i++ {
		ips = append(ips, fmt.Sprintf("10.0.0.%d", i))
	}
	p := allocWriter(t, tc, g, ips...)
	p.fastPath = true
	sessions := make([]shapeplan.Session, len(ips))
	for i, ip := range ips {
		sessions[i] = allocSession(fmt.Sprintf("s%d", i), ip, fmt.Sprintf("old-%d", i), "SHARED")
	}
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, sessions...), t0); err != nil {
		t.Fatal(err)
	}
	for i := range sessions {
		sessions[i].EntitlementID = fmt.Sprintf("new-%d", i)
		stampStored(p, &sessions[i])
	}
	for n := 1; n <= 3; n++ {
		if _, err := p.submit(context.Background(), envelope(2, sessions...), t0.Add(time.Duration(n)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for i, ip := range ips {
		if tc.parentOf("br-guest", ip) != shape.GroupClassid(fmt.Sprintf("new-%d", i)) {
			t.Fatalf("%s is still under its old group after three submissions of the same plan", ip)
		}
	}
}

func TestSpeedAllocation_ABulkMoveIsRebuiltAFewSessionsPerPass(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	var ips []string
	for i := 10; i < 30; i++ {
		ips = append(ips, fmt.Sprintf("10.0.0.%d", i))
	}
	p := allocWriter(t, tc, g, ips...)
	sessions := make([]shapeplan.Session, len(ips))
	for i, ip := range ips {
		sessions[i] = allocSession(fmt.Sprintf("s%d", i), ip, fmt.Sprintf("old-%d", i), "SHARED")
	}
	if res, err := p.submit(context.Background(), envelope(1, sessions...), time.Now()); err != nil || res.Shaped != len(ips) {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	for i := range sessions {
		sessions[i].EntitlementID = fmt.Sprintf("new-%d", i)
		stampStored(p, &sessions[i])
	}
	moved := func() int {
		n := 0
		for i, ip := range ips {
			if tc.parentOf("br-guest", ip) == shape.GroupClassid(fmt.Sprintf("new-%d", i)) {
				n++
			}
		}
		return n
	}
	for gen := int64(2); gen <= 4; gen++ {
		before := moved()
		res, err := p.submit(context.Background(), envelope(gen, sessions...), time.Now())
		if err != nil || res.Failed != 0 || res.Shaped != len(ips) {
			t.Fatalf("pass %d: %+v %v", gen, res, err)
		}
		if step := moved() - before; step > maxRegroupsPerPass {
			t.Fatalf("pass %d rebuilt %d sessions, want at most %d", gen, step, maxRegroupsPerPass)
		}
		for _, ip := range ips {
			if !g.isAuthorized("br-guest", ip) {
				t.Fatalf("pass %d: %s lost its access while others were rebuilt", gen, ip)
			}
		}
	}
	if n := moved(); n != len(ips) {
		t.Fatalf("after three passes %d of %d sessions are under their new group", n, len(ips))
	}
}

// PER_DEVICE: no group is created and each session hangs off the root at its own rate.
func TestSpeedAllocation_PerDeviceKeepsIndependentRates(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")

	res, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "PER_DEVICE"),
		allocSession("b", "10.0.0.12", "ent-1", "PER_DEVICE")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 2 || res.Failed != 0 {
		t.Fatalf("per-device shaping failed: %+v", res)
	}
	if _, ok := tc.groupRate("br-guest", shape.GroupClassid("ent-1")); ok {
		t.Fatal("PER_DEVICE created an aggregate group class")
	}
	for _, ip := range []string{"10.0.0.11", "10.0.0.12"} {
		if got := tc.parentOf("br-guest", ip); got != shape.RootParent {
			t.Fatalf("%s hangs under %q, want the root — a per-device class shares nothing", ip, got)
		}
	}
}

// An empty mode is PER_DEVICE; older revisions produce one and must not become SHARED.
func TestSpeedAllocation_UnsetModeIsPerDevice(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11")

	if _, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "")), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := tc.groupRate("br-guest", shape.GroupClassid("ent-1")); ok {
		t.Fatal("an unset allocation mode was treated as SHARED")
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != shape.RootParent {
		t.Fatalf("parent = %q, want the root", got)
	}
}

// SHARED: one group class per Entitlement, on both the download bridge and the upload IFB.
func TestSpeedAllocation_SharedBuildsOneCeilingForTheEntitlement(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")

	res, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "SHARED"),
		allocSession("b", "10.0.0.12", "ent-1", "SHARED")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 2 || res.Failed != 0 {
		t.Fatalf("shared shaping failed: %+v", res)
	}
	group := shape.GroupClassid("ent-1")
	down, ok := tc.groupRate("br-guest", group)
	if !ok || down != 20000 {
		t.Fatalf("download group rate = %d (present=%v), want the plan's 20000", down, ok)
	}
	up, ok := tc.groupRate(shape.IFBName("br-guest"), group)
	if !ok || up != 5000 {
		t.Fatalf("upload group rate = %d (present=%v), want the plan's 5000", up, ok)
	}
	for _, ip := range []string{"10.0.0.11", "10.0.0.12"} {
		if got := tc.parentOf("br-guest", ip); got != group {
			t.Fatalf("%s hangs under %q, want the entitlement's group %q", ip, got, group)
		}
	}
}

// Separate entitlements get separate ceilings.
func TestSpeedAllocation_SeparateEntitlementsGetSeparateCeilings(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")

	if _, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "SHARED"),
		allocSession("b", "10.0.0.12", "ent-2", "SHARED")), time.Now()); err != nil {
		t.Fatal(err)
	}
	g1, g2 := shape.GroupClassid("ent-1"), shape.GroupClassid("ent-2")
	if g1 == g2 {
		t.Fatal("two entitlements hashed to one group class")
	}
	if tc.parentOf("br-guest", "10.0.0.11") != g1 || tc.parentOf("br-guest", "10.0.0.12") != g2 {
		t.Fatal("sessions were not placed under their own entitlement's ceiling")
	}
}

// The group is created once and re-rated, never replaced: replacing resets counters and drops the ceiling.
func TestSpeedAllocation_SharedGroupIsIdempotentAcrossPasses(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-1", "SHARED")

	for gen := int64(1); gen <= 3; gen++ {
		if _, err := p.submit(context.Background(), envelope(gen, s), time.Now()); err != nil {
			t.Fatalf("pass %d: %v", gen, err)
		}
	}
	if rate, ok := tc.groupRate("br-guest", shape.GroupClassid("ent-1")); !ok || rate != 20000 {
		t.Fatalf("group rate after three passes = %d (present=%v)", rate, ok)
	}
}

// A SHARED session with no entitlement to group by is refused rather than given its own full rate.
func TestSpeedAllocation_SharedWithoutAnEntitlementIsRefused(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "", "SHARED")

	res, err := p.submit(context.Background(), envelope(1, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || res.Failed == 0 {
		t.Fatalf("a SHARED session with no entitlement was shaped anyway: %+v", res)
	}
	if g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("it was authorized despite having no accountable ceiling")
	}
}

// The floor is a floor, not a slice: it stays well below the ceiling so HTB lends the rest.
func TestSpeedAllocation_TheSharedFloorIsNotADivision(t *testing.T) {
	for _, tc := range []struct{ group, wantMax int }{
		{group: 20000, wantMax: 20000},
		{group: 1000, wantMax: 1000},
		{group: 50, wantMax: 50},
	} {
		floor := shape.SharedFloorKbpsForTest(tc.group)
		if floor > tc.wantMax {
			t.Fatalf("group %d: floor %d exceeds the ceiling", tc.group, floor)
		}
		if tc.group > 100 && floor >= tc.group {
			t.Fatalf("group %d: floor %d leaves nothing to borrow — that is a division, not sharing",
				tc.group, floor)
		}
	}
}
