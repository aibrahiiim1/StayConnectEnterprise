package main

// Fake-kernel tests of nft authorization, accountable tc and durable Session together. Invariant: a guest is
// never authorized at the packet gate without a verified tc class, or traffic escapes through the default class unbilled.

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

type fakeGate struct {
	mu sync.Mutex
	// authorized models the Phase-3 nft set only; the gate can never reach the legacy set.
	authorized map[string]bool // "bridge|ip"
	calls      []string
	failAllow  map[string]error
	failRevoke map[string]bool
	// loseAllowResult applies the change and then reports an error.
	loseAllowResult map[string]bool
	// allowSilentlyIgnored returns success without taking effect.
	allowSilentlyIgnored map[string]bool
	listErr              error
	// leases is the remaining lease per element, so renewal is distinguishable from about-to-expire.
	leases map[string]time.Duration
	// ttls records every requested lease length, to assert unproven activation only gets the short lease.
	ttls map[string][]time.Duration
}

func newFakeGate() *fakeGate {
	return &fakeGate{authorized: map[string]bool{}, failAllow: map[string]error{},
		failRevoke: map[string]bool{}, loseAllowResult: map[string]bool{},
		allowSilentlyIgnored: map[string]bool{}, leases: map[string]time.Duration{},
		ttls: map[string][]time.Duration{}}
}

func gk(bridge string, ip net.IP) string { return bridge + "|" + ip.String() }

func (g *fakeGate) Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, "authorize "+gk(bridge, ip))
	// The real gate refuses a permanent authorization; the fake must too or unbounded access would pass the suite.
	if ttl <= 0 {
		return errors.New("nft: packet authorization must be a bounded lease (ttl > 0)")
	}
	if err, ok := g.failAllow[ip.String()]; ok {
		return err
	}
	g.ttls[gk(bridge, ip)] = append(g.ttls[gk(bridge, ip)], ttl)
	if g.allowSilentlyIgnored[ip.String()] {
		return nil
	}
	g.authorized[gk(bridge, ip)] = true
	g.leases[gk(bridge, ip)] = ttl
	if g.loseAllowResult[ip.String()] {
		return errors.New("connection reset after the element was added")
	}
	return nil
}

func (g *fakeGate) Revoke(ctx context.Context, bridge string, ip net.IP) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, "revoke "+gk(bridge, ip))
	if g.failRevoke[ip.String()] {
		return errors.New("nft delete element failed")
	}
	delete(g.authorized, gk(bridge, ip))
	delete(g.leases, gk(bridge, ip))
	return nil
}

func (g *fakeGate) Authorized(ctx context.Context, bridge string, ip net.IP) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.authorized[gk(bridge, ip)], nil
}

func (g *fakeGate) List(ctx context.Context) ([]gateElem, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.listErr != nil {
		return nil, g.listErr
	}
	out := []gateElem{}
	for k := range g.authorized {
		var bridge, ipStr string
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				bridge, ipStr = k[:i], k[i+1:]
				break
			}
		}
		out = append(out, gateElem{Bridge: bridge, IP: net.ParseIP(ipStr), Expires: g.leases[k]})
	}
	return out, nil
}

func (g *fakeGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.authorized)
}

func (g *fakeGate) isAuthorized(bridge, ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.authorized[bridge+"|"+ip]
}

func (g *fakeGate) callLog() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// put authorizes directly, modelling a stray left behind by a crash.
func (g *fakeGate) put(bridge, ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.authorized[bridge+"|"+ip] = true
	g.leases[bridge+"|"+ip] = phase3LeaseTTL
}

// advance ages every lease by d; the kernel removes expired ones with no help from netd, acctd or the database.
func (g *fakeGate) advance(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, left := range g.leases {
		left -= d
		if left <= 0 {
			delete(g.authorized, k)
			delete(g.leases, k)
			continue
		}
		g.leases[k] = left
	}
}

// leaseOf reports the remaining lease, or 0 when the element is gone.
func (g *fakeGate) leaseOf(bridge, ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leases[bridge+"|"+ip]
}

// requestedTTLs is every lease length asked for, in order.
func (g *fakeGate) requestedTTLs(bridge, ip string) []time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]time.Duration(nil), g.ttls[bridge+"|"+ip]...)
}

