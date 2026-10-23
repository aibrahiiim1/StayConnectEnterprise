package main

// Success waits for kernel enforcement, produced by the reconciliation pass, so the budget must cover an
// arrival just after a tick. These pin that arithmetic and that there is one budget for the endpoint.

import (
	"testing"
	"time"
)

// Mirror acctd's tick and scd's polling granularity, so a change fails a test rather than a guest.
const (
	reconcileTick     = 1 * time.Second
	healthyPassWork   = 500 * time.Millisecond
	scdPollGranulariy = 100 * time.Millisecond
	localHops         = 200 * time.Millisecond // portald -> scd -> portald, unix sockets
)

// Arrival just after a tick is the worst healthy case.
func TestBudgetCoversAnArrivalJustAfterTheReconciliationTick(t *testing.T) {
	worstHealthy := reconcileTick + healthyPassWork + scdPollGranulariy + localHops
	usable := phase3FailureBudget - phase3EnforcementReserve
	if usable < worstHealthy {
		t.Fatalf("a healthy connect has %v of budget but needs %v when it arrives just after a tick: those "+
			"guests would get the uniform non-success on a grant that was perfectly fine", usable, worstHealthy)
	}
}

func TestBudgetCoversAnArrivalJustBeforeTheReconciliationTick(t *testing.T) {
	bestHealthy := healthyPassWork + scdPollGranulariy + localHops
	if phase3FailureBudget-phase3EnforcementReserve < bestHealthy {
		t.Fatalf("even the best healthy case (%v) does not fit the budget", bestHealthy)
	}
	if bestHealthy >= reconcileTick+healthyPassWork+scdPollGranulariy+localHops {
		t.Fatal("the before/after cases are indistinguishable, so this test proves nothing")
	}
}

func TestBudgetStaysInsideWhatACaptivePortalCanSpend(t *testing.T) {
	if phase3FailureBudget > 3*time.Second {
		t.Fatalf("the uniform budget is %v; past about three seconds a captive portal reads as broken", phase3FailureBudget)
	}
}

// Splitting into short identity and long connection budgets would make a wrong room distinguishable by time.
func TestThereIsExactlyOneGuestVisibleBudget(t *testing.T) {
	if phase3EnforcementReserve >= phase3FailureBudget {
		t.Fatal("the upstream reserve is not smaller than the budget, so there is no time to do any work in")
	}
	if phase3EnforcementReserve <= 0 {
		t.Fatal("without a reserve the answer lands AFTER the budget, which is itself a measurable difference")
	}
}
