package main

// TIMING COMPOSITION: the guest's budget, the enforcement wait, and the reconciliation tick.
//
// These three used not to compose. portald ran every Phase-3 request under a 1200ms uniform response-time
// budget and passed it down as a context deadline; scd then waited "up to 8 seconds" for the kernel to be
// confirmed. The 8 seconds were unreachable — the context was already cancelled — so the effective wait was
// whatever portald had left, and the enforcement owner converges on a one-second cadence. A guest who tapped
// Connect just after a tick was reproducibly told they were not connected, on a perfectly healthy grant.
//
// The wait is now DERIVED from the caller's deadline, and the budget is sized from the convergence arithmetic
// rather than from a PMS round trip. These tests pin both halves of that.

import (
	"context"
	"testing"
	"time"
)

// The wait runs on the CALLER's clock, ending slightly before it so scd can still answer.
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

// With no caller deadline (an operator call, a test) the wait is bounded by its own maximum rather than
// running forever.
func TestEnforcementWaitIsBoundedWithoutACallerDeadline(t *testing.T) {
	now := time.Now()
	got := enforcementDeadlineFor(context.Background(), now)
	if got != now.Add(enforcementDeadlineMax) {
		t.Fatalf("wait deadline = %v, want the %v maximum", got.Sub(now), enforcementDeadlineMax)
	}
}

// A caller with MORE budget than the maximum does not get more than the maximum: nothing may pin a request
// indefinitely because the thing above it was generous.
func TestEnforcementWaitNeverExceedsItsOwnMaximum(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Hour))
	defer cancel()
	if got := enforcementDeadlineFor(ctx, now); got != now.Add(enforcementDeadlineMax) {
		t.Fatalf("wait deadline = %v, want the %v maximum", got.Sub(now), enforcementDeadlineMax)
	}
}

// THE ARITHMETIC THAT MATTERS. The wait a normal Portal request gets must cover the worst HEALTHY convergence:
// a grant that commits immediately after a reconciliation tick waits out the rest of that interval, the pass's
// own work, and the polling granularity.
//
// These are the numbers, written down where a change to any of them breaks a test rather than a guest:
//
//	reconciliation interval   1s   (acctd's tick)
//	pass work, healthy        <=500ms
//	polling granularity       enforcementPoll
//
// The portald budget itself is asserted on the portald side; here the property is that scd's derived wait is
// large enough to see the convergence when it is handed that budget.
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

// The poll must be short relative to the producer's cadence, or most of the budget is spent not noticing a
// promotion that already happened.
func TestThePollIsShortRelativeToTheReconciliationCadence(t *testing.T) {
	if enforcementPoll > 250*time.Millisecond {
		t.Fatalf("the enforcement poll is %v against a 1s reconciliation cadence; a quarter of the budget "+
			"would be spent waiting to look", enforcementPoll)
	}
}
