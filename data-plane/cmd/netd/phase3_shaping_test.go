package main

// Tests for the SINGLE Phase-3 shaping writer and its control plane (ADR-0002). These prove the properties
// that make one writer safe: only an authenticated producer can submit, a dark appliance refuses outright, a
// plan is checked against netd's OWN scope, a stale or replayed plan cannot reinstate access, ordering and
// idempotency hold, strays are removed, and health tells the truth.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

type tcCall struct {
	op     string // ensure | add | delete | delete-minor
	bridge string
	ip     string
	minor  int
	down   int
	up     int
}

type fakeTC struct {
	mu      sync.Mutex
	calls   []tcCall
	failAdd map[string]error
	failDel map[string]error
	// installed models the kernel CLASSES: device -> minor -> the class that is actually there (a prepared
	// class exists here even though it has no forwarding filter). Reconciliation is only meaningful against
	// something that remembers what a previous plan (or a crash) left behind.
	installed map[string]map[int]tcCall
	// forwarding models the kernel FILTERS separately: device -> minor -> whether a guest forwarding filter is
	// installed. A prepared-but-not-activated class is installed==true, forwarding==false — it carries no
	// guest packets. This split is what lets the staged-provisioning tests prove "prepared does not forward".
	forwarding map[string]map[int]bool
	readErr    map[string]error

	// per-stage failure injection, keyed by guest IP.
	failPrepare        map[string]error // download (bridge) class preparation
	failPrepareUpload  map[string]error // upload (ifb) class preparation
	failActivate       map[string]error // download filter activation
	failActivateUpload map[string]error // upload filter activation
	failReRate         map[string]error
	failAbort          map[string]bool // AbortSession cannot remove the class (returns error, leaves it)
}

func newFakeTC() *fakeTC {
	return &fakeTC{failAdd: map[string]error{}, failDel: map[string]error{},
		installed: map[string]map[int]tcCall{}, forwarding: map[string]map[int]bool{}, readErr: map[string]error{},
		failPrepare: map[string]error{}, failPrepareUpload: map[string]error{},
		failActivate: map[string]error{}, failActivateUpload: map[string]error{},
		failReRate: map[string]error{}, failAbort: map[string]bool{}}
}

func (f *fakeTC) EnsureBridgeInfra(ctx context.Context, bridge string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tcCall{op: "ensure", bridge: bridge})
	return nil
}

// setFwd records/clears a forwarding filter for a minor on a device (caller holds the lock).
func (f *fakeTC) setFwd(device string, minor int, on bool) {
	if f.forwarding[device] == nil {
		f.forwarding[device] = map[int]bool{}
	}
	if on {
		f.forwarding[device][minor] = true
	} else {
		delete(f.forwarding[device], minor)
	}
}

// PrepareSession installs the download+upload classes WITHOUT forwarding filters, on both the bridge and its
// ifb. It clears any stale class+filter for the slot first, as the real client does.
func (f *fakeTC) PrepareSession(ctx context.Context, bridge string, ip net.IP, down, up int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	ifb := shape.IFBName(bridge)
	f.calls = append(f.calls, tcCall{op: "prepare", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up})
	// clear stale first (like DeleteSession)
	f.delClass(bridge, minor)
	f.delClass(ifb, minor)
	if err, ok := f.failPrepare[ip.String()]; ok {
		return err
	}
	f.put(bridge, minor, tcCall{op: "prepare", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up})
	if err, ok := f.failPrepareUpload[ip.String()]; ok {
		f.delClass(bridge, minor) // roll the download class back, as the real client does
		return err
	}
	f.put(ifb, minor, tcCall{op: "prepare", bridge: ifb, ip: ip.String(), minor: minor, down: down, up: up})
	return nil
}

func (f *fakeTC) ActivateSession(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	ifb := shape.IFBName(bridge)
	f.calls = append(f.calls, tcCall{op: "activate", bridge: bridge, ip: ip.String(), minor: minor})
	if err, ok := f.failActivate[ip.String()]; ok {
		return err
	}
	f.setFwd(bridge, minor, true)
	if err, ok := f.failActivateUpload[ip.String()]; ok {
		f.setFwd(bridge, minor, false) // roll the download filter back
		return err
	}
	f.setFwd(ifb, minor, true)
	return nil
}

func (f *fakeTC) AbortSession(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	ifb := shape.IFBName(bridge)
	f.calls = append(f.calls, tcCall{op: "abort", bridge: bridge, ip: ip.String(), minor: minor})
	// forwarding always stops first
	f.setFwd(bridge, minor, false)
	f.setFwd(ifb, minor, false)
	if f.failAbort[ip.String()] {
		// class cannot be removed; it remains installed but non-forwarding
		return fmt.Errorf("abort: class %d could not be removed", minor)
	}
	f.delClass(bridge, minor)
	f.delClass(ifb, minor)
	return nil
}

func (f *fakeTC) DenyForwarding(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	f.calls = append(f.calls, tcCall{op: "deny", bridge: bridge, ip: ip.String(), minor: minor})
	f.setFwd(bridge, minor, false)
	f.setFwd(shape.IFBName(bridge), minor, false)
	return nil
}

func (f *fakeTC) ReRateSession(ctx context.Context, bridge string, ip net.IP, down, up int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	ifb := shape.IFBName(bridge)
	f.calls = append(f.calls, tcCall{op: "rerate", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up})
	if err, ok := f.failReRate[ip.String()]; ok {
		return err
	}
	// class change requires the class present on both devices; it never deletes+recreates (counters preserved)
	if _, d := f.installed[bridge][minor]; !d {
		return fmt.Errorf("re-rate: no download class for minor %d", minor)
	}
	if _, u := f.installed[ifb][minor]; !u {
		return fmt.Errorf("re-rate: no upload class for minor %d", minor)
	}
	return nil
}

func (f *fakeTC) SessionForwarding(ctx context.Context, bridge string, ip net.IP) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	return f.forwarding[bridge][minor] && f.forwarding[shape.IFBName(bridge)][minor], nil
}

// delClass removes a class and its forwarding filter on one device (caller holds the lock).
func (f *fakeTC) delClass(device string, minor int) {
	if m := f.installed[device]; m != nil {
		delete(m, minor)
	}
	f.setFwd(device, minor, false)
}

