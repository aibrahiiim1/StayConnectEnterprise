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
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	h := holder()
	s.DownKbps, s.UpKbps = 4000, 4000
	h.DownKbps, h.UpKbps = 10000, 10000
	if res, err := p.submit(context.Background(), envelope(1, s, h), time.Now()); err != nil || res.Failed != 0 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	s.EntitlementID, s.DownKbps, s.UpKbps = "ent-fullstay", 10000, 10000
	if res, err := p.submit(context.Background(), envelope(2, s, h), time.Now()); err != nil || res.Failed != 0 || res.Shaped != 2 {
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
	if res, err := p.submit(context.Background(), envelope(3, s, h), time.Now()); err != nil || res.Failed != 0 {
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

// withBulkHolder adds a device that keeps the bulk move's target in use from the first pass, so the moved sessions must regroup into it.
func withBulkHolder(s []shapeplan.Session) []shapeplan.Session {
	return append(append([]shapeplan.Session{}, s...), allocSession("hb", "10.0.0.9", "new", "SHARED"))
}

// lasting makes a test plan valid at the given clock, for a test that moves its clock past the default validity.
func lasting(e shapeplan.Envelope, at time.Time) shapeplan.Envelope {
	e.ExpiresAt = at.Add(time.Minute)
	return e
}

// holder keeps a device on ent-fullstay, so that entitlement already has its own group and a session moved into it must regroup.
func holder() shapeplan.Session { return allocSession("h", "10.0.0.12", "ent-fullstay", "SHARED") }

// The reverse: a PER_DEVICE device moved into a SHARED package that already has a group joins it in the same pass, with no new sign-in.
func TestSpeedAllocation_AMoveFromPerDeviceIntoAGroupIsImmediate(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	s := allocSession("a", "10.0.0.11", "ent-perdevice", "PER_DEVICE")
	h := holder()
	t0 := time.Now()
	if res, err := p.submit(context.Background(), envelope(1, s, h), t0); err != nil || res.Shaped != 2 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	if tc.parentOf("br-guest", "10.0.0.11") != shape.RootParent {
		t.Fatal("the PER_DEVICE session was not under the root")
	}
	s.EntitlementID, s.SpeedAllocation = "ent-fullstay", "SHARED"
	if res, err := p.submit(context.Background(), envelope(2, s, h), t0.Add(time.Second)); err != nil || res.Failed != 0 || res.Shaped != 2 {
		t.Fatalf("pass after the move: %+v %v", res, err)
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != shape.GroupClassid("ent-fullstay") || got != tc.parentOf("br-guest", "10.0.0.12") {
		t.Fatalf("the moved device hangs under %q, want the package's group shared with its other device", got)
	}
	if !g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("the moved device is offline after the move")
	}
}

// Devices whose moves keep failing do not use up the per-pass budget: another device's move still happens in the same pass.
func TestSpeedAllocation_FailingMovesDoNotStarveOtherMoves(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	var ips []string
	for i := 10; i < 20; i++ {
		ips = append(ips, fmt.Sprintf("10.0.0.%d", i))
	}
	p := allocWriter(t, tc, g, append(ips, "10.0.0.9")...)
	sessions := make([]shapeplan.Session, len(ips))
	for i, ip := range ips {
		sessions[i] = allocSession(fmt.Sprintf("s%d", i), ip, fmt.Sprintf("old-%d", i), "SHARED")
	}
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, withBulkHolder(sessions)...), t0); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	for _, ip := range ips[:9] {
		g.failRevoke[ip] = true
	}
	g.mu.Unlock()
	for i := range sessions {
		sessions[i].EntitlementID = "new"
	}
	if _, err := p.submit(context.Background(), envelope(2, withBulkHolder(sessions)...), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	last := ips[9]
	if tc.parentOf("br-guest", last) != shape.GroupClassid("new") || !g.isAuthorized("br-guest", last) {
		t.Fatalf("%s did not move in the pass where nine other moves failed", last)
	}
}

// A move whose old class cannot be removed leaves the device denied (never unshaped), reports it, and completes on the next pass.
func TestSpeedAllocation_AMoveThatCannotRemoveTheOldClassStaysDeniedAndIsRetried(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	h := holder()
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, s, h), t0); err != nil {
		t.Fatal(err)
	}
	s.EntitlementID = "ent-fullstay"
	tc.mu.Lock()
	tc.failDel["10.0.0.11"] = errors.New("tc class del failed")
	tc.mu.Unlock()
	res, err := p.submit(context.Background(), envelope(2, s, h), t0.Add(time.Second))
	if err != nil || res.Failed == 0 {
		t.Fatalf("a failed removal was reported clean: %+v %v", res, err)
	}
	if g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("the device forwards while its old class could not be removed")
	}
	tc.mu.Lock()
	delete(tc.failDel, "10.0.0.11")
	tc.mu.Unlock()
	if res, err := p.submit(context.Background(), envelope(3, s, h), t0.Add(2*time.Second)); err != nil || res.Failed != 0 {
		t.Fatalf("retry pass: %+v %v", res, err)
	}
	if !g.isAuthorized("br-guest", "10.0.0.11") || tc.parentOf("br-guest", "10.0.0.11") != shape.GroupClassid("ent-fullstay") {
		t.Fatal("the move did not complete on the next pass")
	}
}

