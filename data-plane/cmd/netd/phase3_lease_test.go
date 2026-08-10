package main

// ADVERSARIAL TESTS FOR THE BOUNDED KERNEL LEASE AND FAIL-CLOSED ACTIVATION.
//
// The enforcement suite proves that a guest is never authorized without an accountable class. These prove the
// property that only shows up once everything STOPS:
//
//	A GUEST CANNOT REMAIN ON THE INTERNET BECAUSE THE PROCESSES THAT WERE SUPPOSED TO REMOVE THEM DIED.
//
// Every other mechanism in the system needs something to be running. The plan needs acctd; the teardown needs
// netd; the revocation needs the database. The kernel lease needs nothing, which is why it is the only thing
// that can be trusted to end access when everything else has stopped being able to.
//
// The second half proves the other end of the same argument: enforcement that is in force but whose durable
// ACTIVE state was never confirmed must not become permanent either. It is held on a SHORT lease, and if
// nothing ever proves the promotion it is failed closed rather than renewed forever.
//
// These are FAKE-KERNEL tests: nft and tc are modelled, and the fake gate expires leases when time is
// advanced, exactly as the kernel would with nothing running. They prove orchestration, ordering and
// arithmetic. They are NOT live evidence, and the real nft timeout behaviour they depend on is proven
// separately by the disposable-namespace kernel suite (internal/kerneltest).

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// leasePlan is one entitled session with an optional hard access boundary.
func leasePlan(gen int64, endsAt *time.Time) shapeplan.Envelope {
	return envelopeOn([]string{"br-guest"}, gen,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: provIP, Bridge: "br-guest",
			DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: endsAt})
}

// leasePlanAt is the same plan stamped at a SIMULATED time. These tests move time forward by minutes, and an
// envelope stamped from the wall clock would be refused as expired long before the property under test is
// reached — which would make a lease test fail for a reason that has nothing to do with leases.
func leasePlanAt(gen int64, endsAt *time.Time, now time.Time) shapeplan.Envelope {
	env := leasePlan(gen, endsAt)
	env.GeneratedAt = now.UTC()
	env.ExpiresAt = now.UTC().Add(90 * time.Second)
	return env
}

func mustConverge(t *testing.T, p *phase3Shaping, env shapeplan.Envelope, now time.Time) shapingPlanResponse {
	t.Helper()
	res, err := p.submit(t.Context(), env, now)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if res.Degraded {
		t.Fatalf("healthy plan did not converge: %+v", res.Problems)
	}
	return res
}

// ---- A. the lease is real, bounded, and renewed only by health ---------------

// The gate refuses ttl<=0, so a provisioning that ever asked for a permanent element would fail outright.
// Asserting the happy path succeeds AND that the lease it asked for is the bounded one is what makes this a
// statement about the design rather than about the fake.
func TestLease_AuthorizationIsAlwaysBounded(t *testing.T) {
	_, g, gens, origins, enf, p := sysSetup()
	_, _, _ = gens, origins, enf
	now := time.Now()
	mustConverge(t, p, leasePlan(1, nil), now)

	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("a healthy session was not authorized")
	}
	ttls := g.requestedTTLs("br-guest", provIP)
	if len(ttls) == 0 {
		t.Fatal("no lease was ever requested")
	}
	for _, ttl := range ttls {
		if ttl <= 0 {
			t.Fatalf("a PERMANENT authorization was requested (ttl=%v): it would outlive every process that maintains it", ttl)
		}
		if ttl > phase3LeaseTTL {
			t.Fatalf("lease %v exceeds the documented bound %v", ttl, phase3LeaseTTL)
		}
	}
	// and the settled lease, once durable ACTIVE is proven, is the full one
	if got := g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("settled lease = %v, want %v", got, phase3LeaseTTL)
	}
}