func (f *fakeTC) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	f.calls = append(f.calls, tcCall{op: "delete", bridge: bridge, ip: ip.String(), minor: minor})
	if err, ok := f.failDel[ip.String()]; ok {
		return err
	}
	f.delClass(bridge, minor)
	f.delClass(shape.IFBName(bridge), minor)
	return nil
}

func (f *fakeTC) ReadClasses(ctx context.Context, device string) (map[int]shape.ClassBytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.readErr[device]; ok {
		return nil, err
	}
	out := map[int]shape.ClassBytes{}
	for minor := range f.installed[device] {
		out[minor] = shape.ClassBytes{}
	}
	return out, nil
}

func (f *fakeTC) DeleteSessionClass(ctx context.Context, bridge string, minor int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tcCall{op: "delete-minor", bridge: bridge, minor: minor})
	// A managed class is a pair — download on the bridge, upload on its ifb — so a stray removal drops both.
	f.delClass(bridge, minor)
	f.delClass(shape.IFBName(bridge), minor)
	return nil
}

// put records an installed class (caller holds the lock).
func (f *fakeTC) put(bridge string, minor int, c tcCall) {
	if f.installed[bridge] == nil {
		f.installed[bridge] = map[int]tcCall{}
	}
	f.installed[bridge][minor] = c
}

// wipe empties the kernel, as a reboot does.
func (f *fakeTC) wipe() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = map[string]map[int]tcCall{}
	f.forwarding = map[string]map[int]bool{}
}

// wipeBridge empties one bridge (and its paired ifb), as a flushed qdisc does within a single boot.
func (f *fakeTC) wipeBridge(bridge string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.installed, bridge)
	delete(f.installed, shape.IFBName(bridge))
	delete(f.forwarding, bridge)
	delete(f.forwarding, shape.IFBName(bridge))
}

// preinstall seeds the kernel with a class no plan will claim — a leftover from a crash or an earlier run.
func (f *fakeTC) preinstall(bridge, ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(net.ParseIP(ip))
	f.put(bridge, minor, tcCall{op: "add", bridge: bridge, ip: ip, minor: minor})
}

func (f *fakeTC) snapshot() ([]tcCall, map[string]map[int]tcCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[int]tcCall{}
	for b, m := range f.installed {
		out[b] = map[int]tcCall{}
		for k, v := range m {
			out[b][k] = v
		}
	}
	return append([]tcCall(nil), f.calls...), out
}

// countInstalled counts DOWNLOAD classes only (on bridges, not the paired ifb devices), so it still equals
// the number of shaped sessions now that the fake models both directions of each managed class.
func (f *fakeTC) countInstalled() int {
	_, inst := f.snapshot()
	n := 0
	for dev, m := range inst {
		if strings.HasPrefix(dev, "ifb-") {
			continue
		}
		n += len(m)
	}
	return n
}

// countForwarding counts sessions with an ACTIVE download forwarding filter — i.e. classes actually carrying
// guest packets, as distinct from merely prepared. The staged-provisioning tests assert on this.
func (f *fakeTC) countForwarding() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for dev, m := range f.forwarding {
		if strings.HasPrefix(dev, "ifb-") {
			continue
		}
		n += len(m)
	}
	return n
}

// ---- fixtures -------------------------------------------------------------

const (
	testTenant    = "11111111-1111-1111-1111-111111111111"
	testSite      = "22222222-2222-2222-2222-222222222222"
	testAppliance = "33333333-3333-3333-3333-333333333333"
	testUID       = 4242
)

func liveMode() phase3Mode {
	return phase3Mode{Active: true, TenantID: testTenant, SiteID: testSite,
		ApplianceID: testAppliance, AssignGen: 7}
}

func liveWriter(tc shaper) *phase3Shaping {
	// A live writer always has generation authority — without it no class can be made accountable, which is
	// its own test (TestNoGenerationAuthorityMeansNoAccountableClass) rather than the default condition.
	return &phase3Shaping{shp: tc, mode: liveMode(), authz: shapingAuthz{allowedUID: testUID, configured: true},
		generations: &fakeGenerations{}}
}

// envelope builds a well-formed plan for the live scope, declaring the bridges its sessions use plus the
// site's other guest bridge — so stray removal is exercised on a bridge with no sessions too.
func envelope(gen int64, sessions ...shapeplan.Session) shapeplan.Envelope {
	return envelopeOn([]string{"br-guest", "br-lan", "br-empty"}, gen, sessions...)
}

func envelopeOn(bridges []string, gen int64, sessions ...shapeplan.Session) shapeplan.Envelope {
	env := shapeplan.Envelope{
		ContractVersion: shapeplan.ContractVersion,
		TenantID:        testTenant, SiteID: testSite, ApplianceID: testAppliance,
		AssignmentGen: 7, PlanGeneration: gen,
		GeneratedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(90 * time.Second),
		ManagedBridges: bridges,
		Sessions:       sessions,
	}
	env.DesiredStateHash = shapeplan.HashDesiredState(env.ManagedBridges, env.Sessions)
	return env
}

func standardPlan(gen int64) shapeplan.Envelope {
	return envelope(gen,
		shapeplan.Session{SessionID: "gone-1", DeviceID: "dev-gone-1", IP: "10.0.0.8", Bridge: "br-guest"},
		shapeplan.Session{SessionID: "gone-2", DeviceID: "dev-gone-2", IP: "10.0.0.9", Bridge: "br-guest"},
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000, Entitled: true},
		shapeplan.Session{SessionID: "live-2", DeviceID: "dev-live-2", IP: "10.0.0.2", Bridge: "br-lan", DownKbps: 4000, UpKbps: 1500, Entitled: true},
	)
}

// ---- DARK -----------------------------------------------------------------

