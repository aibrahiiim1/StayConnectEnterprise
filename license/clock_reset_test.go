package license

import (
	"testing"
	"time"
)

// AD-17: one boot with a far-future clock raises the high-water mark; an audited reset to authenticated Central time
// recovers it, and the rollback protection stays in force afterwards.
func TestResetHighWaterRecoversAFarFutureBoot(t *testing.T) {
	s := newSigner(t)
	store := NewStore(t.TempDir(), NewVerifier(s.PublicKey()))
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	d := testDoc(now.AddDate(0, -1, 0))
	d.ValidUntil = now.AddDate(1, 0, 0)
	d.GracePeriodDays = 30
	env, err := s.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := env.Encode()
	if _, err := store.Install(raw, now); err != nil {
		t.Fatal(err)
	}
	if ev, _ := store.Evaluate(now); ev.State != StateActive {
		t.Fatalf("baseline: %s", ev.State)
	}

	// The bad boot: the clock reads 2031, then is corrected.
	_, _ = store.Evaluate(now.AddDate(5, 0, 0))
	ev, _ := store.Evaluate(now.Add(time.Hour))
	if ev.State == StateActive || !ev.ClockRollback {
		t.Fatalf("the premise: after the far-future boot the licence reads %s (rollback=%v)", ev.State, ev.ClockRollback)
	}

	// A reset is refused while the local clock disagrees with Central's authenticated time.
	if _, err := store.ResetHighWater(now, now.AddDate(0, 0, 3), 10*time.Minute); err == nil {
		t.Fatal("a reset with a local clock days away from Central's must be refused")
	}
	if _, err := store.ResetHighWater(time.Time{}, now, 10*time.Minute); err == nil {
		t.Fatal("a reset needs an authenticated Central time")
	}
	old, err := store.ResetHighWater(now.Add(time.Hour), now.Add(time.Hour), 10*time.Minute)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if old.Before(now.AddDate(4, 0, 0)) {
		t.Fatalf("the reset must report the mark it replaced, got %s", old)
	}
	if ev, _ := store.Evaluate(now.Add(2 * time.Hour)); ev.State != StateActive || ev.ClockRollback {
		t.Fatalf("after the reset the licence is Active again, got %s (rollback=%v)", ev.State, ev.ClockRollback)
	}

	// Rollback protection is kept: time still cannot be moved back past the tolerance, nor an expired licence revived.
	_, _ = store.Evaluate(d.ValidUntil.AddDate(0, 0, 60))
	if ev, _ := store.Evaluate(now.Add(3 * time.Hour)); ev.State == StateActive {
		t.Fatal("after a reset the high-water mark still defeats a clock rolled back from the future")
	}
}
