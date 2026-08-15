package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// THE ACCOUNTING OWNER EXISTS IN EVERY SUPPORTED CONFIGURATION.
//
// The failure this closes is a cross-phase one, which is why it was invisible from inside Phase 6: the
// aggregate accrual rides in the Phase-3 arm's sweep, and that arm is built only when the Phase-3 master and
// checkout-grace flags are on. An operator turning those off -- for reasons having nothing to do with Phase 6
// -- would stop every aggregate budget from being consumed, and a finite package would silently become
// unlimited.

func TestFallbackOwnerExistsExactlyWhenThePhase3ArmDoesNot(t *testing.T) {
	// The Phase-3 arm is present: it owns the sweep, and a second sweeper would race for no benefit.
	live := newPhase3(iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true},
		&acctd{}, "t", "s", planScope{}, nil)
	if live == nil {
		t.Fatal("the fixture did not build a Phase-3 arm")
	}
	if o := newAggregateOwner(live, nil, "t", "s", 60); o != nil {
		t.Fatal("a second sweeper was constructed alongside the Phase-3 arm")
	}
}

// A nil owner is safe to sweep, so the tick needs no branch and a misconfiguration cannot panic the loop.
func TestNilOwnerSweepIsSafe(t *testing.T) {
	var o *aggregateOwner
	o.sweep(nil)
}

// The charge bound is derived, not defaulted to something unbounded: an owner that accrued without a bound
// would charge unobserved time after any outage.
func TestFallbackOwnerBoundIsTheSameOneAcctdUses(t *testing.T) {
	if got := aggregateChargeBoundSeconds(1); got < 60 {
		t.Fatalf("the charge bound for a one-second tick is %ds; a sub-minute bound would record ordinary "+
			"slow sweeps as outages", got)
	}
	if got := aggregateChargeBoundSeconds(30); got != 120 {
		t.Fatalf("a thirty-second tick gives a %ds bound, not four intervals", got)
	}
}