// A dark appliance refuses to mutate tc, on its OWN authority. If netd trusted "acctd would never submit while
// dark", the kill switch would depend on a different process staying correct — and a compromised or
// misconfigured producer would be enough to start enforcing on a site that never enabled Phase 3.
func TestDarkNetdRefusesEveryPlan(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc, authz: shapingAuthz{allowedUID: testUID, configured: true}} // mode.Active == false
	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err == nil || res.Accepted {
		t.Fatalf("a dark netd accepted a shaping plan: %+v", res)
	}
	if res.Reason != "phase3_dark" {
		t.Fatalf("refusal reason = %q, want phase3_dark", res.Reason)
	}
	if calls, _ := tc.snapshot(); len(calls) != 0 {
		t.Fatalf("a dark netd touched tc: %+v", calls)
	}
	// and it does not even disclose the managed-class generations
	srv := &server{phase3: p}
	epochReq := httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil)
	epochReq = epochReq.WithContext(context.WithValue(epochReq.Context(), peerConnKey{}, producerIdentity{UID: testUID}))
	rec := httptest.NewRecorder()
	http.HandlerFunc(srv.phase3EpochsHandler).ServeHTTP(rec, epochReq)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a dark netd served class generations: %d", rec.Code)
	}
}

// ---- producer authentication ---------------------------------------------

// The producer is authenticated by the KERNEL's statement of who is on the other end of the socket. A header
// is a claim any local process can write; SO_PEERCRED cannot be forged by the caller.
func TestOnlyTheAuthenticatedProducerMaySubmit(t *testing.T) {
	tc := newFakeTC()
	srv := &server{phase3: liveWriter(tc)}
	h := http.HandlerFunc(srv.phase3ShapingHandler)
	raw, _ := json.Marshal(standardPlan(1))

	// no peer credentials at all (a connection that did not come through the peer listener)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(raw)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unauthenticated caller was allowed to submit: %d", rec.Code)
	}

	// the wrong local uid — e.g. edged or portald, which do not own enforcement
	req := httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(raw))
	req = req.WithContext(context.WithValue(req.Context(), peerConnKey{}, producerIdentity{UID: testUID + 1, PID: 99}))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("uid %d was allowed to submit: %d", testUID+1, rec2.Code)
	}
	if tc.countInstalled() != 0 {
		t.Fatal("an unauthorized submission reached tc")
	}

	// the authorized producer
	req3 := httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(raw))
	req3 = req3.WithContext(context.WithValue(req3.Context(), peerConnKey{}, producerIdentity{UID: testUID, PID: 42}))
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("the authorized producer was refused: %d %s", rec3.Code, rec3.Body.String())
	}
}

// With no producer uid configured there is no way to tell acctd from any other local process. That is not a
// degraded mode, it is an unenforceable one, so authorization fails closed.
func TestUnconfiguredProducerFailsClosed(t *testing.T) {
	a := newShapingAuthz(func(string) string { return "" })
	if a.configured {
		t.Fatal("an empty producer uid was treated as configured")
	}
	if err := a.authorize(producerIdentity{UID: 0}, nil); err == nil {
		t.Fatal("an unconfigured authorizer allowed root to submit")
	}
	// a malformed value is not a reason to open up either
	if newShapingAuthz(func(string) string { return "not-a-uid" }).configured {
		t.Fatal("a malformed producer uid was treated as configured")
	}
	ok := newShapingAuthz(func(k string) string {
		if k == "NETD_PHASE3_PRODUCER_UID" {
			return "4242"
		}
		return ""
	})
	if !ok.configured || ok.authorize(producerIdentity{UID: 4242}, nil) != nil {
		t.Fatal("the configured producer uid was not accepted")
	}
	if ok.authorize(producerIdentity{UID: 4242}, errors.New("no creds")) == nil {
		t.Fatal("unreadable peer credentials were treated as the right uid")
	}
}

// ---- envelope validation --------------------------------------------------

// A plan is checked against netd's OWN scope, never against itself. Applying another site's or another
// appliance's desired state would silently enforce someone else's policy here.
func TestPlanMustMatchThisAppliancesScope(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*shapeplan.Envelope)
		reason string
	}{
		{"other tenant", func(e *shapeplan.Envelope) { e.TenantID = "99999999-9999-9999-9999-999999999999" }, shapeplan.ReasonWrongTenant},
		{"other site", func(e *shapeplan.Envelope) { e.SiteID = "99999999-9999-9999-9999-999999999999" }, shapeplan.ReasonWrongSite},
		{"other appliance", func(e *shapeplan.Envelope) { e.ApplianceID = "99999999-9999-9999-9999-999999999999" }, shapeplan.ReasonWrongAppliance},
		{"older assignment", func(e *shapeplan.Envelope) { e.AssignmentGen = 6 }, shapeplan.ReasonStaleAssignment},
		{"unknown contract", func(e *shapeplan.Envelope) { e.ContractVersion = "phase3-shaping/9" }, shapeplan.ReasonUnsupportedContract},
		{"no generation", func(e *shapeplan.Envelope) { e.PlanGeneration = 0 }, shapeplan.ReasonInvalidGeneration},
		{"already expired", func(e *shapeplan.Envelope) { e.ExpiresAt = time.Now().Add(-time.Second) }, shapeplan.ReasonExpiredPlan},
		{"no expiry at all", func(e *shapeplan.Envelope) { e.ExpiresAt = time.Time{} }, shapeplan.ReasonExpiredPlan},
		{"truncated body", func(e *shapeplan.Envelope) { e.Sessions = e.Sessions[:1] }, shapeplan.ReasonHashMismatch},
		{"bridge list stripped", func(e *shapeplan.Envelope) { e.ManagedBridges = nil }, shapeplan.ReasonNoManagedBridges},
		{"bridge list trimmed", func(e *shapeplan.Envelope) { e.ManagedBridges = e.ManagedBridges[:1] }, shapeplan.ReasonHashMismatch},
		{"tampered rates", func(e *shapeplan.Envelope) { e.Sessions[2].DownKbps = 100000 }, shapeplan.ReasonHashMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc := newFakeTC()
			p := liveWriter(tc)
			env := standardPlan(1)
			c.mutate(&env)
			res, err := p.submit(context.Background(), env, time.Now())
			if err == nil || res.Accepted {
				t.Fatalf("%s was accepted", c.name)
			}
			if res.Reason != c.reason {
				t.Fatalf("reason = %q, want %q", res.Reason, c.reason)
			}
			if tc.countInstalled() != 0 {
				t.Fatalf("%s reached tc", c.name)
			}
		})
	}
}

