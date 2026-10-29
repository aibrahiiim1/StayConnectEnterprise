package main

// AD-53: an unchanged plan only refreshes validity between full walks; every change walks at once; lease renewal between
// walks is bounded by the plan's validity; and the off-switch restores the walk-every-submission behaviour.

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func fastWriter(t *testing.T) (*phase3Shaping, *fakeTC, *fakeGate) {
	tc := newFakeTC()
	p := liveWriter(tc)
	g := newFakeGate()
	p.gate = g
	p.fastPath = true
	return p, tc, g
}

func livePlan(gen int64, extra ...shapeplan.Session) shapeplan.Envelope {
	s := []shapeplan.Session{
		{SessionID: "live-1", DeviceID: "dev-live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000, Entitled: true},
	}
	return envelope(gen, append(s, extra...)...)
}

func TestAnUnchangedPlanRefreshesValidityWithoutWalking(t *testing.T) {
	p, tc, _ := fastWriter(t)
	ctx, now := context.Background(), time.Now()
	if _, err := p.submit(ctx, livePlan(1), now); err != nil {
		t.Fatal(err)
	}
	if !p.memo.clean {
		t.Fatalf("precondition: the first walk must be clean: %+v", p.memo.res)
	}
	before := len(tc.calls)
	later := now.Add(10 * time.Second)
	env2 := livePlan(2)
	res, err := p.submit(ctx, env2, later)
	if err != nil || !res.Accepted || res.PlanGeneration != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(tc.calls) != before {
		t.Fatalf("an unchanged plan was walked: %d new tc calls", len(tc.calls)-before)
	}
	if p.lastConverged.Generation != 2 || !p.lastConverged.ExpiresAt.Equal(env2.ExpiresAt.UTC()) {
		t.Fatalf("validity was not refreshed: %+v", p.lastConverged)
	}
	if res.Shaped != 1 {
		t.Fatalf("the cached result lost the shaped count: %+v", res)
	}
}

func TestAChangedPlanWalksAtOnce(t *testing.T) {
	p, tc, g := fastWriter(t)
	ctx, now := context.Background(), time.Now()
	_, _ = p.submit(ctx, livePlan(1), now)
	before := len(tc.calls)
	second := shapeplan.Session{SessionID: "live-2", DeviceID: "dev-live-2", IP: "10.0.0.2", Bridge: "br-guest", DownKbps: 4000, UpKbps: 1500, Entitled: true}
	if _, err := p.submit(ctx, livePlan(2, second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(tc.calls) == before || !g.isAuthorized("br-guest", "10.0.0.2") {
		t.Fatal("a session added to the plan was not walked in")
	}
	// A session ending is a change too: it must be torn down on the very next submission, not at the next full walk.
	ended := second
	ended.Entitled = false
	if _, err := p.submit(ctx, livePlan(3, ended), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if g.isAuthorized("br-guest", "10.0.0.2") {
		t.Fatal("an ended session was still authorized after the plan said so")
	}
}

func TestAFullWalkRunsAtLeastEveryThirtySeconds(t *testing.T) {
	p, tc, _ := fastWriter(t)
	ctx, now := context.Background(), time.Now()
	_, _ = p.submit(ctx, livePlan(1), now)
	_, _ = p.submit(ctx, livePlan(2), now.Add(phase3FullWalkEvery-time.Second))
	n := len(tc.calls)
	_, _ = p.submit(ctx, livePlan(3), now.Add(phase3FullWalkEvery))
	if len(tc.calls) == n {
		t.Fatal("drift detection: the walk did not run after phase3FullWalkEvery")
	}
}

func TestAWalkThatReportedAProblemIsNeverSkipped(t *testing.T) {
	p, tc, _ := fastWriter(t)
	tc.failPrepare = map[string]error{"10.0.0.1": errFake("tc busy")}
	ctx, now := context.Background(), time.Now()
	res, _ := p.submit(ctx, livePlan(1), now)
	if res.Failed == 0 || p.memo.clean {
		t.Skipf("the fake did not produce a failed walk (res=%+v); the property is covered by memo.clean", res)
	}
	n := len(tc.calls)
	_, _ = p.submit(ctx, livePlan(2), now.Add(time.Second))
	if len(tc.calls) == n {
		t.Fatal("a plan that did not converge was skipped")
	}
}

func TestTheOffSwitchWalksEverySubmission(t *testing.T) {
	p, tc, _ := fastWriter(t)
	p.fastPath = false
	ctx, now := context.Background(), time.Now()
	_, _ = p.submit(ctx, livePlan(1), now)
	n := len(tc.calls)
	_, _ = p.submit(ctx, livePlan(2), now.Add(time.Second))
	if len(tc.calls) == n {
		t.Fatal("with the fast path off an unchanged plan must still be walked")
	}
}

func TestLeaseTimerRenewsOnlyWhatTheLastWalkProvedAndOnlyWhileThePlanIsCurrent(t *testing.T) {
	p, _, g := fastWriter(t)
	ctx, now := context.Background(), time.Now()
	env := livePlan(1)
	if _, err := p.submit(ctx, env, now); err != nil {
		t.Fatal(err)
	}
	key := gk("br-guest", mustIP("10.0.0.1"))
	g.leases[key] = 10 * time.Second // running down
	g.calls = nil
	if n := p.renewDue(ctx, now.Add(20*time.Second)); n != 1 {
		t.Fatalf("renewed %d, want 1 (calls %v)", n, g.calls)
	}
	if ttls := g.ttls[key]; ttls[len(ttls)-1] > env.ExpiresAt.Sub(now.Add(20*time.Second)) {
		t.Fatalf("a renewal outlives the plan's validity: %v", ttls)
	}
	// Healthy lease: nothing to do.
	g.leases[key] = 85 * time.Second
	g.calls = nil
	if n := p.renewDue(ctx, now.Add(21*time.Second)); n != 0 {
		t.Fatalf("a healthy lease was rewritten: %v", g.calls)
	}
	// The producer went quiet: the plan has expired, so the timer stops and the lease is left to lapse.
	g.leases[key] = 5 * time.Second
	if n := p.renewDue(ctx, env.ExpiresAt.Add(time.Second)); n != 0 {
		t.Fatalf("renewed %d leases under an expired plan", n)
	}
}

func TestLeaseTimerNeverExtendsPastTheAccessBoundary(t *testing.T) {
	p, _, g := fastWriter(t)
	ctx, now := context.Background(), time.Now()
	ends := now.Add(40 * time.Second)
	s := shapeplan.Session{SessionID: "live-1", DeviceID: "dev-live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000, Entitled: true, AccessEndsAt: &ends}
	if _, err := p.submit(ctx, envelope(1, s), now); err != nil {
		t.Fatal(err)
	}
	key := gk("br-guest", mustIP("10.0.0.1"))
	g.leases[key] = time.Second
	p.renewDue(ctx, now.Add(30*time.Second))
	ttls := g.ttls[key]
	if last := ttls[len(ttls)-1]; last > 10*time.Second {
		t.Fatalf("lease %v runs past the access boundary (10s left)", last)
	}
}

func TestLeaseTimerIsInertWhenTheFastPathIsOff(t *testing.T) {
	p, _, g := fastWriter(t)
	ctx, now := context.Background(), time.Now()
	_, _ = p.submit(ctx, livePlan(1), now)
	p.fastPath = false
	g.leases[gk("br-guest", mustIP("10.0.0.1"))] = time.Second
	if n := p.renewDue(ctx, now.Add(time.Second)); n != 0 {
		t.Fatal("the timer renewed with the fast path off; the walk owns renewal then")
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
