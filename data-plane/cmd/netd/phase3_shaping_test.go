package main

// Tests for the single Phase-3 shaping writer and its control plane.

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
	op     string
	bridge string
	ip     string
	minor  int
	down   int
	up     int
	group  string
}

type fakeTC struct {
	mu      sync.Mutex
	calls   []tcCall
	failAdd map[string]error
	failDel map[string]error
	// installed models kernel classes (device -> minor -> class); reconciliation needs a fake that remembers what a prior plan or crash left.
	installed map[string]map[int]tcCall
	// forwarding models kernel filters separately: a prepared class is installed but not forwarding.
	forwarding map[string]map[int]bool
	readErr    map[string]error

	failPrepare        map[string]error
	failPrepareUpload  map[string]error
	failActivate       map[string]error
	failActivateUpload map[string]error
	failReRate         map[string]error
	failAbort          map[string]bool

	groups  map[string]int
	parents map[string]string
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

// PrepareSession installs download+upload classes without forwarding filters, clearing any stale slot first.
func (f *fakeTC) EnsureGroupClass(ctx context.Context, ifc, groupCid string, kbps int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groups == nil {
		f.groups = map[string]int{}
	}
	f.calls = append(f.calls, tcCall{op: "group", bridge: ifc, ip: groupCid, down: kbps})
	f.groups[ifc+"|"+groupCid] = kbps
	return nil
}

func (f *fakeTC) PrepareSessionIn(ctx context.Context, bridge string, ip net.IP, down, up int, groupCid string) error {
	f.mu.Lock()
	if f.parents == nil {
		f.parents = map[string]string{}
	}
	parent := groupCid
	if parent == "" {
		parent = shape.RootParent
	}
	f.parents[bridge+"|"+ip.String()] = parent
	f.mu.Unlock()
	return f.PrepareSession(ctx, bridge, ip, down, up)
}

func (f *fakeTC) groupRate(ifc, groupCid string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.groups[ifc+"|"+groupCid]
	return v, ok
}

func (f *fakeTC) parentOf(bridge, ip string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parents[bridge+"|"+ip]
}

func (f *fakeTC) PrepareSession(ctx context.Context, bridge string, ip net.IP, down, up int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	ifb := shape.IFBName(bridge)
	f.calls = append(f.calls, tcCall{op: "prepare", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up})
	f.delClass(bridge, minor)
	f.delClass(ifb, minor)
	if err, ok := f.failPrepare[ip.String()]; ok {
		return err
	}
	f.put(bridge, minor, tcCall{op: "prepare", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up})
	if err, ok := f.failPrepareUpload[ip.String()]; ok {
		f.delClass(bridge, minor)
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
		f.setFwd(bridge, minor, false)
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
	f.setFwd(bridge, minor, false)
	f.setFwd(ifb, minor, false)
	if f.failAbort[ip.String()] {
		return fmt.Errorf("abort: class %d could not be removed", minor)
	}
	f.delClass(bridge, minor)
	f.delClass(ifb, minor)
	return nil
}

func (f *fakeTC) RemoveClassification(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	f.calls = append(f.calls, tcCall{op: "declassify", bridge: bridge, ip: ip.String(), minor: minor})
	f.setFwd(bridge, minor, false)
	f.setFwd(shape.IFBName(bridge), minor, false)
	return nil
}

func (f *fakeTC) ReRateSessionIn(ctx context.Context, bridge string, ip net.IP, down, up int, groupCid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	minor, _ := shape.MinorForIP(ip)
	ifb := shape.IFBName(bridge)
	f.calls = append(f.calls, tcCall{op: "rerate", bridge: bridge, ip: ip.String(), minor: minor, down: down, up: up, group: groupCid})
	if err, ok := f.failReRate[ip.String()]; ok {
		return err
	}
	// A class change never deletes and recreates, so counters are preserved.
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
	// A managed class is a pair (bridge download, ifb upload); stray removal drops both.
	f.delClass(bridge, minor)
	f.delClass(shape.IFBName(bridge), minor)
	return nil
}

func (f *fakeTC) put(bridge string, minor int, c tcCall) {
	if f.installed[bridge] == nil {
		f.installed[bridge] = map[int]tcCall{}
	}
	f.installed[bridge][minor] = c
}

func (f *fakeTC) wipe() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = map[string]map[int]tcCall{}
	f.forwarding = map[string]map[int]bool{}
}

func (f *fakeTC) wipeBridge(bridge string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.installed, bridge)
	delete(f.installed, shape.IFBName(bridge))
	delete(f.forwarding, bridge)
	delete(f.forwarding, shape.IFBName(bridge))
}