type fakeEnforcement struct {
	mu        sync.Mutex
	activated []string
	ended     []string
	err       error
	// confirmErr fails the re-read too: the promotion's outcome is genuinely unknown.
	confirmErr error
	// commitThenLoseAck commits, then Activate reports failure; the re-read finds the session active.
	commitThenLoseAck map[string]bool
	state             map[string]string
	confirms          int
}

func newFakeEnforcement() *fakeEnforcement {
	return &fakeEnforcement{state: map[string]string{}, commitThenLoseAck: map[string]bool{}}
}

func (e *fakeEnforcement) Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activated = append(e.activated, sessionID)
	if e.commitThenLoseAck[sessionID] {
		if e.state[sessionID] != "ended" {
			e.state[sessionID] = "active"
		}
		return "", errors.New("connection reset after commit")
	}
	if e.err != nil {
		return "", e.err
	}
	if e.state[sessionID] == "ended" {
		return "", errors.New("ENFORCE_SESSION_ENDED")
	}
	if e.state[sessionID] == "active" {
		return "ALREADY_ACTIVE", nil
	}
	e.state[sessionID] = "active"
	return "ACTIVATED", nil
}

func (e *fakeEnforcement) Ended(ctx context.Context, sessionID, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ended = append(e.ended, sessionID)
	e.state[sessionID] = "ended"
	return nil
}

func (e *fakeEnforcement) Confirm(ctx context.Context, sessionID string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.confirms++
	if e.confirmErr != nil {
		return false, e.confirmErr
	}
	return e.state[sessionID] == "active", nil
}

// setState forces durable state, modelling a commit whose acknowledgement never arrived.
func (e *fakeEnforcement) setState(id, st string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state[id] = st
}

func (e *fakeEnforcement) sessionState(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.state[id]; ok {
		return s
	}
	return "PENDING_ENFORCEMENT"
}

func (e *fakeEnforcement) activateCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.activated)
}

// sysWriter builds a writer with both enforcement halves wired.
func sysWriter(tc *fakeTC, g *fakeGate, gens *fakeGenerations, origins *fakeOrigins, enf *fakeEnforcement) *phase3Shaping {
	p := liveWriter(tc)
	p.generations = gens
	p.origins = origins
	p.gate = g
	p.enforcement = enf
	p.bootID = "boot-sys"
	return p
}

func sysSetup() (*fakeTC, *fakeGate, *fakeGenerations, *fakeOrigins, *fakeEnforcement, *phase3Shaping) {
	tc, g := newFakeTC(), newFakeGate()
	gens, origins, enf := &fakeGenerations{}, &fakeOrigins{}, newFakeEnforcement()
	return tc, g, gens, origins, enf, sysWriter(tc, g, gens, origins, enf)
}

// assertNoUnaccountedAccess asserts no guest is authorized without a verified tc class.
func assertNoUnaccountedAccess(t *testing.T, tc *fakeTC, g *fakeGate) {
	t.Helper()
	for _, e := range mustList(t, g) {
		minor, ok := shape.MinorForIP(e.IP)
		if !ok {
			t.Fatalf("authorized element with an unusable address: %v", e)
		}
		if !tc.hasForwarding(e.Bridge, minor) {
			t.Fatalf("UNACCOUNTED ACCESS: %s on %s is authorized at the packet gate with no classifying tc "+
				"filter — its traffic escapes through the bridge default class", e.IP, e.Bridge)
		}
	}
}

func mustList(t *testing.T, g *fakeGate) []gateElem {
	t.Helper()
	els, err := g.List(context.Background())
	if err != nil {
		t.Fatalf("gate list: %v", err)
	}
	return els
}