// A proven session is admitted on the SHORT provisional lease first and only extended once durable state
// confirms it. The order matters: it is what makes a crash between the two harmless.
func TestLease_ProvisionalFirstThenFullOnProvenActivation(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	mustConverge(t, p, leasePlan(1, nil), time.Now())

	ttls := g.requestedTTLs("br-guest", provIP)
	if len(ttls) < 2 {
		t.Fatalf("expected a provisional lease then a full one, got %v", ttls)
	}
	if ttls[0] != phase3ProvisionalLease {
		t.Fatalf("the guest was admitted on a %v lease, not the provisional %v — a crash before the "+
			"promotion would leave them online for that long with nothing recording it", ttls[0], phase3ProvisionalLease)
	}
	if ttls[len(ttls)-1] != phase3LeaseTTL {
		t.Fatalf("the settled lease is %v, want the full %v", ttls[len(ttls)-1], phase3LeaseTTL)
	}
}

// THE CENTRAL PROPERTY. Nothing renews, so the kernel ends the access by itself.
func TestLease_ExpiresWhenNobodyRenews(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	mustConverge(t, p, leasePlan(1, nil), time.Now())

	// Everything stops here: no plans, no reconciliation, no database, no daemons. Only the kernel runs.
	g.advance(phase3LeaseTTL)

	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized after the lease elapsed with nobody renewing it")
	}
	// And the tc class is deliberately still there: nothing removed it, because nothing was running. That is
	// the point — the class is not what ended the access, the lease is.
	minor, _ := shape.MinorForIP(mustIP(provIP))
	if !tc.hasForwarding("br-guest", minor) {
		t.Fatal("the tc class disappeared, which would make this test prove nothing about the lease")
	}
}

// The §I timeline, end to end: an ACTIVE guest, then acctd dies, then netd dies, then the entitlement window
// elapses — with neither daemon ever recovering.
func TestLease_ProducerAndApplierBothDie_AccessStillEnds(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	ends := start.Add(45 * time.Second)
	mustConverge(t, p, leasePlan(1, &ends), start)

	// acctd dies: no further plans are ever produced. netd dies: nothing applies anything. The writer is
	// simply never called again — which is what makes this an honest model of both being gone.
	g.advance(46 * time.Second) // past the entitlement window
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("access outlived the entitlement window with both daemons dead")
	}
}

// A lease is clamped by the hard boundary, and renewal re-derives the clamp rather than resetting to full.
func TestLease_NeverExtendsBeyondTheAccessBoundary(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	ends := start.Add(20 * time.Second)
	mustConverge(t, p, leasePlan(1, &ends), start)

	if got := g.leaseOf("br-guest", provIP); got > 20*time.Second {
		t.Fatalf("lease %v reaches past the access boundary at +20s", got)
	}
	// Ten seconds later a healthy pass renews. The renewal must be for the REMAINING ten, not a fresh full
	// lease: otherwise every reconciliation would quietly push the guest's deadline further away, and a
	// long-lived session would never reach it at all.
	at := start.Add(10 * time.Second)
	g.advance(10 * time.Second)
	mustConverge(t, p, leasePlan(2, &ends), at)
	if got := g.leaseOf("br-guest", provIP); got > 10*time.Second {
		t.Fatalf("renewal extended the lease to %v, past the boundary that is now only 10s away", got)
	}
	// and after the boundary, nothing keeps it alive
	g.advance(11 * time.Second)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized past their entitlement window")
	}
}