// preinstall seeds a class no plan claims, like a crash leftover.
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

// countInstalled counts download classes only, not the paired ifbs.
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

// countForwarding counts sessions with an active download filter, as distinct from merely prepared.
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
	return &phase3Shaping{shp: tc, mode: liveMode(), authz: shapingAuthz{allowedUID: testUID, configured: true},
		generations: &fakeGenerations{},
		// Every writer needs a monotonic security clock; without one it refuses to authorize anyone.
		secClock: &fixedSecurityClock{ms: 60_000, bootID: "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"}}
}

// envelope builds a plan declaring its sessions' bridges plus an empty one, so stray removal is exercised there too.
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

// A dark appliance refuses to mutate tc on its own authority, not on trust that acctd will not submit.
func TestDarkNetdRefusesEveryPlan(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc, authz: shapingAuthz{allowedUID: testUID, configured: true}}
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
	srv := &server{phase3: p}
	epochReq := httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil)
	epochReq = epochReq.WithContext(context.WithValue(epochReq.Context(), peerConnKey{}, producerIdentity{UID: testUID}))
	rec := httptest.NewRecorder()
	http.HandlerFunc(srv.phase3EpochsHandler).ServeHTTP(rec, epochReq)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a dark netd served class generations: %d", rec.Code)
	}
}

// Identity comes from SO_PEERCRED; a header is forgeable.
func TestOnlyTheAuthenticatedProducerMaySubmit(t *testing.T) {
	tc := newFakeTC()
	srv := &server{phase3: liveWriter(tc)}
	h := http.HandlerFunc(srv.phase3ShapingHandler)
	raw, _ := json.Marshal(standardPlan(1))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(raw)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unauthenticated caller was allowed to submit: %d", rec.Code)
	}

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

	req3 := httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(raw))
	req3 = req3.WithContext(context.WithValue(req3.Context(), peerConnKey{}, producerIdentity{UID: testUID, PID: 42}))
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("the authorized producer was refused: %d %s", rec3.Code, rec3.Body.String())
	}
}

// With no producer uid configured, authorization fails closed.
func TestUnconfiguredProducerFailsClosed(t *testing.T) {
	a := newShapingAuthz(func(string) string { return "" })
	if a.configured {
		t.Fatal("an empty producer uid was treated as configured")
	}
	if err := a.authorize(producerIdentity{UID: 0}, nil); err == nil {
		t.Fatal("an unconfigured authorizer allowed root to submit")
	}
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

// A plan is checked against netd's own scope; applying another site's state would enforce someone else's policy.
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

// A delayed or replayed older plan must never reinstate revoked access.
func TestStaleAndReplayedPlansAreRefused(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)

	if _, err := p.submit(context.Background(), standardPlan(5), time.Now()); err != nil {
		t.Fatalf("generation 5 was refused: %v", err)
	}
	res, err := p.submit(context.Background(), standardPlan(4), time.Now())
	if err == nil || res.Reason != shapeplan.ReasonStaleGeneration {
		t.Fatalf("a stale plan was accepted: %+v", res)
	}
	// The same generation with different contents means the producer's numbering is broken.
	conflicting := envelope(5, shapeplan.Session{SessionID: "live-1", IP: "10.0.0.77", Bridge: "br-guest",
		DownKbps: 1000, UpKbps: 1000, Entitled: true})
	res2, err2 := p.submit(context.Background(), conflicting, time.Now())
	if err2 == nil || res2.Reason != shapeplan.ReasonGenerationConflict {
		t.Fatalf("a conflicting duplicate generation was accepted: %+v", res2)
	}
	if _, err := p.submit(context.Background(), standardPlan(5), time.Now()); err != nil {
		t.Fatalf("an exact replay was refused: %v", err)
	}
	if _, err := p.submit(context.Background(), standardPlan(6), time.Now()); err != nil {
		t.Fatalf("a newer plan was refused: %v", err)
	}
}