// A delayed or replayed older plan must never be applied. It is the classic way a reconciliation loop
// reinstates access somebody has just revoked: the stale plan still lists them as entitled.
func TestStaleAndReplayedPlansAreRefused(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)

	if _, err := p.submit(context.Background(), standardPlan(5), time.Now()); err != nil {
		t.Fatalf("generation 5 was refused: %v", err)
	}
	// an older plan arriving late
	res, err := p.submit(context.Background(), standardPlan(4), time.Now())
	if err == nil || res.Reason != shapeplan.ReasonStaleGeneration {
		t.Fatalf("a stale plan was accepted: %+v", res)
	}
	// the SAME generation with DIFFERENT contents means the producer's numbering is broken; applying either
	// would be a guess
	conflicting := envelope(5, shapeplan.Session{SessionID: "live-1", IP: "10.0.0.77", Bridge: "br-guest",
		DownKbps: 1000, UpKbps: 1000, Entitled: true})
	res2, err2 := p.submit(context.Background(), conflicting, time.Now())
	if err2 == nil || res2.Reason != shapeplan.ReasonGenerationConflict {
		t.Fatalf("a conflicting duplicate generation was accepted: %+v", res2)
	}
	// an exact replay of the accepted generation is harmless and idempotent
	if _, err := p.submit(context.Background(), standardPlan(5), time.Now()); err != nil {
		t.Fatalf("an exact replay was refused: %v", err)
	}
	// and a newer one is accepted
	if _, err := p.submit(context.Background(), standardPlan(6), time.Now()); err != nil {
		t.Fatalf("a newer plan was refused: %v", err)
	}
}

// The generation history survives a netd restart. Without that, a restarted netd would accept an old plan it
// had already superseded — and re-shape sessions whose access had ended.
func TestAcceptedGenerationSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "plan.json")
	tc := newFakeTC()

	p := liveWriter(tc)
	p.store = &planStore{path: statePath}
	if _, err := p.submit(context.Background(), standardPlan(9), time.Now()); err != nil {
		t.Fatalf("generation 9 was refused: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("the accepted plan was not persisted: %v", err)
	}

	// a brand-new netd process, no memory at all
	restarted := liveWriter(tc)
	restarted.store = &planStore{path: statePath}
	res, err := restarted.submit(context.Background(), standardPlan(8), time.Now())
	if err == nil || res.Reason != shapeplan.ReasonStaleGeneration {
		t.Fatalf("a restarted netd accepted a superseded plan: %+v", res)
	}
	if _, err := restarted.submit(context.Background(), standardPlan(10), time.Now()); err != nil {
		t.Fatalf("a restarted netd refused a current plan: %v", err)
	}
}

// ---- reconciliation -------------------------------------------------------

// Teardown happens before shaping. The other order leaves a window in which access that has ended is still
// forwarded while capacity is handed to whoever is still entitled.
func TestShapingTearsDownBeforeShaping(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.TornDown != 2 || res.Shaped != 2 || res.Degraded {
		t.Fatalf("unexpected result: %+v", res)
	}
	calls, _ := tc.snapshot()
	firstShape, lastDelete := -1, -1
	for i, c := range calls {
		if (c.op == "prepare" || c.op == "activate" || c.op == "rerate") && firstShape == -1 {
			firstShape = i
		}
		if c.op == "delete" || c.op == "delete-minor" {
			lastDelete = i
		}
	}
	if lastDelete > firstShape {
		t.Fatalf("shaping was applied before teardown finished: %+v", calls)
	}
}

// THE case a delta protocol can never handle: a class belonging to a session that no longer exists anywhere.
// After a crash — or any restart that lost the in-memory map — nothing will ever mention it again, so it would
// keep forwarding traffic forever. Enumerating the kernel is the only way to find it.
func TestStrayClassesAreRemoved(t *testing.T) {
	tc := newFakeTC()
	// a leftover from before: a class for a guest whose session ended while netd was down
	tc.preinstall("br-guest", "10.0.0.55")
	p := liveWriter(tc)

	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.StraysRemoved != 1 {
		t.Fatalf("strays removed = %d, want 1: %+v", res.StraysRemoved, res)
	}
	_, installed := tc.snapshot()
	strayMinor, _ := shape.MinorForIP(net.ParseIP("10.0.0.55"))
	if _, still := installed["br-guest"][strayMinor]; still {
		t.Fatal("a class with no live session is still forwarding traffic")
	}
	if len(installed["br-guest"]) != 1 || len(installed["br-lan"]) != 1 {
		t.Fatalf("reconciled state is not exactly the desired state: %+v", installed)
	}
}

// THE CASE THAT NEEDS THE DECLARED BRIDGE LIST: a leftover class on a bridge with NO sessions at all. Nothing
// in the session set mentions that bridge, so an applier that reconciled only "wherever the sessions are"
// would never look there — and the class would keep forwarding traffic for access that ended, forever.
func TestStrayOnAnEmptyBridgeIsStillRemoved(t *testing.T) {
	tc := newFakeTC()
	tc.preinstall("br-empty", "10.0.0.77") // a guest bridge whose last session ended while netd was down
	p := liveWriter(tc)

	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.StraysRemoved != 1 {
		t.Fatalf("strays removed = %d, want the one on the session-less bridge: %+v", res.StraysRemoved, res)
	}
	_, installed := tc.snapshot()
	if len(installed["br-empty"]) != 0 {
		t.Fatal("a class on a bridge with no sessions survived reconciliation")
	}
}

