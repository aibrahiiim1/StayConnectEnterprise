package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	uidRoot    = 0
	uidEdged   = 998
	uidPortald = 997
	uidPmsd    = 996
)

func testCallerPolicy(t *testing.T, env map[string]string) callerPolicy {
	t.Helper()
	prev := lookupUID
	lookupUID = func(name string) (int, error) {
		switch name {
		case "root", "0":
			return uidRoot, nil
		case "stayconnect":
			return uidEdged, nil
		case "stayconnect-portald":
			return uidPortald, nil
		}
		return 0, errors.New("unknown user")
	}
	t.Cleanup(func() { lookupUID = prev })
	return loadCallerPolicy(func(k string) string { return env[k] })
}

func shippedPolicy(t *testing.T) callerPolicy {
	// Mirrors deploy/systemd/stayconnect-netd.service.
	return testCallerPolicy(t, map[string]string{
		"NETD_ADMIN_CALLERS": "stayconnect,root",
		"NETD_FLOW_CALLERS":  "root",
		"NETD_EDGED_CALLER":  "stayconnect",
	})
}

func asCaller(r *http.Request, uid int) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerConnKey{}, producerIdentity{UID: uid, PID: 4242}))
}

// netdRoutes lists every route netd serves and the class that admits it.
var netdRoutes = []struct {
	method, path string
	class        routeClass
}{
	{http.MethodGet, "/v1/health", classAdmin},
	{http.MethodGet, "/v1/interfaces", classAdmin},
	{http.MethodPost, "/v1/validate", classAdmin},
	{http.MethodPost, "/v1/apply", classAdmin},
	{http.MethodPost, "/v1/reassert-confirmed", classAdmin},
	{http.MethodPost, "/v1/confirm", classAdmin},
	{http.MethodPost, "/v1/rollback", classAdmin},
	{http.MethodGet, "/v1/leases", classAdmin},
	{http.MethodGet, "/v1/pending", classAdmin},
	{http.MethodPost, "/v1/portal-ip/validate", classAdmin},
	{http.MethodPost, "/v1/flows/end", classFlows},
	{http.MethodGet, "/v1/system-network", classAdmin},
	{http.MethodPost, "/v1/system-network/validate", classAdmin},
	{http.MethodPost, "/v1/system-network/apply", classAdmin},
	{http.MethodPost, "/v1/system-network/confirm", classAdmin},
	{http.MethodPost, "/v1/system-network/rollback", classAdmin},
	{http.MethodGet, "/v1/system-network/history", classAdmin},
	{http.MethodGet, "/v1/system-network/diagnostics", classAdmin},
}

// The captive portal's identity, pmsd's and an unidentified connection are refused on every route before its
// handler runs; the Phase-3 routes refuse them in their own producer check.
func TestEveryRouteRefusesThePortalAndUnidentifiedCallers(t *testing.T) {
	srv := &server{callers: shippedPolicy(t), phase3: &phase3Shaping{authz: shapingAuthz{allowedUID: uidRoot, configured: true}}}
	h := srv.routes()
	all := append([]struct {
		method, path string
		class        routeClass
	}{}, netdRoutes...)
	all = append(all,
		struct {
			method, path string
			class        routeClass
		}{http.MethodPost, "/v1/phase3/shaping", ""},
		struct {
			method, path string
			class        routeClass
		}{http.MethodGet, "/v1/phase3/shaping/epochs", ""})
	for _, rt := range all {
		for _, uid := range []int{uidPortald, uidPmsd} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, asCaller(httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")), uid))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s answered uid %d with %d, want 403", rt.method, rt.path, uid, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))) // no peer credentials
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s answered a caller with no kernel identity with %d, want 403", rt.method, rt.path, rec.Code)
		}
	}
}

// Admitted callers get past the gate; these stubs have no store, so a 5xx is expected but never a 403.
func TestAdmittedCallersPassTheGate(t *testing.T) {
	p := shippedPolicy(t)
	for _, rt := range netdRoutes {
		admitted := map[routeClass][]int{classAdmin: {uidEdged, uidRoot}, classFlows: {uidRoot}}[rt.class]
		for _, uid := range admitted {
			reached := false
			h := p.gate(rt.class, func(w http.ResponseWriter, r *http.Request) { reached = true })
			h(httptest.NewRecorder(), asCaller(httptest.NewRequest(rt.method, rt.path, nil), uid))
			if !reached {
				t.Errorf("%s %s refused admitted uid %d", rt.method, rt.path, uid)
			}
		}
	}
	// edged is not a flow caller: a staff disconnect is scd's to make, after it has ended the session row.
	reached := false
	p.gate(classFlows, func(http.ResponseWriter, *http.Request) { reached = true })(httptest.NewRecorder(),
		asCaller(httptest.NewRequest(http.MethodPost, "/v1/flows/end", nil), uidEdged))
	if reached {
		t.Fatal("edged reached /v1/flows/end")
	}
}

