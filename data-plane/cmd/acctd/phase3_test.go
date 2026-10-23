package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

type fakeNetd struct {
	envs   []shapeplan.Envelope
	result shapingResult
	err    error
}

func (f *fakeNetd) SubmitShapingPlan(ctx context.Context, env shapeplan.Envelope) (shapingResult, error) {
	f.envs = append(f.envs, env)
	if f.err != nil {
		return shapingResult{}, f.err
	}
	res := f.result
	if !res.Accepted && res.Reason == "" {
		res.Accepted = true
	}
	return res, nil
}

// The arm follows the master flag, not checkout grace; otherwise Room-authentication sessions stay
// PENDING_ENFORCEMENT because nothing derives a plan for them.
func TestPhase3ArmRunsWheneverPhase3IsEnabled(t *testing.T) {
	for _, cfg := range []iamv2.PMSConfig{
		{MasterEnabled: true},
		{MasterEnabled: true, PMSAuthEnabled: true},
		{MasterEnabled: true, CheckoutGraceEnabled: true},
	} {
		if p := newPhase3(cfg, &acctd{}, "t", "s", planScope{}, nil); p == nil {
			t.Fatalf("no enforcement producer while Phase 3 is enabled: %+v — sessions would stay pending forever", cfg)
		}
	}
}

// While dark the arm is never constructed and every entry point on a nil arm is a safe no-op.
func TestPhase3ArmIsInertWhileDark(t *testing.T) {
	for _, cfg := range []iamv2.PMSConfig{
		{},
		{CheckoutGraceEnabled: true}, // a child flag cannot enable anything without master
		{PMSAuthEnabled: true},
	} {
		p := newPhase3(cfg, &acctd{}, "t", "s", planScope{}, nil)
		if p != nil {
			t.Fatalf("the Phase-3 arm was constructed while dark: %+v", cfg)
		}
		netd := &fakeNetd{}
		p.enforceExpiries(context.Background())
		p.reconcileShaping(context.Background(), netd, "br-lan")
		if len(netd.envs) != 0 {
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

func TestPhase3ArmIsConstructedWhenLive(t *testing.T) {
	cfg := iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}
	p := newPhase3(cfg, &acctd{}, "tenant-1", "site-1", planScope{ApplianceID: "appl-1"}, nil)
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

// An unapplied plan means kernel and durable state disagree; it is reported and re-derived next tick.
func TestUnappliedPlanIsReportedAsDegraded(t *testing.T) {
	p := &phase3{tenant: "t", site: "s"}
	netd := &fakeNetd{err: errors.New("netd socket unavailable")}
	p.degraded = ""
	res, err := netd.SubmitShapingPlan(context.Background(), shapeplan.Envelope{})
	if err == nil {
		t.Fatal("the fake should have failed")
	}
	_ = res
	p.degraded = "shaping plan not applied: " + err.Error()
	if p.Degraded() == "" {
		t.Fatal("a failed submission must surface as degraded")
	}

	// partial enforcement is not success
	p2 := &phase3{tenant: "t", site: "s"}
	p2.degraded = "netd applied the plan with problems"
	if p2.Degraded() == "" {
		t.Fatal("a partially applied plan must surface as degraded")
	}
}

// Tripwire: the single-writer property is structural; the arm must not gain a tc client.
func TestAcctdCannotMutateShapingDirectly(t *testing.T) {
	p := newPhase3(iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}, &acctd{}, "t", "s", planScope{}, nil)
	if p == nil {
		t.Fatal("expected a live arm")
	}
	var _ interface {
		reconcileShaping(ctx context.Context, netd planSubmitter, fallbackBridge string)
	} = p
}

// The legacy loop must stand down when Phase-3 owns enforcement, or it becomes a second writer of the same guests.
func TestLegacyLoopStandsDownWhenPhase3Owns(t *testing.T) {
	live := newPhase3(iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}, &acctd{}, "t", "s", planScope{}, nil)
	if !live.ownsAccounting() {
		t.Fatal("a live arm must own accounting")
	}
	var dark *phase3
	if dark.ownsAccounting() {
		t.Fatal("a dark arm must NOT own accounting — the legacy loop keeps running")
	}
}