// A session on a bridge the plan does not declare is refused rather than installed: the applier would create
// a class it has not been told to reconcile, and would therefore never remove it again.
func TestSessionOnAnUndeclaredBridgeIsRefused(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	res, err := p.submit(context.Background(), envelopeOn([]string{"br-guest"}, 1,
		shapeplan.Session{SessionID: "ok", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true},
		shapeplan.Session{SessionID: "elsewhere", IP: "10.0.0.2", Bridge: "br-rogue", DownKbps: 1000, UpKbps: 500, Entitled: true},
	), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 || !res.Degraded {
		t.Fatalf("an undeclared bridge was shaped anyway: %+v", res)
	}
	_, installed := tc.snapshot()
	if len(installed["br-rogue"]) != 0 {
		t.Fatal("a class was installed on a bridge the plan does not manage")
	}
}

// An unreadable kernel means UNKNOWN strays. Reporting success would tell an operator enforcement is exact
// when netd has no idea what is installed.
func TestUnreadableKernelIsDegradedNotAssumedClean(t *testing.T) {
	tc := newFakeTC()
	tc.readErr["br-guest"] = errors.New("tc: cannot open netlink")
	p := liveWriter(tc)
	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded || res.Failed == 0 {
		t.Fatalf("an unreadable kernel was reported as a clean apply: %+v", res)
	}
	if st := p.status(); st["degraded"] != true {
		t.Fatalf("health hid an unreadable kernel: %v", st)
	}
}

// Applying the same plan repeatedly converges: the desired state is identical every time, which is what makes
// restart and reboot recovery ordinary rather than special.
func TestShapingIsIdempotentAndConverges(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, first := tc.snapshot()

	// a "restart": a brand-new writer with no memory at all applies the same desired state
	p2 := liveWriter(tc)
	if _, err := p2.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, second := tc.snapshot()

	if len(first) != len(second) {
		t.Fatalf("state diverged across a restart: %v vs %v", first, second)
	}
	for bridge, m := range first {
		for minor, c := range m {
			if second[bridge][minor] != c {
				t.Fatalf("class %s/%d diverged: %+v vs %+v", bridge, minor, c, second[bridge][minor])
			}
		}
	}
	if tc.countInstalled() != 2 {
		t.Fatalf("installed %d sessions, want exactly the 2 entitled ones", tc.countInstalled())
	}
}

// A plan that could not be fully applied is reported as degraded rather than treated as success — especially a
// TEARDOWN failure, which means traffic may still be forwarded for access that has ended.
func TestPartialApplicationIsTruthfullyDegraded(t *testing.T) {
	tc := newFakeTC()
	tc.failDel["10.0.0.8"] = errors.New("class busy")
	p := liveWriter(tc)
	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded || res.Failed == 0 {
		t.Fatalf("a failed teardown was not reported: %+v", res)
	}
	if res.Shaped != 2 {
		t.Fatalf("the rest of the plan was abandoned: %+v", res)
	}
	if len(res.Problems) == 0 {
		t.Fatal("degraded state must say what went wrong")
	}
}

// A session with no addressing or no rates is left UNSHAPED. Installing a zero rate would be a silent
// full-speed pass — worse than not shaping it, because it looks deliberate.
func TestUnusablePlanEntriesAreNotShaped(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	res, err := p.submit(context.Background(), envelope(1,
		shapeplan.Session{SessionID: "no-ip", Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true},
		shapeplan.Session{SessionID: "no-rates", IP: "10.0.0.3", Bridge: "br-guest", Entitled: true},
		shapeplan.Session{SessionID: "no-bridge", IP: "10.0.0.4", DownKbps: 1000, UpKbps: 500, Entitled: true},
	), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || res.Failed != 3 || !res.Degraded {
		t.Fatalf("unusable entries were shaped or hidden: %+v", res)
	}
	if tc.countInstalled() != 0 {
		t.Fatal("something was installed for an unusable entry")
	}
}

// Two live sessions mapping to one class is a producer defect. Installing either would attribute the other's
// traffic to it, so neither is installed and the conflict is reported.
func TestConflictingSessionsAreBothRefused(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	// 10.0.0.1 and 10.16.0.1 collide: the minor is derived from the low 12 bits of the host part
	res, err := p.submit(context.Background(), envelope(1,
		shapeplan.Session{SessionID: "a", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true},
		shapeplan.Session{SessionID: "b", IP: "10.0.16.1", Bridge: "br-guest", DownKbps: 2000, UpKbps: 900, Entitled: true},
	), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || !res.Degraded {
		t.Fatalf("a class conflict was resolved by guessing: %+v", res)
	}
	if tc.countInstalled() != 0 {
		t.Fatal("a conflicting class was installed")
	}
}

// Concurrent submissions must not interleave: two half-applied plans would leave the kernel in a state neither
// plan describes.
func TestConcurrentSubmissionsDoNotInterleave(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.submit(context.Background(), standardPlan(1), time.Now())
		}()
	}
	wg.Wait()
	if tc.countInstalled() != 2 {
		t.Fatalf("installed %d sessions after concurrent submissions, want exactly 2", tc.countInstalled())
	}
}

// ---- HTTP contract --------------------------------------------------------

// The endpoint accepts only a COMPLETE envelope and rejects anything it does not understand, so the two sides
// cannot drift into exchanging deltas.
func TestShapingEndpointContract(t *testing.T) {
	tc := newFakeTC()
	srv := &server{phase3: liveWriter(tc)}
	h := http.HandlerFunc(srv.phase3ShapingHandler)

	post := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), peerConnKey{}, producerIdentity{UID: testUID}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	raw, _ := json.Marshal(standardPlan(1))
	rec := post(raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("a valid plan was rejected: %d %s", rec.Code, rec.Body.String())
	}
	var res shapingPlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || res.Shaped != 2 || res.TornDown != 2 {
		t.Fatalf("unexpected response: %+v", res)
	}

	// an unknown field is a contract drift signal, not something to silently ignore
	if rec2 := post([]byte(`{"contract_version":"phase3-shaping/1","remove_one":"s1"}`)); rec2.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field was accepted: %d", rec2.Code)
	}

	// a legacy DELTA body (the pre-contract shape) must not be silently interpreted
	if rec3 := post([]byte(`{"tear":[],"shape":[]}`)); rec3.Code != http.StatusBadRequest {
		t.Fatalf("a legacy delta body was accepted: %d %s", rec3.Code, rec3.Body.String())
	}

	// an unsupported contract version is a 400 (the caller must change), a scope/staleness refusal is a 409
	badVersion := standardPlan(2)
	badVersion.ContractVersion = "phase3-shaping/2"
	bvRaw, _ := json.Marshal(badVersion)
	if rec4 := post(bvRaw); rec4.Code != http.StatusBadRequest {
		t.Fatalf("an unsupported contract version returned %d, want 400", rec4.Code)
	}
	wrongSite := standardPlan(3)
	wrongSite.SiteID = "99999999-9999-9999-9999-999999999999"
	wsRaw, _ := json.Marshal(wrongSite)
	if rec5 := post(wsRaw); rec5.Code != http.StatusConflict {
		t.Fatalf("an out-of-scope plan returned %d, want 409", rec5.Code)
	}
}

// ---- health ---------------------------------------------------------------

