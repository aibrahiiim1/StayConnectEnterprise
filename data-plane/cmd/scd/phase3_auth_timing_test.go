package main

import (
	"context"
	"testing"
	"time"
)

func TestEnforcementWaitDerivesFromTheCallersDeadline(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(2*time.Second))
	defer cancel()

	got := enforcementDeadlineFor(ctx, now)
	if !got.Before(now.Add(2 * time.Second)) {
		t.Fatalf("the wait ends at or after the caller's deadline (%v); it would be cancelled rather than "+
			"answered, which is the exact bug this replaced", got.Sub(now))
	}
	if got.Sub(now) < time.Second {
		t.Fatalf("the wait is only %v of a 2s budget; that is not enough of it to be useful", got.Sub(now))
	}
	if want := now.Add(2*time.Second - enforcementReserve); !got.Equal(want) {
		t.Fatalf("wait deadline = %v, want the caller's deadline less the reserve (%v)", got.Sub(now), want.Sub(now))
	}
}

func TestEnforcementWaitIsBoundedWithoutACallerDeadline(t *testing.T) {
	now := time.Now()
	got := enforcementDeadlineFor(context.Background(), now)
	if got != now.Add(enforcementDeadlineMax) {
		t.Fatalf("wait deadline = %v, want the %v maximum", got.Sub(now), enforcementDeadlineMax)
	}
}

func TestEnforcementWaitNeverExceedsItsOwnMaximum(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Hour))
	defer cancel()
	if got := enforcementDeadlineFor(ctx, now); got != now.Add(enforcementDeadlineMax) {
		t.Fatalf("wait deadline = %v, want the %v maximum", got.Sub(now), enforcementDeadlineMax)
	}
}

// The derived wait must cover the worst healthy convergence: a 1s reconciliation tick, <=500ms pass work and
// the poll granularity. The portald budget itself is asserted on the portald side.
func TestTheDerivedWaitCoversTheWorstHealthyConvergence(t *testing.T) {
	const (
		portalBudget    = 2500 * time.Millisecond // portald's phase3FailureBudget
		portalReserve   = 200 * time.Millisecond  // portald's phase3EnforcementReserve
		reconcileTick   = 1 * time.Second
		healthyPassWork = 500 * time.Millisecond
	)
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(portalBudget-portalReserve))
	defer cancel()

	worstHealthy := reconcileTick + healthyPassWork + enforcementPoll
	available := enforcementDeadlineFor(ctx, now).Sub(now)
	if available < worstHealthy {
		t.Fatalf("a healthy grant has %v to converge but the worst healthy case needs %v: guests who arrive "+
			"just after a reconciliation tick would be told they are not connected", available, worstHealthy)
	}
}

func TestThePollIsShortRelativeToTheReconciliationCadence(t *testing.T) {
	if enforcementPoll > 250*time.Millisecond {
		t.Fatalf("the enforcement poll is %v against a 1s reconciliation cadence; a quarter of the budget "+
			"would be spent waiting to look", enforcementPoll)
	}
}