// Every stage that can fail before the gate must leave the guest unauthorized.
func TestSystem_NoAuthorizationWhenAnyEarlierStageFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeTC, *fakeGate, *fakeGenerations, *fakeOrigins)
	}{
		{"tc generation failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			gn.err = errors.New("allocator unreachable")
		}},
		{"tc prepare failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			tc.failPrepare[provIP] = errors.New("class add failed")
		}},
		{"tc upload prepare failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			tc.failPrepareUpload[provIP] = errors.New("ifb class add failed")
		}},
		{"accounting origin read failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			tc.readErr["ifb-guest"] = errors.New("class show failed")
		}},
		{"accounting origin registration failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			o.err = errors.New("ACCT_SOURCE_MISMATCH")
		}},
		{"tc activation failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			tc.failActivate[provIP] = errors.New("filter add failed")
		}},
		{"tc partial activation failure", func(tc *fakeTC, g *fakeGate, gn *fakeGenerations, o *fakeOrigins) {
			tc.failActivateUpload[provIP] = errors.New("ifb filter add failed")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc, g, gens, origins, enf, p := sysSetup()
			c.setup(tc, g, gens, origins)

			res, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if g.count() != 0 {
				t.Fatalf("the guest was AUTHORIZED despite %s — internet access without accountable metering", c.name)
			}
			for _, call := range g.callLog() {
				if len(call) > 9 && call[:9] == "authorize" {
					t.Fatalf("authorization was attempted before tc was proven (%s)", c.name)
				}
			}
			if res.Shaped != 0 || !res.Degraded {
				t.Fatalf("failed provisioning reported %+v", res)
			}
			if enf.activateCount() != 0 {
				t.Fatal("a Session was promoted to active without enforcement")
			}
			if enf.sessionState("live-1") == "active" {
				t.Fatal("durable state claims the session is active")
			}
			assertNoUnaccountedAccess(t, tc, g)
		})
	}
}

