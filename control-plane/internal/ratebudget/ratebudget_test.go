package ratebudget

import (
	"testing"
	"time"
)

func TestBudgetBoundsPerKeyAndTotal(t *testing.T) {
	b := New(2, 3, time.Minute)
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }

	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := b.Allow("203.0.113.1"); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("one key may record 2 per window, recorded %d", allowed)
	}
	if ok, _ := b.Allow("203.0.113.2"); !ok {
		t.Fatal("another key still has room")
	}
	if ok, _ := b.Allow("203.0.113.3"); ok {
		t.Fatal("the overall cap (3) must hold across keys")
	}
	// Next window: the suppressed count of the previous one is reported once.
	clock = clock.Add(time.Minute)
	ok, suppressed := b.Allow("203.0.113.1")
	if !ok || suppressed != 9 {
		t.Fatalf("new window: ok=%v suppressed=%d (want 8 from one key + 1 over the total = 9)", ok, suppressed)
	}
	if _, suppressed = b.Allow("203.0.113.1"); suppressed != 0 {
		t.Fatal("the suppressed count is reported only once")
	}
}