// An entitlement that has ALREADY expired is never leased, even though the plan still lists the session (the
// producer derives from durable state; the expiry sweep runs on its own schedule).
func TestLease_AlreadyExpiredEntitlementIsNeverAuthorized(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	now := time.Now()
	past := now.Add(-time.Second)
	res, err := p.submit(t.Context(), leasePlan(1, &past), now)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !res.Degraded || res.Shaped != 0 {
		t.Fatalf("an expired session was provisioned: %+v", res)
	}
	if g.count() != 0 {
		t.Fatal("an expired session was authorized")
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// A session already online whose boundary passes is DENIED on the next pass, not merely left to expire.
func TestLease_BoundaryPassingRevokesOnTheNextPass(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	start := time.Now()
	ends := start.Add(30 * time.Second)
	mustConverge(t, p, leasePlan(1, &ends), start)

	after := start.Add(31 * time.Second)
	g.advance(31 * time.Second)
	res, err := p.submit(t.Context(), leasePlan(2, &ends), after)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !res.Degraded {
		t.Fatal("a session past its boundary was reported as a clean convergence")
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized past the boundary")
	}
	assertNoUnaccountedAccess(t, tc, g)
}

// Renewal refreshes ONE element. A renewal that added a second would be a leak that grows with uptime, and a
// stray sweep would then find and remove an element that a live guest depends on.
func TestLease_RenewalDoesNotDuplicateTheElement(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	now := time.Now()
	mustConverge(t, p, leasePlan(1, nil), now)
	for i := int64(2); i <= 6; i++ {
		g.advance(35 * time.Second) // enough to fall below the renewal threshold each time
		now = now.Add(35 * time.Second)
		mustConverge(t, p, leasePlanAt(i, nil, now), now)
	}
	if g.count() != 1 {
		t.Fatalf("%d authorization elements for one session", g.count())
	}
	if got := g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("after renewal the lease is %v, want the full %v", got, phase3LeaseTTL)
	}
}

// A healthy pass over an unchanged session does NOT rewrite the kernel every tick. This is a cost property,
// but it is a correctness one too: an nft transaction per guest per second on a busy property is how a
// renewal mechanism becomes the thing that takes the appliance down.
func TestLease_SteadyStateDoesNotRewriteTheKernelEveryPass(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	now := time.Now()
	mustConverge(t, p, leasePlan(1, nil), now)
	before := len(g.requestedTTLs("br-guest", provIP))

	for i := int64(2); i <= 11; i++ { // ten more passes, one second apart
		g.advance(time.Second)
		now = now.Add(time.Second)
		mustConverge(t, p, leasePlan(i, nil), now)
	}
	if after := len(g.requestedTTLs("br-guest", provIP)); after != before {
		t.Fatalf("ten healthy passes issued %d lease writes; a lease with %v remaining does not need refreshing",
			after-before, phase3LeaseTTL-10*time.Second)
	}
}

// A REBOOT flushes nftables. The first plan afterwards must re-establish exactly the access that is still
// valid — and nothing else.
func TestLease_RebootReestablishesOnlyCurrentlyValidAccess(t *testing.T) {
	tc, _, gens, origins, enf, p := sysSetup()
	start := time.Now()
	live := start.Add(10 * time.Minute)
	expired := start.Add(-time.Minute)
	env := envelopeOn([]string{"br-guest"}, 1,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: "10.0.0.1", Bridge: "br-guest",
			DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: &live},
		shapeplan.Session{SessionID: "over-1", DeviceID: "dev-2", IP: "10.0.0.2", Bridge: "br-guest",
			DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: &expired})
	if _, err := p.submit(t.Context(), env, start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	// REBOOT: the ruleset is regenerated (the Phase-3 set comes back EMPTY) and tc is gone with it. A fresh
	// writer with a new boot id is what netd is after a restart.
	g2 := newFakeGate()
	tc2 := newFakeTC()
	p2 := sysWriter(tc2, g2, gens, origins, enf)
	p2.bootID = "boot-after-reboot"
	if _, err := p2.submit(t.Context(), envelopeOn([]string{"br-guest"}, 2,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: "10.0.0.1", Bridge: "br-guest",
			DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: &live},
		shapeplan.Session{SessionID: "over-1", DeviceID: "dev-2", IP: "10.0.0.2", Bridge: "br-guest",
			DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: &expired}), start.Add(time.Second)); err != nil {
		t.Fatalf("plan refused after reboot: %v", err)
	}
	if !g2.isAuthorized("br-guest", "10.0.0.1") {
		t.Fatal("a still-valid session was not re-authorized after the reboot")
	}
	if g2.isAuthorized("br-guest", "10.0.0.2") {
		t.Fatal("a session whose window had already elapsed was re-authorized after the reboot")
	}
	assertNoUnaccountedAccess(t, tc2, g2)
	_ = tc
}

// A stale plan cannot preserve access. netd reports ACTIVE_STALE, and the kernel enforces what that means.
func TestLease_StalePlanCannotPreserveAccessIndefinitely(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	mustConverge(t, p, leasePlan(1, nil), start)

	// The producer goes quiet. The plan expires; nothing replaces it.
	late := start.Add(2 * time.Minute)
	if state, _ := p.shapingState(late); state != shapingStale {
		t.Fatalf("health = %s, want %s once the plan has expired without a replacement", state, shapingStale)
	}
	g.advance(2 * time.Minute)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the appliance reports a stale plan while the kernel still forwards the guest")
	}
}