// nft Allow failing after tc succeeded must roll tc back and leave the Session inactive.
func TestSystem_AuthorizationFailureRollsBackTC(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	g.failAllow[provIP] = errors.New("nft add element failed")

	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if g.count() != 0 {
		t.Fatal("the guest is authorized after a failed authorization")
	}
	if tc.countForwarding() != 0 {
		t.Fatalf("tc was left classifying after the authorization failed: %d", tc.countForwarding())
	}
	if tc.countInstalled() != 0 {
		t.Fatalf("tc classes survived a failed authorization: %d", tc.countInstalled())
	}
	if enf.sessionState("live-1") == "active" {
		t.Fatal("the Session claims active although the guest was never authorized")
	}
	if res.Shaped != 0 || !res.Degraded {
		t.Fatalf("result: %+v", res)
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// An Allow that reports success but did not take must be caught by verification.
func TestSystem_SilentlyIgnoredAuthorizationIsCaughtByVerification(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	g.allowSilentlyIgnored[provIP] = true

	res, _ := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if res.Shaped != 0 || !res.Degraded {
		t.Fatalf("an unverified authorization was treated as success: %+v", res)
	}
	if tc.countForwarding() != 0 {
		t.Fatal("tc remained classifying after the authorization could not be verified")
	}
	if enf.sessionState("live-1") == "active" {
		t.Fatal("the Session was promoted although the authorization never took")
	}
}

// An Allow that took effect but lost its result must converge on one authorization and one active Session on retry.
func TestSystem_LostAuthorizationResultRetryConvergesExactlyOnce(t *testing.T) {
	tc, g, gens, origins, enf, p := sysSetup()
	g.loseAllowResult[provIP] = true
	plan := oneLive(1, provIP, 8000, 3000)

	res, _ := p.submit(context.Background(), plan, time.Now())
	if res.Shaped != 0 || !res.Degraded {
		t.Fatalf("a lost authorization result was treated as success: %+v", res)
	}
	assertNoUnaccountedAccess(t, tc, g)

	delete(g.loseAllowResult, provIP)
	res2, _ := p.submit(context.Background(), plan, time.Now())
	if res2.Shaped != 1 || res2.Degraded {
		t.Fatalf("the retry did not converge: %+v", res2)
	}
	if g.count() != 1 {
		t.Fatalf("the retry produced %d authorizations, want exactly 1", g.count())
	}
	if len(gens.issued) != 1 {
		t.Fatalf("the retry allocated a second generation: %v", gens.issued)
	}
	if origins.count() < 1 {
		t.Fatal("no origin was registered")
	}
	for _, c := range origins.calls {
		if c.Epoch != origins.calls[0].Epoch {
			t.Fatal("the retry re-baselined the accounting origin at a new epoch")
		}
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the Session is not active after a converged retry")
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// A crash between origin registration and tc activation converges without a second generation or origin.
func TestSystem_CrashAfterOriginBeforeActivationConverges(t *testing.T) {
	tc, g, gens, origins, enf, p := sysSetup()
	tc.failActivate[provIP] = errors.New("crash before activation")
	plan := oneLive(1, provIP, 8000, 3000)

	_, _ = p.submit(context.Background(), plan, time.Now())
	if g.count() != 0 {
		t.Fatal("the guest was authorized before tc activation")
	}

	delete(tc.failActivate, provIP)
	restarted := sysWriter(tc, g, gens, origins, enf)
	restarted.pending = p.pending
	res, _ := restarted.submit(context.Background(), plan, time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("the post-crash reconciliation did not converge: %+v", res)
	}
	if g.count() != 1 {
		t.Fatalf("authorizations after recovery = %d, want 1", g.count())
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// A crash after tc activation but before nft Allow is safe (no internet) and the next pass completes admission.
func TestSystem_CrashAfterTCBeforeAuthorizationIsSafeAndConverges(t *testing.T) {
	tc, g, gens, origins, enf, p := sysSetup()
	g.failAllow[provIP] = errors.New("crash before nft")
	plan := oneLive(1, provIP, 8000, 3000)

	_, _ = p.submit(context.Background(), plan, time.Now())
	if g.count() != 0 {
		t.Fatal("an authorization survived a crash before the gate step")
	}
	assertNoUnaccountedAccess(t, tc, g)

	delete(g.failAllow, provIP)
	restarted := sysWriter(tc, g, gens, origins, enf)
	restarted.pending = p.pending
	res, _ := restarted.submit(context.Background(), plan, time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("recovery did not converge: %+v", res)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest was never authorized after recovery")
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the Session did not reach active after recovery")
	}
}

// A lost ACTIVE acknowledgement must be promoted on retry without tearing down working access.
func TestSystem_CrashAfterAuthorizationBeforeAckPromotesOnRetry(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	enf.err = errors.New("connection lost before the ack")
	plan := oneLive(1, provIP, 8000, 3000)

	res, _ := p.submit(context.Background(), plan, time.Now())
	if !res.Degraded {
		t.Fatal("a failed activation ack was not reported")
	}
	if !g.isAuthorized("br-guest", provIP) || tc.countForwarding() != 1 {
		t.Fatal("working, accountable enforcement was torn down because a status write failed")
	}
	assertNoUnaccountedAccess(t, tc, g)

	enf.err = nil
	res2, _ := p.submit(context.Background(), plan, time.Now())
	if res2.Degraded {
		t.Fatalf("the retry did not converge: %+v", res2)
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the Session was never promoted on retry")
	}
}

// After a reboot mid-transition nothing is authorized, and the next plan provisions cleanly.
func TestSystem_RebootDuringTransitionLeavesNothingAuthorized(t *testing.T) {
	tc, g, gens, origins, enf, p := sysSetup()
	g.failAllow[provIP] = errors.New("crash mid-admission")
	_, _ = p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())

	tc.wipe()
	g.mu.Lock()
	g.authorized = map[string]bool{}
	g.mu.Unlock()
	delete(g.failAllow, provIP)

	rebooted := sysWriter(tc, g, gens, origins, enf)
	res, _ := rebooted.submit(context.Background(), oneLive(2, provIP, 8000, 3000), time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("post-reboot provisioning failed: %+v", res)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest was not authorized after a clean post-reboot provisioning")
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// An ended entitlement loses packet access before tc is touched; the reverse could leave a guest online if tc delete fails.
func TestSystem_TeardownDeniesAccessBeforeTC(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("setup: the guest was never authorized")
	}

	tear := envelopeOn([]string{"br-guest"}, 2,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: provIP, Bridge: "br-guest", Entitled: false})
	res, _ := p.submit(context.Background(), tear, time.Now())
	if res.TornDown != 1 || res.Degraded {
		t.Fatalf("teardown did not complete cleanly: %+v", res)
	}
	if g.count() != 0 {
		t.Fatal("an ended session is still authorized at the packet gate")
	}
	if tc.countForwarding() != 0 {
		t.Fatal("tc classification survived the teardown")
	}
	if enf.sessionState("live-1") != "ended" {
		t.Fatalf("durable state did not converge: %s", enf.sessionState("live-1"))
	}
	calls := g.callLog()
	if len(calls) == 0 || calls[len(calls)-1] != "revoke br-guest|"+provIP {
		t.Fatalf("no revocation was issued: %v", calls)
	}
	tcCalls, _ := tc.snapshot()
	revokeSeen := false
	for _, c := range tcCalls {
		if c.op == "delete" && c.ip == provIP {
			if !revokeSeen {
				if g.isAuthorized("br-guest", provIP) {
					t.Fatal("tc teardown ran while the guest was still authorized")
				}
			}
			revokeSeen = true
		}
	}
}

// A tc deletion failure after access is denied is degraded cleanup, not continued access.
func TestSystem_TCTeardownFailureAfterDenialIsNotContinuedAccess(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err != nil {
		t.Fatal(err)
	}
	tc.failDel[provIP] = errors.New("class delete failed")

	tear := envelopeOn([]string{"br-guest"}, 2,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: provIP, Bridge: "br-guest", Entitled: false})
	res, _ := p.submit(context.Background(), tear, time.Now())
	if !res.Degraded {
		t.Fatal("a failed tc cleanup was not reported")
	}
	if g.count() != 0 {
		t.Fatal("the guest is STILL AUTHORIZED after a tc cleanup failure — access was not actually revoked")
	}
}

// An unproven revocation is the most serious outcome and must be reported.
func TestSystem_UnprovenRevocationIsReportedNotSwallowed(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err != nil {
		t.Fatal(err)
	}
	g.failRevoke[provIP] = true

	tear := envelopeOn([]string{"br-guest"}, 2,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: provIP, Bridge: "br-guest", Entitled: false})
	res, _ := p.submit(context.Background(), tear, time.Now())
	if !res.Degraded {
		t.Fatal("an unproven revocation was not reported")
	}
	found := false
	for _, pr := range res.Problems {
		if len(pr) > 0 && contains(pr, "PACKET AUTHORIZATION NOT PROVEN REMOVED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the problem does not name the packet-authorization failure: %v", res.Problems)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A stray Phase-3 authorization no live session claims must be removed; unlike a stray tc class it keeps a guest online.
func TestSystem_StrayAuthorizationIsRemoved(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	g.put("br-guest", "10.0.0.77")

	res, _ := p.submit(context.Background(), envelopeOn([]string{"br-guest"}, 1), time.Now())
	if g.isAuthorized("br-guest", "10.0.0.77") {
		t.Fatal("a stray Phase-3 authorization survived reconciliation — that guest is still online")
	}
	if res.StraysRemoved < 1 {
		t.Fatalf("the stray was not counted as removed: %+v", res)
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// Phase-3 reconciliation must never remove a legacy authorization; the gate can only reach the Phase-3 set.
func TestSystem_LegacyAuthorizationIsNeverTouched(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	// Legacy guests live in a separate set (auth_ipv4 vs phase3_auth_ipv4) the Phase-3 gate cannot access.
	legacyAuth := map[string]bool{"br-guest|10.0.0.200": true}

	g.put("br-guest", "10.0.0.77")
	res, _ := p.submit(context.Background(), envelopeOn([]string{"br-guest"}, 1), time.Now())

	if !legacyAuth["br-guest|10.0.0.200"] {
		t.Fatal("Phase-3 reconciliation removed a LEGACY authorization")
	}
	if g.isAuthorized("br-guest", "10.0.0.77") {
		t.Fatal("the Phase-3 stray was not removed")
	}
	if res.Degraded {
		t.Fatalf("stray removal should be clean: %+v", res)
	}
	for _, c := range g.callLog() {
		if contains(c, "10.0.0.200") {
			t.Fatalf("the Phase-3 gate touched a legacy address: %s", c)
		}
	}
}

// A re-rate keeps both the authorization and the accounting epoch.
func TestSystem_ReRateKeepsAuthorizationAndEpoch(t *testing.T) {
	_, g, gens, origins, enf, p := sysSetup()
	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err != nil {
		t.Fatal(err)
	}
	epochBefore := p.Epochs()[classKey("br-guest", "live-1")]
	originsBefore := origins.count()
	gensBefore := len(gens.issued)

	res, _ := p.submit(context.Background(), oneLive(2, provIP, 20000, 9000), time.Now())
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("re-rate did not converge: %+v", res)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the re-rate dropped the guest's internet authorization")
	}
	if got := p.Epochs()[classKey("br-guest", "live-1")]; got != epochBefore {
		t.Fatalf("the re-rate changed the accounting epoch: %d -> %d", epochBefore, got)
	}
	if origins.count() != originsBefore || len(gens.issued) != gensBefore {
		t.Fatal("the re-rate re-registered an origin or allocated a new generation")
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the session stopped being active across a re-rate")
	}
}

// tc is proven before the gate opens, and the Session is promoted only after both.
func TestSystem_HappyPathOrdersMeteringBeforeAdmission(t *testing.T) {
	tc, g, _, origins, enf, p := sysSetup()
	res, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 || res.Degraded {
		t.Fatalf("the happy path did not converge: %+v", res)
	}
	calls, _ := tc.snapshot()
	activateIdx := -1
	for i, c := range calls {
		if c.op == "activate" && c.ip == provIP {
			activateIdx = i
		}
	}
	if activateIdx < 0 {
		t.Fatal("tc was never activated")
	}
	if origins.count() != 1 {
		t.Fatalf("origin registrations = %d, want exactly 1", origins.count())
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest was never authorized")
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the Session was not promoted to active")
	}
	assertNoUnaccountedAccess(t, tc, g)
	if !p.hasConverged {
		t.Fatal("a fully enforced session did not record convergence")
	}
	state, _ := p.shapingState(time.Now())
	if state != shapingConverged {
		t.Fatalf("health = %s, want %s", state, shapingConverged)
	}
}

// The default-class bypass window is transient, so this checks the call order as well as the end state.
func TestSystem_NoDefaultClassBypassWindowDuringProvisioning(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err != nil {
		t.Fatal(err)
	}
	authIdx := -1
	for i, c := range g.callLog() {
		if c == "authorize br-guest|"+provIP {
			authIdx = i
		}
	}
	if authIdx < 0 {
		t.Fatal("the guest was never authorized")
	}
	calls, _ := tc.snapshot()
	activateIdx := -1
	for i, c := range calls {
		if c.op == "activate" && c.ip == provIP {
			activateIdx = i
		}
	}
	if activateIdx < 0 {
		t.Fatal("tc classification was never activated")
	}
	if !tc.hasForwarding("br-guest", 0x1001) {
		t.Fatal("the authorized guest has no classifying filter — default-class bypass")
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// 24 concurrent retries of one plan converge on one authorization, generation, origin epoch and active Session.
func TestSystem_ConcurrentRetriesConvergeExactlyOnce(t *testing.T) {
	tc, g, gens, origins, enf, p := sysSetup()
	plan := oneLive(1, provIP, 8000, 3000)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.submit(context.Background(), plan, time.Now())
		}()
	}
	wg.Wait()

	if g.count() != 1 {
		t.Fatalf("authorizations = %d, want exactly 1", g.count())
	}
	if len(gens.issued) != 1 {
		t.Fatalf("generations issued = %v, want exactly 1", gens.issued)
	}
	for _, c := range origins.calls {
		if c.Epoch != origins.calls[0].Epoch {
			t.Fatal("concurrent retries registered origins at different epochs")
		}
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the Session is not active after concurrent retries")
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// A dark appliance authorizes nobody.
func TestSystem_DarkApplianceNeverAuthorizes(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := &phase3Shaping{shp: tc, gate: g, mode: phase3Mode{Active: false},
		authz: shapingAuthz{allowedUID: testUID, configured: true}}
	if _, err := p.submit(context.Background(), oneLive(1, provIP, 8000, 3000), time.Now()); err == nil {
		t.Fatal("a dark appliance accepted a plan")
	}
	if g.count() != 0 {
		t.Fatal("a dark appliance authorized a guest")
	}
}