// Fail closed: an unset or unresolvable list admits nobody, and says so.
func TestUnsetOrUnresolvableCallerListsAdmitNobody(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset":        {},
		"empty":        {"NETD_ADMIN_CALLERS": " , ", "NETD_FLOW_CALLERS": ""},
		"unresolvable": {"NETD_ADMIN_CALLERS": "no-such-user", "NETD_FLOW_CALLERS": "nobody-either"},
	} {
		p := testCallerPolicy(t, env)
		for _, uid := range []int{uidRoot, uidEdged, uidPortald} {
			for _, class := range []routeClass{classAdmin, classFlows} {
				reached := false
				p.gate(class, func(http.ResponseWriter, *http.Request) { reached = true })(httptest.NewRecorder(),
					asCaller(httptest.NewRequest(http.MethodGet, "/v1/health", nil), uid))
				if reached {
					t.Fatalf("%s: uid %d reached a %s route", name, uid, class)
				}
			}
		}
		if len(p.problems) == 0 {
			t.Fatalf("%s: a policy that admits nobody reported no problem", name)
		}
	}
}

// Only edged authenticates operators, so only its body actor is recorded; any other caller is recorded as itself.
func TestActorIsBelievedOnlyFromEdged(t *testing.T) {
	p := shippedPolicy(t)
	r := asCaller(httptest.NewRequest(http.MethodPost, "/v1/system-network/apply", nil), uidEdged)
	a, id, ip := p.actorFor(r.Context(), "ops@hotel.example", "4b0c2c1e-1111-4222-8333-444455556666", "192.0.2.7")
	if a != "ops@hotel.example" || id == "" || ip != "192.0.2.7" {
		t.Fatalf("edged's operator was not recorded: %q %q %q", a, id, ip)
	}
	r = asCaller(httptest.NewRequest(http.MethodPost, "/v1/system-network/apply", nil), uidRoot)
	a, id, ip = p.actorFor(r.Context(), "ops@hotel.example", "4b0c2c1e-1111-4222-8333-444455556666", "192.0.2.7")
	if !strings.HasPrefix(a, "local:") || id != "" || ip != "local" {
		t.Fatalf("a non-edged caller's self-description was recorded as the actor: %q %q %q", a, id, ip)
	}
	// No NETD_EDGED_CALLER: nobody's body actor is believed.
	p2 := testCallerPolicy(t, map[string]string{"NETD_ADMIN_CALLERS": "stayconnect"})
	r = asCaller(httptest.NewRequest(http.MethodPost, "/v1/apply", nil), uidEdged)
	if a, _, _ := p2.actorFor(r.Context(), "ops@hotel.example", "", ""); !strings.HasPrefix(a, "local:") {
		t.Fatalf("an unconfigured edged identity still had its body actor believed: %q", a)
	}
}

type fakeFlows struct {
	mu      sync.Mutex
	flushed []string
	// order is shared with the gate so the test can assert revoke-before-flush.
	order *[]string
	err   error
}

func (f *fakeFlows) FlushAddrs(_ context.Context, ips []net.IP) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ip := range ips {
		f.flushed = append(f.flushed, ip.String())
		if f.order != nil {
			*f.order = append(*f.order, "flush "+ip.String())
		}
	}
	return len(ips), f.err
}

type orderedGate struct {
	*fakeGate
	order *[]string
}

func (g orderedGate) Revoke(ctx context.Context, bridge string, ip net.IP) error {
	*g.order = append(*g.order, "revoke "+ip.String())
	return g.fakeGate.Revoke(ctx, bridge, ip)
}