// ---- B. an unconfirmed durable activation never becomes permanent access -----

// The commit LANDED and the acknowledgement was lost. The re-read proves it, exactly one Session is active,
// and the guest gets the full lease. This is the case that must NOT be treated as failure.
func TestActivation_CommitLandsButAckIsLost_ConvergesWithoutASecondGrant(t *testing.T) {
	_, g, _, _, enf, p := sysSetup()
	enf.commitThenLoseAck["live-1"] = true
	now := time.Now()
	res, err := p.submit(t.Context(), leasePlan(1, nil), now)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if res.Degraded {
		t.Fatalf("a promotion that actually committed was reported as a failure: %+v", res.Problems)
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatalf("session state = %s, want active", enf.sessionState("live-1"))
	}
	if enf.activateCount() != 1 {
		t.Fatalf("the promotion was attempted %d times; the lost acknowledgement created extra work, not extra grants", enf.activateCount())
	}
	if got := g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("a proven session holds a %v lease, want the full %v", got, phase3LeaseTTL)
	}
}

// The transaction GENUINELY failed and state is unreadable. The guest keeps only the provisional lease.
func TestActivation_UnprovableActivationHoldsOnlyTheProvisionalLease(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	enf.err = errors.New("could not begin transaction")
	enf.confirmErr = errors.New("connection refused")
	now := time.Now()
	res, err := p.submit(t.Context(), leasePlan(1, nil), now)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !res.Degraded {
		t.Fatal("an unprovable activation was reported as a clean convergence")
	}
	if res.Shaped != 0 {
		t.Fatal("a session whose durable state was never proven was counted as shaped")
	}
	for _, ttl := range g.requestedTTLs("br-guest", provIP) {
		if ttl > phase3ProvisionalLease {
			t.Fatalf("an unproven session was given a %v lease; only %v is justified until durable ACTIVE is proven",
				ttl, phase3ProvisionalLease)
		}
	}
	// It is still accountable while it holds that lease — this is a bounded provisional state, not a bypass.
	assertNoUnaccountedAccess(t, tc, g)
}

// AND IT EXPIRES. netd dies while the activation is unproven; nothing else ever runs.
func TestActivation_UnprovenAndAbandoned_ExpiresInTheKernel(t *testing.T) {
	_, g, _, _, enf, p := sysSetup()
	enf.err = errors.New("database down")
	enf.confirmErr = errors.New("database down")
	if _, err := p.submit(t.Context(), leasePlan(1, nil), time.Now()); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the provisional admission did not take, so this proves nothing about its expiry")
	}
	g.advance(phase3ProvisionalLease)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatalf("a guest whose activation was never proven is still online after %v", phase3ProvisionalLease)
	}
}