// A device moved from a SHARED package to a PER_DEVICE one leaves its old group in the same pass (live, 53 such devices stayed capped by a group
// and were rewritten every pass): denied first, its class rebuilt directly under the root, then admitted again.
func TestSpeedAllocation_AMoveToPerDeviceLeavesTheGroup(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-shared", "SHARED")
	t0 := time.Now()
	if res, err := p.submit(context.Background(), envelope(1, s), t0); err != nil || res.Shaped != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got == shape.RootParent {
		t.Fatal("the SHARED session was not under a group")
	}
	g.mu.Lock()
	g.calls = nil
	g.mu.Unlock()
	s.EntitlementID, s.SpeedAllocation = "ent-perdevice", "PER_DEVICE"
	if res, err := p.submit(context.Background(), envelope(2, s), t0.Add(time.Second)); err != nil || res.Failed != 0 || res.Shaped != 1 {
		t.Fatalf("pass after the move: %+v %v", res, err)
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != shape.RootParent || !g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatalf("after the move: parent %q authorized=%v, want the root and online in the same pass", got, g.isAuthorized("br-guest", "10.0.0.11"))
	}
	g.mu.Lock()
	revokedFirst := len(g.calls) > 0 && strings.HasPrefix(g.calls[0], "revoke ")
	g.mu.Unlock()
	if !revokedFirst {
		t.Fatal("the device was not denied before its class was replaced")
	}
	tc.mu.Lock()
	tc.calls = nil
	tc.mu.Unlock()
	if _, err := p.submit(context.Background(), envelope(3, s), t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for _, c := range tc.calls {
		if c.op == "delete" || c.op == "prepare" {
			t.Fatalf("the per-device session was rebuilt again: %+v", c)
		}
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
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	h := holder()
	if res, err := p.submit(context.Background(), envelope(1, s, h), time.Now()); err != nil || res.Shaped != 2 {
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
	if _, err := p.submit(context.Background(), envelope(2, s, h), time.Now()); err != nil {
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
	p := allocWriter(t, tc, nil, "10.0.0.11", "10.0.0.12")
	p.gate = gate
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	h := holder()
	if res, err := p.submit(context.Background(), envelope(1, s, h), time.Now()); err != nil || res.Shaped != 2 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	s.EntitlementID = "ent-fullstay"
	res, err := p.submit(context.Background(), envelope(2, s, h), time.Now())
	if err != nil || res.Failed != 0 || res.Shaped != 2 {
		t.Fatalf("pass after the move: %+v %v", res, err)
	}
	if ok, _ := gate.Authorized(context.Background(), "br-guest", net.ParseIP("10.0.0.11")); !ok {
		t.Fatalf("the rebuilt session is not authorized by the MAC-bound set: %v", sets.mac)
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != shape.GroupClassid("ent-fullstay") {
		t.Fatalf("parent %q, want the new group", got)
	}
}

// A denial that cannot be proven leaves the class in place and makes the walk unclean, so the regroup is retried, never skipped.
func TestSpeedAllocation_AFailedRegroupDenialIsReportedAndRetried(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	p.fastPath = true
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	h := holder()
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, s, h), t0); err != nil {
		t.Fatal(err)
	}
	old := tc.parentOf("br-guest", "10.0.0.11")
	s.EntitlementID = "ent-fullstay"
	g.mu.Lock()
	g.failRevoke["10.0.0.11"] = true
	g.mu.Unlock()
	res, err := p.submit(context.Background(), envelope(2, s, h), t0.Add(time.Second))
	if err != nil || res.Failed == 0 {
		t.Fatalf("a regroup whose denial failed was reported clean: %+v %v", res, err)
	}
	if tc.parentOf("br-guest", "10.0.0.11") != old {
		t.Fatal("the class was moved although the device could not be denied")
	}
	g.mu.Lock()
	delete(g.failRevoke, "10.0.0.11")
	g.mu.Unlock()
	if _, err := p.submit(context.Background(), envelope(2, s, h), t0.Add(2*time.Second)); err != nil {
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
	p := allocWriter(t, tc, g, append(ips, "10.0.0.9")...)
	p.fastPath = true
	sessions := make([]shapeplan.Session, len(ips))
	for i, ip := range ips {
		sessions[i] = allocSession(fmt.Sprintf("s%d", i), ip, fmt.Sprintf("old-%d", i), "SHARED")
	}
	t0 := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, withBulkHolder(sessions)...), t0); err != nil {
		t.Fatal(err)
	}
	for i := range sessions {
		sessions[i].EntitlementID = "new"
	}
	for n := 1; n <= 3; n++ {
		if _, err := p.submit(context.Background(), envelope(2, withBulkHolder(sessions)...), t0.Add(time.Duration(n)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, ip := range ips {
		if tc.parentOf("br-guest", ip) != shape.GroupClassid("new") {
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
	p := allocWriter(t, tc, g, append(ips, "10.0.0.9")...)
	sessions := make([]shapeplan.Session, len(ips))
	for i, ip := range ips {
		sessions[i] = allocSession(fmt.Sprintf("s%d", i), ip, fmt.Sprintf("old-%d", i), "SHARED")
	}
	if res, err := p.submit(context.Background(), envelope(1, withBulkHolder(sessions)...), time.Now()); err != nil || res.Shaped != len(ips)+1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	for i := range sessions {
		sessions[i].EntitlementID = "new"
	}
	moved := func() int {
		n := 0
		for _, ip := range ips {
			if tc.parentOf("br-guest", ip) == shape.GroupClassid("new") {
				n++
			}
		}
		return n
	}
	for gen := int64(2); gen <= 4; gen++ {
		before := moved()
		res, err := p.submit(context.Background(), envelope(gen, withBulkHolder(sessions)...), time.Now())
		if err != nil || res.Failed != 0 || res.Shaped != len(ips)+1 {
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
