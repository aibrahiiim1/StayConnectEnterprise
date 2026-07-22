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
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	// installed models the kernel: bridge -> minor -> the class that is actually there. Reconciliation is
	// only meaningful against something that remembers what a previous plan (or a crash) left behind.
	installed map[string]map[int]tcCall
	readErr   map[string]error
}

func newFakeTC() *fakeTC {
	return &fakeTC{failAdd: map[string]error{}, failDel: map[string]error{},
		installed: map[string]map[int]tcCall{}, readErr: map[string]error{}}
}

func (f *fakeTC) EnsureBridgeInfra(ctx context.Context, bridge string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tcCall{op: "ensure", bridge: bridge})
	return nil
}

func (f *fakeTC) AddSession(ctx context.Context, bridge string, ip net.IP, down, up int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	c := tcCall{op: "add", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up}
	f.calls = append(f.calls, c)
	if err, ok := f.failAdd[ip.String()]; ok {
		return err
	}
	f.put(bridge, minor, c)
	return nil
}

func (f *fakeTC) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	f.calls = append(f.calls, tcCall{op: "delete", bridge: bridge, ip: ip.String(), minor: minor})
	if err, ok := f.failDel[ip.String()]; ok {
		return err
	}
	if m := f.installed[bridge]; m != nil {
		delete(m, minor)
	}
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
	if m := f.installed[bridge]; m != nil {
		delete(m, minor)
	}
	return nil
}

// put records an installed class (caller holds the lock).
func (f *fakeTC) put(bridge string, minor int, c tcCall) {
	if f.installed[bridge] == nil {
		f.installed[bridge] = map[int]tcCall{}
	}
	f.installed[bridge][minor] = c
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

func (f *fakeTC) countInstalled() int {
	_, inst := f.snapshot()
	n := 0
	for _, m := range inst {
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
	return &phase3Shaping{shp: tc, mode: liveMode(), authz: shapingAuthz{allowedUID: testUID, configured: true}}
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
		shapeplan.Session{SessionID: "gone-1", IP: "10.0.0.8", Bridge: "br-guest"},
		shapeplan.Session{SessionID: "gone-2", IP: "10.0.0.9", Bridge: "br-guest"},
		shapeplan.Session{SessionID: "live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000, Entitled: true},
		shapeplan.Session{SessionID: "live-2", IP: "10.0.0.2", Bridge: "br-lan", DownKbps: 4000, UpKbps: 1500, Entitled: true},
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
	rec := httptest.NewRecorder()
	http.HandlerFunc(srv.phase3EpochsHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil))
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
	firstAdd, lastDelete := -1, -1
	for i, c := range calls {
		if c.op == "add" && firstAdd == -1 {
			firstAdd = i
		}
		if c.op == "delete" || c.op == "delete-minor" {
			lastDelete = i
		}
	}
	if lastDelete > firstAdd {
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

// Health must tell the truth about enforcement. A plan that failed to apply is invisible otherwise, and
// "shaping looks fine" is the most expensive kind of wrong.
func TestHealthReportsTruthfulShapingState(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)

	// nothing submitted yet: not degraded, no last-applied claim, and explicitly stale — because a live
	// appliance with no plan is NOT enforcing anything, which an operator must be able to see.
	st := p.status()
	if st["degraded"] != false {
		t.Fatalf("a writer that has done nothing reported degraded: %v", st)
	}
	if _, ok := st["last_applied_at"]; ok {
		t.Fatal("a writer that has applied nothing claimed a last-applied time")
	}
	if st["plan_stale"] != true {
		t.Fatalf("a live writer with no plan did not report a stale plan: %v", st)
	}
	if st["active"] != true || st["producer_authenticated"] != true {
		t.Fatalf("health did not report the enforcement mode: %v", st)
	}

	// a clean apply
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	st = p.status()
	if st["degraded"] != false || st["last_applied_at"] == nil {
		t.Fatalf("a clean apply was not reported: %v", st)
	}
	if st["accepted_generation"] != int64(1) || st["plan_stale"] != false {
		t.Fatalf("health did not report the accepted plan: %v", st)
	}

	// a failed teardown must surface, with a reason
	tc.failDel["10.0.0.8"] = errors.New("class busy")
	if _, err := p.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	st = p.status()
	if st["degraded"] != true || st["problem"] == nil {
		t.Fatalf("a failed apply was not reported as degraded: %v", st)
	}

	// recovering clears it, so a stale problem cannot linger
	delete(tc.failDel, "10.0.0.8")
	if _, err := p.submit(context.Background(), standardPlan(3), time.Now()); err != nil {
		t.Fatal(err)
	}
	if st := p.status(); st["degraded"] != false {
		t.Fatalf("a recovered writer still reports degraded: %v", st)
	}

	// a refusal is counted and named, so an operator can see that netd is REFUSING plans rather than quietly
	// enforcing nothing
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err == nil {
		t.Fatal("expected the stale plan to be refused")
	}
	st = p.status()
	if st["last_refusal"] != shapeplan.ReasonStaleGeneration || st["refused_total"] != int64(1) {
		t.Fatalf("health did not report the refusal: %v", st)
	}
}

// An expired plan with no replacement means the producer has gone quiet: what is installed is no longer known
// to be current. That is a health fact, not an internal detail.
func TestExpiredPlanShowsAsStale(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	env := standardPlan(1)
	now := time.Now()
	env.ExpiresAt = now.Add(30 * time.Second)
	if _, err := p.submit(context.Background(), env, now); err != nil {
		t.Fatal(err)
	}
	if p.status()["plan_stale"] != false {
		t.Fatal("a fresh plan was reported stale")
	}
	// wind the accepted plan's expiry into the past, as a producer that died would leave it
	p.mu.Lock()
	p.lastAccepted.ExpiresAt = time.Now().Add(-time.Second)
	p.mu.Unlock()
	if p.status()["plan_stale"] != true {
		t.Fatal("an expired plan was not reported as stale")
	}
}
