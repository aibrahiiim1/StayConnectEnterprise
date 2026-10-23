package main

// Fake-kernel tests: a guest cannot stay online because the daemons that should remove them died, and an
// unconfirmed activation never becomes permanent. Real nft timeout behaviour is proven in internal/kerneltest.

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func leasePlan(gen int64, endsAt *time.Time) shapeplan.Envelope {
	return envelopeOn([]string{"br-guest"}, gen,
		shapeplan.Session{SessionID: "live-1", DeviceID: "dev-1", IP: provIP, Bridge: "br-guest",
			DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: endsAt})
}

// leasePlanAt stamps the plan at simulated time; a wall-clock envelope would be refused as expired.
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
	if got := g.leaseOf("br-guest", provIP); got != phase3LeaseTTL {
		t.Fatalf("settled lease = %v, want %v", got, phase3LeaseTTL)
	}
}

// Admission is on the short provisional lease first, so a crash before the promotion is harmless.
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

// Nothing renews, so the kernel ends the access by itself.
func TestLease_ExpiresWhenNobodyRenews(t *testing.T) {
	tc, g, _, _, _, p := sysSetup()
	mustConverge(t, p, leasePlan(1, nil), time.Now())

	g.advance(phase3LeaseTTL)

	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized after the lease elapsed with nobody renewing it")
	}
	// The tc class must still be there, so the lease is what ended the access.
	minor, _ := shape.MinorForIP(mustIP(provIP))
	if !tc.hasForwarding("br-guest", minor) {
		t.Fatal("the tc class disappeared, which would make this test prove nothing about the lease")
	}
}

// Both acctd and netd die and never recover.
func TestLease_ProducerAndApplierBothDie_AccessStillEnds(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	ends := start.Add(45 * time.Second)
	mustConverge(t, p, leasePlan(1, &ends), start)

	g.advance(46 * time.Second) // past the entitlement window; the writer is never called again
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("access outlived the entitlement window with both daemons dead")
	}
}

// Renewal re-derives the clamp rather than resetting to a full lease.
func TestLease_NeverExtendsBeyondTheAccessBoundary(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	ends := start.Add(20 * time.Second)
	mustConverge(t, p, leasePlan(1, &ends), start)

	if got := g.leaseOf("br-guest", provIP); got > 20*time.Second {
		t.Fatalf("lease %v reaches past the access boundary at +20s", got)
	}
	// The renewal must be for the remaining ten seconds, not a fresh full lease.
	at := start.Add(10 * time.Second)
	g.advance(10 * time.Second)
	mustConverge(t, p, leasePlan(2, &ends), at)
	if got := g.leaseOf("br-guest", provIP); got > 10*time.Second {
		t.Fatalf("renewal extended the lease to %v, past the boundary that is now only 10s away", got)
	}
	g.advance(11 * time.Second)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized past their entitlement window")
	}
}

// The plan may still list a session whose entitlement already expired; it is never leased.
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

// Denied on the next pass, not merely left to expire.
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

// Renewal refreshes one element; a second would leak with uptime.
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

// A healthy pass over an unchanged session must not rewrite the kernel every tick.
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

// A reboot flushes nftables; the first plan afterwards re-establishes only still-valid access.
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

	// Reboot: the Phase-3 set comes back empty and tc is gone.
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

func TestLease_StalePlanCannotPreserveAccessIndefinitely(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	mustConverge(t, p, leasePlan(1, nil), start)

	late := start.Add(2 * time.Minute)
	if state, _ := p.shapingState(late); state != shapingStale {
		t.Fatalf("health = %s, want %s once the plan has expired without a replacement", state, shapingStale)
	}
	g.advance(2 * time.Minute)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the appliance reports a stale plan while the kernel still forwards the guest")
	}
}

// The commit landed but the ack was lost: the re-read proves it and this must not count as failure.
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

// The transaction failed and state is unreadable: only the provisional lease.
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
	assertNoUnaccountedAccess(t, tc, g)
}

// netd dies while the activation is unproven; the lease expires in the kernel.
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

// If netd keeps running, past the grace the session fails closed: deny first, then tear down.
func TestActivation_ProvisionalStateIsNotRenewedForever(t *testing.T) {
	tc, g, _, _, enf, p := sysSetup()
	enf.err = errors.New("database down")
	enf.confirmErr = errors.New("database down")
	start := time.Now()
	if _, err := p.submit(t.Context(), leasePlan(1, nil), start); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	// The monotonic security clock, not the wall clock, measures the activation bound.
	at := start
	for i := int64(2); i <= 8; i++ {
		at = at.Add(10 * time.Second)
		g.advance(10 * time.Second)
		advanceSecurityClock(p, 10*time.Second)
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
	assertDenyBeforeTeardown(t, g)
}

// The Session stays PENDING_ENFORCEMENT, which scd's grant waits on.
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

// Recovery inside the grace extends to a full lease without a disconnect.
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
	if n := enf.activateCount(); n > 5 {
		t.Fatalf("%d activation attempts across 5 passes", n)
	}
}

// The detailed fail-closed order is asserted by the enforcement suite; here a revoke only has to have happened.
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

// The activation bound ignores the wall clock, so tests that let wall time pass must also move this.
func advanceSecurityClock(p *phase3Shaping, d time.Duration) {
	if c, ok := p.secClock.(*fixedSecurityClock); ok {
		c.ms += d.Milliseconds()
	}
}

// nft timeouts are whole seconds; only rounding down keeps the lease within the boundary.
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
			if now.Add(got).After(ends) {
				t.Fatalf("a %v lease against %v remaining expires %v PAST the hard boundary",
					got, tc.remaining, now.Add(got).Sub(ends))
			}
		})
	}
}

func TestLease_NoBoundaryKeepsTheOrdinaryLease(t *testing.T) {
	got, ok, _ := leaseFor(shapeplan.Session{}, time.Now())
	if !ok || got != phase3LeaseTTL {
		t.Fatalf("lease = %v (ok=%v), want the ordinary %v", got, ok, phase3LeaseTTL)
	}
}

// Every pass re-derives the clamp, so the expiry walks down to the boundary and never steps over it.
func TestLease_RepeatedRenewalWalksDownToTheBoundaryAndStops(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	start := time.Now()
	// Not a whole number of seconds from any pass.
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
		if !at.Before(ends) && !res.Degraded {
			t.Fatalf("pass %d past the boundary reported a clean convergence", i)
		}
	}
	g.advance(time.Second)
	if g.isAuthorized("br-guest", provIP) {
		t.Fatal("the guest is still authorized past their boundary")
	}
}

// A fresh netd must re-derive the clamp, not treat a nearly-expired entitlement as a full grant.
func TestLease_RestartImmediatelyBeforeTheBoundaryDoesNotExtendIt(t *testing.T) {
	_, _, gens, origins, enf, p := sysSetup()
	start := time.Now()
	ends := start.Add(30 * time.Second)
	mustConverge(t, p, leasePlanAt(1, &ends, start), start)

	// Restart 1.4s before the boundary; the kernel kept its state, netd lost its memory.
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
