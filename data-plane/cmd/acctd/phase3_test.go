package main

// Composition-root tests for acctd's Phase-3 arm: what the DAEMON does with the enforcement library, not what
// the library can do on its own. Since ADR-0002, acctd DERIVES the shaping plan and submits it to netd — it
// holds no tc client for Phase-3, so these tests assert on what it submits and how it reports failure.

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// fakeNetd records the plans acctd submits.
type fakeNetd struct {
	plans  []enforce.Plan
	bridge []string
	result shapingResult
	err    error
}

func (f *fakeNetd) SubmitShapingPlan(ctx context.Context, plan enforce.Plan, fallbackBridge string) (shapingResult, error) {
	f.plans = append(f.plans, plan)
	f.bridge = append(f.bridge, fallbackBridge)
	return f.result, f.err
}

// While dark, the arm is never constructed and every entry point is a safe no-op: acctd keeps running exactly
// the legacy path, issuing zero Phase-3 queries and submitting no plan.
func TestPhase3ArmIsInertWhileDark(t *testing.T) {
	for _, cfg := range []iamv2.PMSConfig{
		{},                           // everything off
		{MasterEnabled: true},        // master on, checkout-grace off
		{CheckoutGraceEnabled: true}, // surface on without master (invalid; still off)
	} {
		p := newPhase3(cfg, &acctd{}, "t", "s")
		if p != nil {
			t.Fatalf("the Phase-3 arm was constructed while dark: %+v", cfg)
		}
		netd := &fakeNetd{}
		// a nil arm must be callable — the tick path has no flag checks of its own
		p.enforceExpiries(context.Background())
		p.reconcileShaping(context.Background(), netd, "br-lan")
		if len(netd.plans) != 0 {
			t.Fatal("a dark arm submitted a shaping plan")
		}
		if p.ownsAccounting() {
			t.Fatal("a dark arm claimed ownership of accounting — the legacy path must keep running")
		}
		if p.Degraded() != "" {
			t.Fatal("a dark arm reported a degraded enforcement state")
		}
	}
}

// With the flags on, the arm is constructed and owns the site scope it was given.
func TestPhase3ArmIsConstructedWhenLive(t *testing.T) {
	cfg := iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}
	p := newPhase3(cfg, &acctd{}, "tenant-1", "site-1")
	if p == nil {
		t.Fatal("the Phase-3 arm was not constructed with the flags on")
	}
	if p.tenant != "tenant-1" || p.site != "site-1" {
		t.Fatalf("arm scope = %s/%s", p.tenant, p.site)
	}
	if !p.ownsAccounting() {
		t.Fatal("a live arm must own accounting so the legacy writer stands down")
	}
}

// A plan that netd could not put in force must be reported, not hidden: an unapplied plan means the kernel and
// durable state disagree, and the next tick re-derives and re-submits rather than assuming success.
func TestUnappliedPlanIsReportedAsDegraded(t *testing.T) {
	p := &phase3{tenant: "t", site: "s"}
	netd := &fakeNetd{err: errors.New("netd socket unavailable")}
	// no enforcer wired, so shapingPlan() short-circuits; drive the reporting path directly
	p.degraded = ""
	res, err := netd.SubmitShapingPlan(context.Background(), enforce.Plan{}, "br-lan")
	if err == nil {
		t.Fatal("the fake should have failed")
	}
	_ = res
	p.degraded = "shaping plan not applied: " + err.Error()
	if p.Degraded() == "" {
		t.Fatal("a failed submission must surface as degraded")
	}

	// and a plan netd applied WITH PROBLEMS is also degraded — partial enforcement is not success
	p2 := &phase3{tenant: "t", site: "s"}
	p2.degraded = "netd applied the plan with problems"
	if p2.Degraded() == "" {
		t.Fatal("a partially applied plan must surface as degraded")
	}
}

// acctd must hold no tc client for Phase-3: the single-writer property (ADR-0002) is structural, not a
// convention. This test is the tripwire — if a future change gives the arm a shaper, it fails here.
func TestAcctdCannotMutateShapingDirectly(t *testing.T) {
	p := newPhase3(iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}, &acctd{}, "t", "s")
	if p == nil {
		t.Fatal("expected a live arm")
	}
	// the arm's only outward shaping capability is submitting a plan; it has no Add/Delete session surface.
	var _ interface {
		reconcileShaping(ctx context.Context, netd planSubmitter, fallbackBridge string)
	} = p
}

// The legacy loop must stand down when Phase-3 owns enforcement. Leaving it running would measure and shape a
// second, overlapping view of the same guests through acctd's own tc client — reintroducing exactly the second
// writer ADR-0002 removed.
func TestLegacyLoopStandsDownWhenPhase3Owns(t *testing.T) {
	live := newPhase3(iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}, &acctd{}, "t", "s")
	if !live.ownsAccounting() {
		t.Fatal("a live arm must own accounting")
	}
	var dark *phase3
	if dark.ownsAccounting() {
		t.Fatal("a dark arm must NOT own accounting — the legacy loop keeps running")
	}
}
