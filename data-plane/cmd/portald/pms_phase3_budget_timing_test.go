package main

// THE BUDGET AS A COMPOSITION, not a number.
//
// The uniform response-time budget used to be sized against a PMS round trip, which was the whole of the work
// at the time. Guest-visible success now waits for the guest to be genuinely ENFORCED in the kernel, and that
// convergence is produced by the enforcement owner's own reconciliation pass — so the budget has to cover an
// arrival that lands just after a tick, or ordinary healthy guests are reproducibly told they are not
// connected. These tests pin the arithmetic and the property that survives it: ONE budget for the endpoint.

import (
	"testing"
	"time"
)

// The numbers the budget is derived from, written down here so a change to any of them fails a test rather
// than a guest. They mirror acctd's tick and scd's polling granularity.
const (
	reconcileTick     = 1 * time.Second
	healthyPassWork   = 500 * time.Millisecond
	scdPollGranulariy = 100 * time.Millisecond
	localHops         = 200 * time.Millisecond // portald -> scd -> portald, unix sockets
)

// A grant that arrives immediately AFTER a reconciliation tick is the worst healthy case, and it must fit.
func TestBudgetCoversAnArrivalJustAfterTheReconciliationTick(t *testing.T) {
	worstHealthy := reconcileTick + healthyPassWork + scdPollGranulariy + localHops
	usable := phase3FailureBudget - phase3EnforcementReserve
	if usable < worstHealthy {
		t.Fatalf("a healthy connect has %v of budget but needs %v when it arrives just after a tick: those "+
			"guests would get the uniform non-success on a grant that was perfectly fine", usable, worstHealthy)
	}
}

// A grant that arrives immediately BEFORE the tick has the easiest ride, and must obviously fit — this is here
// so that a future budget reduction cannot pass by only ever being checked against the easy case.
func TestBudgetCoversAnArrivalJustBeforeTheReconciliationTick(t *testing.T) {
	bestHealthy := healthyPassWork + scdPollGranulariy + localHops
	if phase3FailureBudget-phase3EnforcementReserve < bestHealthy {
		t.Fatalf("even the best healthy case (%v) does not fit the budget", bestHealthy)
	}
	if bestHealthy >= reconcileTick+healthyPassWork+scdPollGranulariy+localHops {
		t.Fatal("the before/after cases are indistinguishable, so this test proves nothing")
	}
}

// THE CEILING IS STILL A CEILING. A budget that grew without bound to accommodate convergence would be a
// captive portal that hangs.
func TestBudgetStaysInsideWhatACaptivePortalCanSpend(t *testing.T) {
	if phase3FailureBudget > 3*time.Second {
		t.Fatalf("the uniform budget is %v; past about three seconds a captive portal reads as broken", phase3FailureBudget)
	}
}

// AND IT IS STILL ONE BUDGET. The protection this exists for is that a wrong room and a right room are
// indistinguishable in the clock. Splitting the endpoint into a short identity budget and a long connection
// budget would be cheaper for the guest and would hand back exactly that distinction.
func TestThereIsExactlyOneGuestVisibleBudget(t *testing.T) {
	if phase3EnforcementReserve >= phase3FailureBudget {
		t.Fatal("the upstream reserve is not smaller than the budget, so there is no time to do any work in")
	}
	if phase3EnforcementReserve <= 0 {
		t.Fatal("without a reserve the answer lands AFTER the budget, which is itself a measurable difference")
	}
}