// Generation history survives a netd restart, else an old plan could be re-accepted.
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

// Audit Phase 8 M8: a plan whose generation could not be saved durably is applied but not reported converged; a
// corrupt record is reported, not silently read as "no history"; a far-future expiry is refused.
func TestAcceptedGenerationSaveFailureIsDegraded(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := liveWriter(newFakeTC())
	p.store = &planStore{path: filepath.Join(blocker, "plan.json")} // its parent is a file: every write fails
	res, err := p.submit(context.Background(), standardPlan(3), time.Now())
	if err != nil || !res.Accepted {
		t.Fatalf("submit: %+v %v", res, err)
	}
	if !res.Degraded || p.hasConverged {
		t.Fatalf("a plan whose replay guard was not saved was reported converged: %+v", res)
	}

	corrupt := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(corrupt, []byte("{\"generation\": 7, trunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	p2 := liveWriter(newFakeTC())
	p2.store = &planStore{path: corrupt}
	res, err = p2.submit(context.Background(), standardPlan(4), time.Now())
	if err != nil || !res.Degraded || !strings.Contains(strings.Join(res.Problems, " "), "could not be read") {
		t.Fatalf("a corrupt replay record was not reported: %+v %v", res, err)
	}

	far := standardPlan(5)
	far.ExpiresAt = time.Now().Add(24 * time.Hour)
	if res, err := p2.submit(context.Background(), far, time.Now()); err == nil || res.Reason != "plan_validity_too_long" {
		t.Fatalf("a plan valid for a day was accepted: %+v %v", res, err)
	}
}

// Audit Phase 8 L3: one failed class-state save withholds fresh grace only until the next successful save, not until
// a restart; an unreadable journal still withholds it.
func TestClassStateSaveFailureClearsAfterTheNextSave(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := liveWriter(newFakeTC())
	p.classStore = &classStore{path: filepath.Join(blocker, "classes.json")}
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !p.unprovenUnknown {
		t.Fatal("a failed class-state save did not withhold fresh grace")
	}
	p.classStore = &classStore{path: filepath.Join(dir, "classes.json")}
	if _, err := p.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	if p.unprovenUnknown {
		t.Fatal("one failed save still denies every new guest after a successful save")
	}
	p.journalUnreadable, p.unprovenUnknown = true, true
	p.classStore = &classStore{path: filepath.Join(blocker, "classes.json")}
	_, _ = p.submit(context.Background(), standardPlan(3), time.Now())
	p.classStore = &classStore{path: filepath.Join(dir, "classes.json")}
	_, _ = p.submit(context.Background(), standardPlan(4), time.Now())
	if !p.unprovenUnknown {
		t.Fatal("a successful class save cleared the unknown history of an unreadable journal")
	}
}

// Teardown happens before shaping; the other order leaves ended access forwarded while capacity is handed out.
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

// A class for a session that no longer exists anywhere is only found by enumerating the kernel.
func TestStrayClassesAreRemoved(t *testing.T) {
	tc := newFakeTC()
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

// A leftover class on a bridge with no sessions is found only via the plan's declared bridge list.
func TestStrayOnAnEmptyBridgeIsStillRemoved(t *testing.T) {
	tc := newFakeTC()
	tc.preinstall("br-empty", "10.0.0.77")
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

// A session on an undeclared bridge is refused; its class would never be removed.
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

// An unreadable kernel means unknown strays; reporting success would claim exact enforcement.
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

// Reapplying the same plan converges, which makes restart recovery ordinary.
func TestShapingIsIdempotentAndConverges(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, first := tc.snapshot()

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

// A partially applied plan is reported degraded, especially a teardown failure (traffic may still be forwarded).
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

// A session with no addressing or rates stays unshaped; a zero rate would be a silent full-speed pass.
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

// Two sessions mapping to one class is a producer defect; neither is installed and the conflict is reported.
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

// Audit Phase 8 L6: a third session claiming an already contested class is refused too.
func TestAThirdSessionOnAContestedClassIsRefused(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)
	res, err := p.submit(context.Background(), envelope(1,
		shapeplan.Session{SessionID: "a", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true},
		shapeplan.Session{SessionID: "b", IP: "10.0.16.1", Bridge: "br-guest", DownKbps: 2000, UpKbps: 900, Entitled: true},
		shapeplan.Session{SessionID: "c", IP: "10.0.32.1", Bridge: "br-guest", DownKbps: 3000, UpKbps: 900, Entitled: true},
	), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || tc.countInstalled() != 0 {
		t.Fatalf("a third claimant of a contested class was installed: %+v", res)
	}
}

// Audit Phase 8 L7: the stray sweep compares the address, not only the class number.
func TestStrayAuthorizationWithTheSameClassNumberIsRemoved(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := liveWriter(tc)
	p.gate = g
	stale := net.ParseIP("10.0.16.1") // same low 12 bits as 10.0.0.1
	if err := g.Authorize(context.Background(), "br-guest", stale, time.Minute); err != nil {
		t.Fatal(err)
	}
	res, err := p.submit(context.Background(), envelope(1,
		shapeplan.Session{SessionID: "a", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 1000, UpKbps: 500, Entitled: true},
	), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if g.isAuthorized("br-guest", "10.0.16.1") {
		t.Fatalf("a stale authorization for another address with the same class number was kept: %+v", res)
	}
}

// Concurrent submissions must not interleave into a state neither plan describes.
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

// The endpoint accepts only a complete envelope so the sides cannot drift into exchanging deltas.
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

	if rec2 := post([]byte(`{"contract_version":"phase3-shaping/1","remove_one":"s1"}`)); rec2.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field was accepted: %d", rec2.Code)
	}

	if rec3 := post([]byte(`{"tear":[],"shape":[]}`)); rec3.Code != http.StatusBadRequest {
		t.Fatalf("a legacy delta body was accepted: %d %s", rec3.Code, rec3.Body.String())
	}

	// An unsupported version is 400, a scope or staleness refusal is 409; a /1 producer states no access boundary, so accepting it would mean inventing a lease length.
	badVersion := standardPlan(2)
	badVersion.ContractVersion = "phase3-shaping/1"
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

// Health must report states an operator can act on: "nothing known in force" and "no longer confirmed" must not read as healthy.
func TestHealthReportsTruthfulShapingState(t *testing.T) {
	tc := newFakeTC()
	p := liveWriter(tc)

	// Live with nothing in force is degraded, not healthy with a buried plan_stale.
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

	// CONVERGED. gone-1 is absent from this plan: an already-torn-down session is not torn down again, so the failure staged below hits a real attempt.
	plan1 := envelope(1,
		shapeplan.Session{SessionID: "gone-2", DeviceID: "dev-gone-2", IP: "10.0.0.9", Bridge: "br-guest"},
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000, Entitled: true},
		shapeplan.Session{SessionID: "live-2", DeviceID: "dev-live-2", IP: "10.0.0.2", Bridge: "br-lan", DownKbps: 4000, UpKbps: 1500, Entitled: true},
	)
	if _, err := p.submit(context.Background(), plan1, time.Now()); err != nil {
		t.Fatal(err)
	}
	st = p.status()
	if st["state"] != shapingConverged || st["degraded"] != false {
		t.Fatalf("a clean apply: %v", st)
	}
	if st["converged_generation"] != int64(1) || st["admitted_generation"] != int64(1) || st["plan_stale"] != false {
		t.Fatalf("health did not report the converged plan: %v", st)
	}

	tc.failDel["10.0.0.8"] = errors.New("class busy")
	if _, err := p.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	st = p.status()
	if st["state"] != shapingDegradedState || st["degraded"] != true || st["problem"] == nil {
		t.Fatalf("a failed apply: %v", st)
	}
	// Admission advanced but convergence did not; claiming generation 2 in force would be a false record.
	if st["admitted_generation"] != int64(2) {
		t.Fatalf("a partially applied plan was not admitted: %v", st)
	}
	if st["converged_generation"] != int64(1) {
		t.Fatalf("a partially applied plan was recorded as converged: %v", st)
	}

	// Resubmitting the same generation and hash is allowed and finishes convergence.
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

	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err == nil {
		t.Fatal("expected the stale plan to be refused")
	}
	st = p.status()
	if st["last_refusal"] != shapeplan.ReasonStaleGeneration || st["refused_total"] != int64(1) {
		t.Fatalf("health did not report the refusal: %v", st)
	}
}

// An expired plan means the producer went quiet; nothing confirms what is installed.
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

func TestDarkIsNotDegraded(t *testing.T) {
	p := &phase3Shaping{shp: newFakeTC(), authz: shapingAuthz{allowedUID: testUID, configured: true}}
	st := p.status()
	if st["state"] != shapingDark || st["degraded"] != false {
		t.Fatalf("a dark writer: %v", st)
	}
}

type fakeGenerations struct {
	mu     sync.Mutex
	last   int64
	err    error
	issued []int64
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

func writerWithState(tc *fakeTC, gens generationAllocator, path, bootID string) *phase3Shaping {
	p := liveWriter(tc)
	p.classStore = &classStore{path: path}
	p.generations = gens
	prev, _, _ := p.classStore.load()
	inv, verified := kernelInventory(context.Background(), tc, bridgesIn(prev))
	p.restore(prev, bootID, inv, verified)
	return p
}

// A restart with the kernel intact keeps every generation; otherwise acctd's checkpoint would reject later observations as stale.
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

// A reboot loses every class; successors must be strictly newer or a checkpoint measures a counter restarting at zero.
func TestRebootAllocatesStrictlyNewerGenerations(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{}

	p := writerWithState(tc, gens, state, "boot-aaaa")
	if _, err := p.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := p.Epochs()

	tc.wipe()
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

// Same boot but the class is gone: only the kernel reading catches it, and the successor must be a new series.
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

// Same boot, same minor, different session: a presence check alone would hand the new guest the previous guest's checkpoint.
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

// Corrupted durable state must not reuse an older generation; the allocator is the authority.
func TestCorruptedStateWithOlderGenerationsNeverReuses(t *testing.T) {
	state := filepath.Join(t.TempDir(), "classes.json")
	tc := newFakeTC()
	gens := &fakeGenerations{last: 5}

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

// The generation is not derived from the wall clock, which may jump backwards.
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
	if _, err := restarted.submit(context.Background(), standardPlan(2), time.Now()); err != nil {
		t.Fatal(err)
	}
	for k, old := range first {
		if got := restarted.Epochs()[k]; got <= old {
			t.Fatalf("class %s: generation %d did not advance past %d despite a backwards clock", k, got, old)
		}
	}
}

// Without generation authority a class is not made accountable; no local value may be manufactured.
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

// Epochs() reports only classes the kernel is verified to hold.
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

// The class-generation endpoint is control-plane state and requires the same producer identity as a submission.
func TestEpochsEndpointRequiresTheAuthenticatedProducer(t *testing.T) {
	tc := newFakeTC()
	srv := &server{phase3: liveWriter(tc)}
	if _, err := srv.phase3.submit(context.Background(), standardPlan(1), time.Now()); err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(srv.phase3EpochsHandler)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unauthenticated caller read class generations: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "live-1") {
		t.Fatal("a refused read disclosed managed sessions")
	}

	other := httptest.NewRequest(http.MethodGet, "/v1/phase3/shaping/epochs", nil)
	other = other.WithContext(context.WithValue(other.Context(), peerConnKey{}, producerIdentity{UID: testUID + 1}))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, other)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("uid %d read class generations: %d", testUID+1, rec2.Code)
	}

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

// The whole reboot sequence end to end: the guarded failure is an interaction, so durable admission and class state must recover together and checkpoints continue without a regression or phantom delta.
func TestRebootConvergesFromOneSubmission(t *testing.T) {
	dir := t.TempDir()
	classState := filepath.Join(dir, "classes.json")
	planState := filepath.Join(dir, "plan.json")
	gens := &fakeGenerations{}
	tc := newFakeTC()

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

	// Reboot: every tc class is gone except one stray left by a partial teardown.
	tc.wipe()
	tc.preinstall("br-guest", "10.0.0.55")

	after := liveWriter(tc)
	after.classStore = &classStore{path: classState}
	after.store = &planStore{path: planState}
	after.generations = gens
	prev, _, _ := after.classStore.load()
	inv, verified := kernelInventory(context.Background(), tc, bridgesIn(prev))
	after.restore(prev, "boot-bbbb", inv, verified)

	if res, err := after.submit(context.Background(), standardPlan(6), time.Now()); err == nil {
		t.Fatalf("a superseded plan was accepted after a reboot: %+v", res)
	} else if res.Reason != shapeplan.ReasonStaleGeneration {
		t.Fatalf("refusal reason after reboot = %q", res.Reason)
	}

	if st := after.status(); st["state"] != shapingNoPlan || st["degraded"] != true {
		t.Fatalf("a rebooted appliance did not report that nothing is in force: %v", st)
	}

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

	// Recreated classes are strictly newer generations, so a held checkpoint sees a reset, not a backwards counter.
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

func classState4() classState {
	return classState{Classes: map[string]managedClass{}}
}

// hasForwarding reports whether a minor is classifying on a bridge (download filter installed).
func (f *fakeTC) hasForwarding(bridge string, minor int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forwarding[bridge][minor]
}

// A handover must not take the new owner offline: teardown works by address, so the naive order revokes the element and class the surviving session is about to receive.
func TestHandoverLeavesTheNewOwnersKernelStateAlone(t *testing.T) {
	const handoverIP = "10.0.0.44"
	tc := newFakeTC()
	p := liveWriter(tc)
	gate := newFakeGate()
	p.gate = gate

	older := shapeplan.Session{SessionID: "s-older", DeviceID: "dev", IP: handoverIP, Bridge: "br-guest",
		Entitled: false, EndReason: "SUPERSEDED_ON_ADDRESS"}
	newer := shapeplan.Session{SessionID: "s-newer", DeviceID: "dev", IP: handoverIP, Bridge: "br-guest",
		DownKbps: 9000, UpKbps: 4000, Entitled: true}

	if _, err := p.submit(context.Background(), envelope(1, newer), time.Now()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if !gate.isAuthorized("br-guest", handoverIP) {
		t.Fatal("the new owner was not authorized by the first pass")
	}
	epochBefore := p.epochs[classKey("br-guest", "s-newer")]
	if epochBefore == 0 {
		t.Fatal("the new owner has no accounting generation")
	}

	res, err := p.submit(context.Background(), envelope(2, older, newer), time.Now())
	if err != nil {
		t.Fatalf("handover pass: %v", err)
	}
	if res.Failed != 0 {
		t.Fatalf("a handover reported failures: %+v", res.Problems)
	}
	if !gate.isAuthorized("br-guest", handoverIP) {
		t.Fatal("the handover revoked the surviving session's packet authorization")
	}
	if fwd, err := tc.SessionForwarding(context.Background(), "br-guest", net.ParseIP(handoverIP)); err != nil || !fwd {
		t.Fatalf("the handover deleted the surviving session's class (fwd=%v err=%v)", fwd, err)
	}
	if got := p.epochs[classKey("br-guest", "s-newer")]; got != epochBefore {
		t.Fatalf("the surviving session's accounting generation changed %d -> %d across a handover: its "+
			"counter series was restarted for a teardown that was not its own", epochBefore, got)
	}
	if _, held := p.classes[classKey("br-guest", "s-older")]; held {
		t.Fatal("the superseded session is still recorded as holding a managed class")
	}
	if res.TornDown != 1 {
		t.Fatalf("torn_down=%d, want 1 — the superseded session must still be recorded as ended", res.TornDown)
	}
}