// AND IF NETD KEEPS RUNNING, the provisional state is still not allowed to become permanent: past the grace
// the session is failed closed — packet authorization denied first, then the class torn down.
func TestActivation_ProvisionalStateIsNotRenewedForever(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	enf.err = errors.New("database down")
	enf.confirmErr = errors.New("database down")
	start := time.Now()
	if _, err := p.submit(t.Context(), leasePlan(1, nil), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	// Passes keep arriving and the database stays down.
	at := start
	for i := int64(2); i <= 8; i++ {
		at = at.Add(10 * time.Second)
		g.advance(10 * time.Second)
		if _, err := p.submit(t.Context(), leasePlanAt(i, nil, at), at); err != nil {
			t.Fatalf("plan refused: %v", err)
		}
	}
	if at.Sub(start) <= phase3ActivationGrace {
		t.Fatal("the test did not run past the activation grace")
	}
	if g.isAuthorized("br-guest", provIP) {
		t.Fatalf("a session unproven for %v is still authorized; the provisional state became a permanent one", at.Sub(start))
	}
	if tc.countForwarding() != 0 {
		t.Fatal("packet authorization was denied but the class was left classifying")
	}
	// deny BEFORE teardown, as everywhere else
	assertDenyBeforeTeardown(t, g)
}

// A guest whose activation could not be proven must not be told they are connected. The Session stays
// PENDING_ENFORCEMENT, which is exactly what scd's grant waits on.
func TestActivation_UnprovenSessionIsNeverReportedActive(t *testing.T) {
	_, _, _, _, enf, p := sysSetup()
	enf.err = errors.New("database down")
	enf.confirmErr = errors.New("database down")
	if _, err := p.submit(t.Context(), leasePlan(1, nil), time.Now()); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if got := enf.sessionState("live-1"); got == "active" {
		t.Fatalf("session state = %s: a promotion that was never proven is being reported as connected", got)
	}
}

// Recovery: the database comes back inside the grace, the promotion lands, and the guest is extended to a
// full lease without ever having been disconnected.
func TestActivation_RecoveryInsideTheGraceRestoresTheFullLease(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	enf.err = errors.New("database down")
	enf.confirmErr = errors.New("database down")
	start := time.Now()
	if _, err := p.submit(t.Context(), leasePlan(1, nil), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if !g.isAuthorized("br-guest", provIP) {
		t.Fatal("the provisional admission did not take")
	}

	enf.err, enf.confirmErr = nil, nil
	at := start.Add(10 * time.Second)
	g.advance(10 * time.Second)
	res := mustConverge(t, p, leasePlan(2, nil), at)
	if res.Shaped != 1 {
		t.Fatalf("the recovered session was not counted as shaped: %+v", res)
	}
	if got := g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("after recovery the lease is %v, want the full %v", got, phase3LeaseTTL)
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the recovered session was not promoted")
	}
	assertNoUnaccountedAccess(t, tc, g)
	if g.isAuthorized("br-guest", provIP) && tc.countForwarding() == 0 {
		t.Fatal("the guest was disconnected during recovery")
	}
}

// Repeated ambiguity must converge on exactly ONE active session, never a second grant.
func TestActivation_RepeatedAmbiguityYieldsExactlyOneActiveSession(t *testing.T) {
	_, _, _, _, enf, p := sysSetup()
	enf.commitThenLoseAck["live-1"] = true
	now := time.Now()
	for i := int64(1); i <= 5; i++ {
		if _, err := p.submit(t.Context(), leasePlan(i, nil), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("plan refused: %v", err)
		}
	}
	if enf.sessionState("live-1") != "active" {
		t.Fatal("the session never converged to active")
	}
	// the first pass commits (and loses the ack); every later pass finds it already active via the re-read or
	// the idempotent promotion, and neither creates anything new
	if n := enf.activateCount(); n > 5 {
		t.Fatalf("%d activation attempts across 5 passes", n)
	}
}

// assertDenyBeforeTeardown checks the gate saw a revoke — the fail-closed order is asserted in detail by the
// enforcement suite; here it only has to have happened.
func assertDenyBeforeTeardown(t *testing.T, g *fakeGate) {
	t.Helper()
	for _, c := range g.callLog() {
		if len(c) >= 6 && c[:6] == "revoke" {
			return
		}
	}
	t.Fatal("the class was torn down without packet authorization ever being revoked")
}

func mustIP(s string) net.IP { return net.ParseIP(s) }

// ---- A. the hard boundary, at every timestamp precision ---------------------
//
// nft's element timeout granularity is whole seconds, so every boundary that does not land exactly on a
// second has to be resolved somehow, and only one direction is safe. Rounding up would let a lease clamped to
// a business deadline expire PAST that deadline — by up to 999ms, on almost every real boundary, silently.
//
// These pin the resolution at each precision the instruction named.

func TestLease_BoundaryIsNeverExceededAtAnyPrecision(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		remaining time.Duration
		wantLease time.Duration
		wantOK    bool
	}{
		{"exactly the full lease", phase3LeaseTTL, phase3LeaseTTL, true},
		{"1.9s", 1900 * time.Millisecond, time.Second, true},
		{"1.1s", 1100 * time.Millisecond, time.Second, true},
		{"exactly 1s", time.Second, time.Second, true},
		{"999ms", 999 * time.Millisecond, 0, false},
		{"100ms", 100 * time.Millisecond, 0, false},
		{"already expired", -time.Millisecond, 0, false},
		{"long past expiry", -time.Hour, 0, false},
		{"just under the full lease", phase3LeaseTTL - time.Millisecond, phase3LeaseTTL - time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ends := now.Add(tc.remaining)
			got, ok, why := leaseFor(shapeplan.Session{AccessEndsAt: &ends}, now)
			if ok != tc.wantOK {
				t.Fatalf("leasable = %v (%s), want %v", ok, why, tc.wantOK)
			}
			if !ok {
				if why == "" {
					t.Fatal("a refusal carried no reason")
				}
				return
			}
			if got != tc.wantLease {
				t.Fatalf("lease = %v, want %v", got, tc.wantLease)
			}
			// THE INVARIANT, stated directly: the kernel expiry may never be later than the boundary.
			if now.Add(got).After(ends) {
				t.Fatalf("a %v lease against %v remaining expires %v PAST the hard boundary",
					got, tc.remaining, now.Add(got).Sub(ends))
			}
		})
	}
}