func TestRevocationEndsTheAddressesFlowsOnceAndAfterTheElementIsGone(t *testing.T) {
	var order []string
	inner := newFakeGate()
	fl := &fakeFlows{order: &order}
	g := newFlushingGate(orderedGate{inner, &order}, fl)
	ip := net.ParseIP("10.0.0.5")
	ctx := context.Background()

	if err := g.Authorize(ctx, "br-guest", ip, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := g.Revoke(ctx, "br-guest", ip); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "revoke 10.0.0.5" || order[1] != "flush 10.0.0.5" {
		t.Fatalf("flows must be flushed AFTER the element is removed: %v", order)
	}
	// A quarantined session is denied every pass; a second revocation with no authorization in between does not
	// dump the conntrack table again.
	_ = g.Revoke(ctx, "br-guest", ip)
	if len(fl.flushed) != 1 {
		t.Fatalf("an address that was not re-authorized was flushed again: %v", fl.flushed)
	}
	// Re-authorized and revoked again: flushed again.
	_ = g.Authorize(ctx, "br-guest", ip, time.Minute)
	_ = g.Revoke(ctx, "br-guest", ip)
	if len(fl.flushed) != 2 {
		t.Fatalf("a re-authorized address was not flushed on its next revocation: %v", fl.flushed)
	}
}

// A flush that fails does not undo the revocation (no new flow can start) and is retried next time.
func TestFailedFlushKeepsTheRevocationAndRetries(t *testing.T) {
	inner := newFakeGate()
	fl := &fakeFlows{err: errors.New("netlink: EPERM")}
	g := newFlushingGate(inner, fl)
	ip := net.ParseIP("10.0.0.6")
	ctx := context.Background()
	_ = g.Authorize(ctx, "br-guest", ip, time.Minute)
	if err := g.Revoke(ctx, "br-guest", ip); err != nil {
		t.Fatalf("a failed flush failed the revocation: %v", err)
	}
	if inner.isAuthorized("br-guest", "10.0.0.6") {
		t.Fatal("the element survived")
	}
	_ = g.Revoke(ctx, "br-guest", ip)
	if len(fl.flushed) != 2 {
		t.Fatalf("a failed flush was not retried: %v", fl.flushed)
	}
}

// Teardown denies (which flushes) before the tc class is deleted, so no open flow runs through the bridge's
// default class; stray authorizations are revoked before stray classes.
func TestTeardownFlushesFlowsBeforeTheClassIsDeleted(t *testing.T) {
	var order []string
	tc := &orderedTC{fakeTC: newFakeTC(), order: &order}
	p := liveWriter(tc)
	inner := newFakeGate()
	p.gate = newFlushingGate(orderedGate{inner, &order}, &fakeFlows{order: &order})

	live := shapeplan.Session{SessionID: "s-1", DeviceID: "d-1", IP: "10.0.0.5", Bridge: "br-guest", DownKbps: 9000, UpKbps: 4000, Entitled: true}
	if _, err := p.submit(context.Background(), envelope(1, live), time.Now()); err != nil {
		t.Fatal(err)
	}
	order = order[:0]
	ended := live
	ended.Entitled = false
	if _, err := p.submit(context.Background(), envelope(2, ended), time.Now()); err != nil {
		t.Fatal(err)
	}
	idx := func(s string) int {
		for i, o := range order {
			if o == s {
				return i
			}
		}
		return -1
	}
	rv, fl, del := idx("revoke 10.0.0.5"), idx("flush 10.0.0.5"), idx("delete 10.0.0.5")
	if rv < 0 || fl < 0 || del < 0 || !(rv < fl && fl < del) {
		t.Fatalf("teardown order must be revoke -> flush -> delete class: %v", order)
	}
}

type orderedTC struct {
	*fakeTC
	order *[]string
}

func (o *orderedTC) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	*o.order = append(*o.order, "delete "+ip.String())
	return o.fakeTC.DeleteSession(ctx, bridge, ip)
}

func TestFlowsEndRevokesThePhase3ElementThenFlushes(t *testing.T) {
	var order []string
	inner := newFakeGate()
	p := liveWriter(newFakeTC())
	p.gate = orderedGate{inner, &order}
	ctx := context.Background()
	ip := net.ParseIP("10.0.0.7")
	_ = inner.Authorize(ctx, "br-guest", ip, time.Minute)
	_ = inner.Authorize(ctx, "br-guest", net.ParseIP("10.0.0.8"), time.Minute)

	srv := &server{phase3: p}
	out, err := srv.endFlows(ctx, ip, "", &fakeFlows{order: &order})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Phase3Managed || out.Phase3Revoked != 1 || out.FlowsEnded != 1 {
		t.Fatalf("result: %+v", out)
	}
	if inner.isAuthorized("br-guest", "10.0.0.7") || !inner.isAuthorized("br-guest", "10.0.0.8") {
		t.Fatal("the wrong element was revoked")
	}
	if len(order) < 2 || order[0] != "revoke 10.0.0.7" || order[len(order)-1] != "flush 10.0.0.7" {
		t.Fatalf("flows must be flushed after the revocation: %v", order)
	}

	// An address netd does not manage: nothing is revoked, the flows still end, and the caller is told the tc
	// class (if any) is its own.
	out, err = srv.endFlows(ctx, net.ParseIP("10.0.0.99"), "", &fakeFlows{})
	if err != nil || out.Phase3Managed || out.Phase3Revoked != 0 || out.FlowsEnded != 1 {
		t.Fatalf("unmanaged address: %+v %v", out, err)
	}
}

