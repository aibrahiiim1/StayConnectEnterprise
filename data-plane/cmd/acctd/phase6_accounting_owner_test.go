package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func TestFallbackOwnerExistsExactlyWhenThePhase3ArmDoesNot(t *testing.T) {
	live := newPhase3(iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true},
		&acctd{}, "t", "s", planScope{}, nil)
	if live == nil {
		t.Fatal("the fixture did not build a Phase-3 arm")
	}
	if o := newAggregateOwner(live, nil, "t", "s", 60); o != nil {
		t.Fatal("a second sweeper was constructed alongside the Phase-3 arm")
	}
}

func TestNilOwnerSweepIsSafe(t *testing.T) {
	var o *aggregateOwner
	o.sweep(nil)
}

// An unbounded accrual would charge unobserved time after any outage.
func TestFallbackOwnerBoundIsTheSameOneAcctdUses(t *testing.T) {
	if got := aggregateChargeBoundSeconds(1); got < 60 {
		t.Fatalf("the charge bound for a one-second tick is %ds; a sub-minute bound would record ordinary "+
			"slow sweeps as outages", got)
	}
	if got := aggregateChargeBoundSeconds(30); got != 120 {
		t.Fatalf("a thirty-second tick gives a %ds bound, not four intervals", got)
	}
}