// Health must tell the truth about enforcement, in a state an operator can act on. The five states exist
// because the interesting failures are not "an error occurred" — they are "nothing is known to be in force"
// and "what is in force is no longer confirmed", both of which an earlier version reported as healthy.
func TestHealthReportsTruthfulShapingState(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)

	// LIVE, NOTHING IN FORCE. This is degraded: the appliance is supposed to be enforcing and demonstrably
	// is not. Reporting degraded=false here — with the fact buried in a plan_stale field — reads as healthy
	// at a glance while saying the opposite in small print.
	st := p.status()
	if st["state"] != shapingNoPlan || st["degraded"] != true {
		t.Fatalf("a live writer with nothing in force: %v", st)
	}
	if _, ok := st["last_applied_at"]; ok {
		t.Fatal("a writer that has applied nothing claimed a last-applied time")
	}
	if st["active"] != true || st["producer_authenticated"] != true {
		t.Fatalf("health did not report the enforcement mode: %v", st)
	}

	// CONVERGED: the kernel was driven to a current plan with nothing failing.
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	st = p.status()
	if st["state"] != shapingConverged || st["degraded"] != false {
		t.Fatalf("a clean apply: %v", st)
	}
	if st["converged_generation"] != int64(1) || st["admitted_generation"] != int64(1) || st["plan_stale"] != false {
		t.Fatalf("health did not report the converged plan: %v", st)
	}

	// DEGRADED: a teardown failed, so traffic may still be forwarded for access that ended.
	tc.failDel["10.0.0.8"] = errors.New("class busy")
	if _, err := p.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	st = p.status()
	if st["state"] != shapingDegradedState || st["degraded"] != true || st["problem"] == nil {
		t.Fatalf("a failed apply: %v", st)
	}
	// the ADMISSION advanced (so a replay of generation 1 still cannot reinstate anything) but CONVERGENCE
	// did not: claiming generation 2 was put in force would be a false record of the kernel's state
	if st["admitted_generation"] != int64(2) {
		t.Fatalf("a partially applied plan was not admitted: %v", st)
	}
	if st["converged_generation"] != int64(1) {
		t.Fatalf("a partially applied plan was recorded as converged: %v", st)
	}

	// RECOVERY: re-submitting the SAME generation and hash is allowed, and finishing convergence clears it.
	delete(tc.failDel, "10.0.0.8")
	if _, err := p.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatalf("retrying the admitted generation was refused: %v", err)
	}
	st = p.status()
	if st["state"] != shapingConverged || st["degraded"] != false {
		t.Fatalf("a recovered writer: %v", st)
	}
	if st["converged_generation"] != int64(2) {
		t.Fatalf("convergence did not catch up: %v", st)
	}

	// a refusal is counted and named, so an operator can see netd REFUSING plans rather than quietly
	// enforcing nothing
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err == nil {
		t.Fatal("expected the stale plan to be refused")
	}
	st = p.status()
	if st["last_refusal"] != shapeplan.ReasonStaleGeneration || st["refused_total"] != int64(1) {
		t.Fatalf("health did not report the refusal: %v", st)
	}
}

// A plan that expired without a replacement means the producer went quiet. What is installed may still be
// correct, but nothing is confirming it — and "probably still correct" is not a state to stay silent about.
func TestExpiredPlanIsStaleAndDegraded(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	now := time.Now()
	env := standardPlan(1)
	env.ExpiresAt = now.Add(30 * time.Second)
	if _, err := p.submit(context.Background(), env, now); err != nil {
		t.Fatal(err)
	}
	if st := p.status(); st["state"] != shapingConverged || st["plan_stale"] != false {
		t.Fatalf("a fresh plan: %v", st)
	}

	// wind the converged plan's validity into the past, as a producer that died would leave it
	p.mu.Lock()
	p.lastConverged.ExpiresAt = time.Now().Add(-time.Second)
	p.mu.Unlock()

	st := p.status()
	if st["state"] != shapingStale {
		t.Fatalf("an expired plan reported state %v", st["state"])
	}
	if st["degraded"] != true {
		t.Fatalf("an expired plan reported degraded=false: %v", st)
	}
	if st["plan_stale"] != true {
		t.Fatalf("plan_stale was not set: %v", st)
	}
}

// A dark appliance is not "degraded" — it is doing exactly what it should.
func TestDarkIsNotDegraded(t *testing.T) {
	p := &phase3Shaping{shp: newFakeTC(), authz: shapingAuthz{allowedUID: testUID, configured: true}}
	st := p.status()
	if st["state"] != shapingDark || st["degraded"] != false {
		t.Fatalf("a dark writer: %v", st)
	}
}

// ---- generation authority and kernel-proven continuity ----------------------

// fakeGenerations stands in for the durable allocator: strictly increasing, never reissued, able to fail.
type fakeGenerations struct {
	mu     sync.Mutex
	last   int64
	err    error
	issued []int64 // every value handed out, so a test can prove none was repeated
}

func (g *fakeGenerations) AllocateClassGeneration(ctx context.Context, tenant, site, appliance string) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return 0, g.err
	}
	g.last++
	g.issued = append(g.issued, g.last)
	return g.last, nil
}

// writerWithState builds a writer backed by a real state file and a durable allocator, as netd's main does.
func writerWithState(tc *fakeTC, gens generationAllocator, path, bootID string) *phase3Shaping {
	p := liveWriter(tc)
	p.classStore = &classStore{path: path}
	p.generations = gens
	prev, _ := p.classStore.load()
	inv, verified := kernelInventory(context.Background(), tc, bridgesIn(prev))
	p.restore(prev, bootID, inv, verified)
	return p
}

// A RESTART with the kernel intact keeps every generation. If it did not, acctd — holding a checkpoint at the
// older generation — would refuse every later observation as stale and accounting would simply stop.
func TestRestartWithKernelIntactKeepsGenerations(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}
	const boot = "boot-aaaa"

	p := writerWithState(tc, gens, state, boot)
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := p.Epochs()
	if len(before) != 2 {
		t.Fatalf("managed classes = %d, want the two shaped sessions: %v", len(before), before)
	}

	restarted := writerWithState(tc, gens, state, boot)
	for k, v := range before {
		if restarted.Epochs()[k] != v {
			t.Fatalf("class %s: generation changed across a restart, %d -> %d", k, v, restarted.Epochs()[k])
		}
	}
	if _, err := restarted.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	for k, v := range before {
		if got := restarted.Epochs()[k]; got != v {
			t.Fatalf("class %s: generation changed on re-application, %d -> %d", k, v, got)
		}
	}
}

