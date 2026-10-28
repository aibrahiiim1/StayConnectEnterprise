//go:build integration

package main

import (
	"context"
	"testing"
)

// D7-L2: the recovery-event trim keeps the newest 2,000 rows, removes nothing below that, and only runs after an
// event was recorded.
func TestIntegration_RecoveryEventTrimKeepsTheNewest2000(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `DELETE FROM appliance_recovery_events`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(context.Background(), `DELETE FROM appliance_recovery_events`) })

	count := func() (n, minID, maxID int64) {
		t.Helper()
		if err := p.QueryRow(ctx, `SELECT count(*), COALESCE(min(id),0), COALESCE(max(id),0) FROM appliance_recovery_events`).
			Scan(&n, &minID, &maxID); err != nil {
			t.Fatal(err)
		}
		return
	}
	insert := func(k int) {
		t.Helper()
		if _, err := p.Exec(ctx, `INSERT INTO appliance_recovery_events (service, event)
		    SELECT 'test', 'e' FROM generate_series(1,$1)`, k); err != nil {
			t.Fatal(err)
		}
	}

	insert(1500)
	if _, err := p.Exec(ctx, trimRecoveryEventsSQL); err != nil {
		t.Fatalf("trim: %v", err)
	}
	if n, _, _ := count(); n != 1500 {
		t.Fatalf("below the cap nothing is removed, have %d", n)
	}
	insert(700)
	if _, err := p.Exec(ctx, trimRecoveryEventsSQL); err != nil {
		t.Fatalf("trim: %v", err)
	}
	n, minID, maxID := count()
	if n != 2000 || maxID-minID != 1999 {
		t.Fatalf("after the trim: %d rows, ids %d..%d; want the newest 2000", n, minID, maxID)
	}

	recoveryEventsSinceTrim.Store(0)
	s := &server{db: p}
	s.recordRecovery(ctx, "test", "e", "", "", 0, "", 0, "system")
	if recoveryEventsSinceTrim.Load() != 1 {
		t.Fatal("a recorded event must schedule a trim")
	}
}