type panickingGate struct{ *fakeGate }

func (panickingGate) List(context.Context) ([]gateElem, error) { panic("nft output unparseable") }

// Audit Phase 8 L8: a panic inside the revocation must not leave the enforcement lock held.
func TestFlowsEndReleasesTheLockOnPanic(t *testing.T) {
	p := liveWriter(newFakeTC())
	p.gate = panickingGate{newFakeGate()}
	srv := &server{phase3: p}
	func() {
		defer func() { _ = recover() }()
		_, _ = srv.endFlows(context.Background(), net.ParseIP("10.0.0.7"), "", &fakeFlows{})
	}()
	if !p.mu.TryLock() {
		t.Fatal("a panic in endFlows left the enforcement lock held: every later pass would deadlock")
	}
	p.mu.Unlock()
}

func TestFlowsEndHandlerRefusesBadInput(t *testing.T) {
	srv := &server{phase3: liveWriter(newFakeTC())}
	for _, body := range []string{"", "{", `{"ip":"not-an-ip"}`, `{"ip":"::1"}`, `{"ip":"0.0.0.0"}`} {
		rec := httptest.NewRecorder()
		srv.flowsEnd(rec, httptest.NewRequest(http.MethodPost, "/v1/flows/end", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q answered %d, want 400", body, rec.Code)
		}
	}
}

type countingLeases struct {
	n    int
	rows []KeaLease
}

func (c *countingLeases) Leases() ([]KeaLease, error) { c.n++; return c.rows, nil }

// One lease list per pass, however many sessions the pass judges.
func TestOwnershipAsksDHCPOncePerPass(t *testing.T) {
	src := &countingLeases{rows: []KeaLease{lease("10.0.0.5", "aa:aa:aa:aa:aa:01"), lease("10.0.0.6", "aa:aa:aa:aa:aa:02"),
		lease("10.0.0.7", "aa:aa:aa:aa:aa:03")}}
	p := ownershipWriter(t, newFakeTC(), newFakeGate(), src)
	mk := func(id, ip, mac string) shapeplan.Session {
		return shapeplan.Session{SessionID: id, DeviceID: "d-" + id, IP: ip, MAC: mac, Bridge: "br-guest",
			EntitlementID: "e-" + id, DownKbps: 9000, UpKbps: 4000, Entitled: true}
	}
	env := envelope(1, mk("a", "10.0.0.5", "aa:aa:aa:aa:aa:01"), mk("b", "10.0.0.6", "aa:aa:aa:aa:aa:02"),
		mk("c", "10.0.0.7", "aa:aa:aa:aa:aa:03"))
	if _, err := p.submit(context.Background(), env, time.Now()); err != nil {
		t.Fatal(err)
	}
	if src.n != 1 {
		t.Fatalf("a pass over three sessions asked DHCP %d times, want 1", src.n)
	}
	if _, err := p.submit(context.Background(), envelope(2, env.Sessions...), time.Now()); err != nil {
		t.Fatal(err)
	}
	if src.n != 2 {
		t.Fatalf("the next pass must ask again (fresh evidence each pass): %d", src.n)
	}
}

// An ended session the producer keeps listing is torn down once, not every tick.
func TestAnEndedSessionIsNotTornDownEveryPass(t *testing.T) {
	var order []string
	tc := &orderedTC{fakeTC: newFakeTC(), order: &order}
	p := liveWriter(tc)
	p.gate = orderedGate{newFakeGate(), &order}
	gone := shapeplan.Session{SessionID: "gone", DeviceID: "d", IP: "10.0.0.8", Bridge: "br-guest"}
	for gen := int64(1); gen <= 4; gen++ {
		if _, err := p.submit(context.Background(), envelope(gen, gone), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, o := range order {
		if o == "delete 10.0.0.8" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("an ended session was torn down %d times over four passes, want 1: %v", n, order)
	}
	// Once the plan stops listing it, it is forgotten; if it ever came back it would be torn down again.
	_, _ = p.submit(context.Background(), envelope(5), time.Now())
	_, _ = p.submit(context.Background(), envelope(6, gone), time.Now())
	n = 0
	for _, o := range order {
		if o == "delete 10.0.0.8" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("a session the plan forgot and listed again was not torn down again: %v", order)
	}
}