// A REBOOT loses every class. Their successors must be strictly newer generations, or a checkpoint that still
// pins the old one would measure a counter restarting at zero against it.
func TestRebootAllocatesStrictlyNewerGenerations(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}

	p := writerWithState(tc, gens, state, "boot-aaaa")
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := p.Epochs()

	tc.wipe() // reboot: new boot id AND an empty kernel
	rebooted := writerWithState(tc, gens, state, "boot-bbbb")
	if len(rebooted.Epochs()) != 0 {
		t.Fatalf("a rebooted appliance claimed %d surviving classes", len(rebooted.Epochs()))
	}
	if _, err := rebooted.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	for k, old := range before {
		if got := rebooted.Epochs()[k]; got <= old {
			t.Fatalf("class %s reused generation %d after a reboot (now %d)", k, old, got)
		}
	}
}

// SAME BOOT but the class is gone — a flushed qdisc, a manual removal. The boot id still matches, so only the
// kernel reading can catch it. The successor must be a new series; untouched classes keep theirs.
func TestSameBootWithClassRemovedRegenerates(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}
	const boot = "boot-aaaa"

	p := writerWithState(tc, gens, state, boot)
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := p.Epochs()

	tc.wipeBridge("br-guest")
	restarted := writerWithState(tc, gens, state, boot)
	if _, still := restarted.Epochs()[classKey("br-guest", "live-1")]; still {
		t.Fatal("a class the kernel no longer has was carried forward on a boot-id match alone")
	}
	if got := restarted.Epochs()[classKey("br-lan", "live-2")]; got != before[classKey("br-lan", "live-2")] {
		t.Fatalf("an untouched class lost its generation: %d -> %d", before[classKey("br-lan", "live-2")], got)
	}
	if _, err := restarted.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Epochs()[classKey("br-guest", "live-1")]; got <= before[classKey("br-guest", "live-1")] {
		t.Fatalf("the recreated class reused generation %d", got)
	}
}

// SAME BOOT, same minor, DIFFERENT session. The slot is occupied, so a presence check alone would carry the
// old generation forward and hand the new guest the previous guest's checkpoint.
func TestSameMinorReusedByAnotherSessionDoesNotInherit(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}

	p := writerWithState(tc, gens, state, "boot-aaaa")
	first := envelope(1, shapeplan.Session{SessionID: "guest-a", DeviceID: "dev-a", IP: "10.0.0.1",
		Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true})
	if _, err := p.submit(context.Background(), first, time.Now()); err != nil {
		t.Fatal(err)
	}
	epochA := p.Epochs()[classKey("br-guest", "guest-a")]

	second := envelope(2, shapeplan.Session{SessionID: "guest-b", DeviceID: "dev-b", IP: "10.0.0.1",
		Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true})
	if _, err := p.submit(context.Background(), second, time.Now()); err != nil {
		t.Fatal(err)
	}
	epochB := p.Epochs()[classKey("br-guest", "guest-b")]
	if epochB == 0 || epochB == epochA {
		t.Fatalf("the new occupant of the slot got generation %d; the previous was %d", epochB, epochA)
	}
	if _, stale := p.Epochs()[classKey("br-guest", "guest-a")]; stale {
		t.Fatal("the previous occupant is still reported as a managed class")
	}
}

// Corrupted durable state with OLDER generations already in use must not reuse one. The allocator is the
// authority; a lost inventory cannot hand back a value some checkpoint still pins.
func TestCorruptedStateWithOlderGenerationsNeverReuses(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{last: 5} // 1..5 already issued and pinned by existing checkpoints

	p := writerWithState(tc, gens, state, "boot-aaaa")
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := p.Epochs()

	if err := os.WriteFile(state, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered := writerWithState(tc, gens, state, "boot-aaaa")
	if len(recovered.Epochs()) != 0 {
		t.Fatal("unreadable state was treated as a valid empty inventory")
	}
	if _, err := recovered.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	for k, old := range before {
		if got := recovered.Epochs()[k]; got <= old {
			t.Fatalf("class %s reused generation %d after corrupted state (now %d)", k, old, got)
		}
	}
	seen := map[int64]bool{}
	for _, g := range gens.issued {
		if seen[g] {
			t.Fatalf("generation %d was issued more than once", g)
		}
		seen[g] = true
	}
}

// The generation is NOT derived from the wall clock. A clock that jumps backwards — RTC reset, NTP
// correction, image restore — must not affect it at all.
func TestGenerationsAreIndependentOfTheWallClock(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}
	p := writerWithState(tc, gens, state, "boot-aaaa")

	past := standardPlan(1)
	past.GeneratedAt = time.Now().Add(-72 * time.Hour)
	if _, err := p.submit(context.Background(), past, time.Now()); err != nil {
		t.Fatal(err)
	}
	first := p.Epochs()

	tc.wipe()
	restarted := writerWithState(tc, gens, state, "boot-aaaa")
	// a plan evaluated against a clock that has moved BACKWARDS two days
	if _, err := restarted.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	for k, old := range first {
		if got := restarted.Epochs()[k]; got <= old {
			t.Fatalf("class %s: generation %d did not advance past %d despite a backwards clock", k, got, old)
		}
	}
}

// Without generation authority a class is NOT made accountable. Installing it anyway would put traffic on the
// wire that no checkpoint can ever attribute, and no local value may be manufactured to fill the gap.
func TestNoGenerationAuthorityMeansNoAccountableClass(t *testing.T) {
	tc := newFakeTC()
	gens := &fakeGenerations{err: errors.New("database unavailable")}
	p := writerWithState(tc, gens, filepath.Join(t.TempDir(), "classes.json"), "boot-aaaa")

	res, err := p.submit(context.Background(), standardPlan(1), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || !res.Degraded {
		t.Fatalf("classes were installed without a generation: %+v", res)
	}
	if len(p.Epochs()) != 0 {
		t.Fatalf("generations were manufactured locally: %v", p.Epochs())
	}
}

// Epochs() reports only classes the kernel is currently verified to hold. Reporting a torn-down or
// unverifiable class would tell acctd it may account against something that no longer exists.
func TestEpochsExposesOnlyVerifiedKernelClasses(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}
	p := writerWithState(tc, gens, state, "boot-aaaa")
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	for k := range p.Epochs() {
		if strings.Contains(k, "gone-") {
			t.Fatalf("a torn-down session is reported as a managed class: %s", k)
		}
	}

	tc.readErr["br-guest"] = errors.New("netlink unavailable")
	blind := writerWithState(tc, gens, state, "boot-aaaa")
	if len(blind.Epochs()) != 0 {
		t.Fatalf("classes were claimed without reading the kernel: %v", blind.Epochs())
	}
	if blind.restoreNote == "" {
		t.Fatal("an unverifiable restore did not record why nothing was carried forward")
	}
}