// A session with NO wall-clock boundary keeps the ordinary 90-second lease. None of the clamping applies to
// it, and this is here so a future change to the clamp cannot quietly shorten every ordinary guest's lease.
func TestLease_NoBoundaryKeepsTheOrdinaryLease(t *testing.T) {
	got, ok, _ := leaseFor(shapeplan.Session{}, time.Now())
	if !ok || got != phase3LeaseTTL {
		t.Fatalf("lease = %v (ok=%v), want the ordinary %v", got, ok, phase3LeaseTTL)
	}
}

// REPEATED RENEWAL APPROACHING THE BOUNDARY. Every pass re-derives the clamp, so the installed expiry walks
// down to the boundary and stops there — it never steps over it, and the guest is denied for the final
// sub-second remainder rather than being given one more whole second.
func TestLease_RepeatedRenewalWalksDownToTheBoundaryAndStops(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	// A deliberately "awkward" boundary: not a whole number of seconds from any pass.
	ends := start.Add(6300 * time.Millisecond)
	mustConverge(t, p, leasePlanAt(1, &ends, start), start)

	for i := int64(2); i <= 8; i++ {
		at := start.Add(time.Duration(i-1) * time.Second)
		g.advance(time.Second)
		res, err := p.submit(t.Context(), leasePlanAt(i, &ends, at), at)
		if err != nil {
			t.Fatalf("plan refused: %v", err)
		}
		if lease := g.leaseOf("br-guest", provIP); lease > 0 {
			expiry := at.Add(lease)
			if expiry.After(ends) {
				t.Fatalf("pass %d installed a lease expiring %v past the boundary", i, expiry.Sub(ends))
			}
		}
		// past the boundary the session must be refused outright
		if !at.Before(ends) && !res.Degraded {
			t.Fatalf("pass %d past the boundary reported a clean convergence", i)
		}
	}
	g.advance(time.Second)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized past their boundary")
	}
}

// RESTART IMMEDIATELY BEFORE THE BOUNDARY. A fresh netd must not treat a nearly-expired entitlement as a new
// full-length grant: it re-derives the clamp from the same durable boundary the plan carries.
func TestLease_RestartImmediatelyBeforeTheBoundaryDoesNotExtendIt(t *testing.T) {
	_, _, gens, origins, enf, p := sysSetup()
	start := time.Now()
	ends := start.Add(30 * time.Second)
	mustConverge(t, p, leasePlanAt(1, &ends, start), start)

	// RESTART, 1.4 seconds before the boundary. The kernel kept its state; netd lost its memory of it.
	at := ends.Add(-1400 * time.Millisecond)
	tc2, g2 := newFakeTC(), newFakeGate()
	p2 := sysWriter(tc2, g2, gens, origins, enf)
	p2.bootID = "boot-restart"
	if _, err := p2.submit(t.Context(), leasePlanAt(2, &ends, at), at); err != nil {
		t.Fatalf("plan refused after restart: %v", err)
	}
	if lease := g2.leaseOf("br-guest", provIP); lease > 0 && at.Add(lease).After(ends) {
		t.Fatalf("a restart %v before the boundary installed a lease expiring %v past it",
			ends.Sub(at), at.Add(lease).Sub(ends))
	}
	if lease := g2.leaseOf("br-guest", provIP); lease > time.Second {
		t.Fatalf("a restart 1.4s before the boundary issued a %v lease", lease)
	}
}