// The class-generation endpoint is control-plane state: it enumerates which sessions are shaped, where, and
// how often each class has been replaced. Every other service on the appliance can reach the socket, so it
// requires the same authenticated producer identity as a submission.
func TestEpochsEndpointRequiresTheAuthenticatedProducer(t *testing.T) {
	tc := newFakeTC()
	srv := &server{phase3: liveWriter(tc)}
	if _, err := srv.phase3.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(srv.phase3EpochsHandler)

	// no peer credentials at all
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unauthenticated caller read class generations: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "live-1") {
		t.Fatal("a refused read disclosed managed sessions")
	}

	// another local service — edged, portald, scd all share the socket group
	other := httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil)
	other = other.WithContext(context.WithValue(other.Context(), peerConnKey{}, producerIdentity{UID: testUID + 1}))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, other)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("uid %d read class generations: %d", testUID+1, rec2.Code)
	}

	// the authorised producer
	ok := httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil)
	ok = ok.WithContext(context.WithValue(ok.Context(), peerConnKey{}, producerIdentity{UID: testUID}))
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, ok)
	if rec3.Code != http.StatusOK {
		t.Fatalf("the authorised producer was refused: %d", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "live-1") {
		t.Fatalf("the authorised producer got no generations: %s", rec3.Body.String())
	}
}

// THE WHOLE REBOOT SEQUENCE, end to end.
//
// Each piece is tested on its own above; this drives them in the order a real reboot produces them, because
// the failure being guarded against is an interaction, not a unit: durable admission state and durable class
// state must recover together, the kernel must be re-driven from a single desired-state submission, strays
// left by the previous boot must go, recreated classes must be new series, and the accounting checkpoints
// acctd still holds must be able to continue without a regression or a phantom delta.
func TestRebootConvergesFromOneSubmission(t *testing.T) {
	dir := t.TempDir()
	classState := filepath.Join(dir, "classes.json")
	planState := filepath.Join(dir, "plan.json")
	gens := &fakeGenerations{}
	tc := newFakeTC()

	// ---- before the reboot: a converged appliance --------------------------
	before := liveWriter(tc)
	before.classStore = &classStore{path: classState}
	before.store = &planStore{path: planState}
	before.generations = gens
	before.restore(classState4(), "boot-aaaa", map[string]map[int]bool{}, true)
	if _, err := before.submit(context.Background(), standardPlan(7), time.Now()); err != nil {
		t.Fatal(err)
	}
	if st := before.status(); st["state"] != shapingConverged {
		t.Fatalf("the appliance did not converge before the reboot: %v", st)
	}
	oldEpochs := before.Epochs()
	if len(oldEpochs) != 2 {
		t.Fatalf("managed classes before the reboot = %d", len(oldEpochs))
	}

	// ---- the reboot --------------------------------------------------------
	// Every tc class is gone. One class from the previous boot is left behind in the kernel by a partial
	// teardown, to prove stray removal still happens after a restart.
	tc.wipe()
	tc.preinstall("br-guest", "10.0.0.55")

	after := liveWriter(tc)
	after.classStore = &classStore{path: classState}
	after.store = &planStore{path: planState}
	after.generations = gens
	prev, _ := after.classStore.load()
	inv, verified := kernelInventory(context.Background(), tc, bridgesIn(prev))
	after.restore(prev, "boot-bbbb", inv, verified)

	// durable ADMISSION state survived: a replay of a superseded generation is still refused
	if res, err := after.submit(context.Background(), standardPlan(6), time.Now()); err == nil {
		t.Fatalf("a superseded plan was accepted after a reboot: %+v", res)
	} else if res.Reason != shapeplan.ReasonStaleGeneration {
		t.Fatalf("refusal reason after reboot = %q", res.Reason)
	}

	// nothing is claimed to be in force yet
	if st := after.status(); st["state"] != shapingNoPlan || st["degraded"] != true {
		t.Fatalf("a rebooted appliance did not report that nothing is in force: %v", st)
	}

	// ---- one complete desired-state submission -----------------------------
	res, err := after.submit(context.Background(), standardPlan(8), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Degraded {
		t.Fatalf("the appliance did not converge from one submission: %+v", res)
	}
	if res.StraysRemoved != 1 {
		t.Fatalf("strays removed = %d; the class left by the previous boot must go", res.StraysRemoved)
	}
	if res.Shaped != 2 {
		t.Fatalf("shaped = %d, want both entitled sessions", res.Shaped)
	}
	if st := after.status(); st["state"] != shapingConverged || st["degraded"] != false {
		t.Fatalf("after convergence: %v", st)
	}

	// ---- the accounting side can continue safely ---------------------------
	// Every recreated class is a STRICTLY NEWER generation, so a checkpoint still holding the previous one
	// sees a new series (a trustworthy reset) rather than a counter that appears to have gone backwards.
	newEpochs := after.Epochs()
	if len(newEpochs) != len(oldEpochs) {
		t.Fatalf("managed classes after convergence = %d, want %d", len(newEpochs), len(oldEpochs))
	}
	for k, old := range oldEpochs {
		if newEpochs[k] <= old {
			t.Fatalf("class %s came back as generation %d, not newer than %d", k, newEpochs[k], old)
		}
	}
	seen := map[int64]string{}
	for k, v := range newEpochs {
		if other, dup := seen[v]; dup {
			t.Fatalf("classes %s and %s share generation %d", other, k, v)
		}
		seen[v] = k
	}
}

// classState4 is an empty starting inventory, spelled out so the reboot test reads in order.
func classState4() classState {
	return classState{Classes: map[string]managedClass{}}
}
