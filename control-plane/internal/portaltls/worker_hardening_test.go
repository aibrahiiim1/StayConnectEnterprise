package portaltls

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// Audit Phase 1 M13: with the ACME slot held, a request path gives up at its deadline instead of hanging.
func TestACMESlotHonoursTheCallerDeadline(t *testing.T) {
	s := New(nil, Options{})
	if err := s.acquireACME(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.acquireACME(ctx); !errors.Is(err, ErrACMEBusy) {
		t.Fatalf("a held slot must answer busy at the deadline, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("waiting for the slot must end at the caller's deadline")
	}
	s.releaseACME()
	if err := s.acquireACME(context.Background()); err != nil {
		t.Fatalf("a released slot must be free: %v", err)
	}
}

// Audit Phase 1 L17: the CA's renewal window is used only before expiry, and never later than 7 days before it.
func TestClampARIWindow(t *testing.T) {
	notAfter := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if _, _, ok := clampARIWindow(notAfter.Add(-48*time.Hour), notAfter.Add(time.Hour), notAfter); ok {
		t.Fatal("a window ending after expiry must be ignored")
	}
	s, e, ok := clampARIWindow(notAfter.Add(-72*time.Hour), notAfter.Add(-24*time.Hour), notAfter)
	if !ok || !s.Equal(notAfter.Add(-ariMinLead)) || !e.Equal(s) {
		t.Fatalf("a late window must be pulled to 7 days before expiry: %v %v %v", s, e, ok)
	}
	early, earlyEnd := notAfter.Add(-30*24*time.Hour), notAfter.Add(-20*24*time.Hour)
	if s, e, ok := clampARIWindow(early, earlyEnd, notAfter); !ok || !s.Equal(early) || !e.Equal(earlyEnd) {
		t.Fatalf("an early window is kept: %v %v", s, e)
	}
}

// Audit Phase 1 M12: attempts count claims, so an order whose passes never finish is failed at maxAttempts
// instead of being reclaimed for ever.
func TestInterruptedOrderIsFailedAtMaxAttempts(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var app, order string
	if err := pool.QueryRow(ctx, `INSERT INTO appliances (serial, lifecycle_state, public_key, activated_at)
        VALUES ('SC-PTLS-M12','assigned','k', now()) RETURNING id::text`).Scan(&app); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO portal_certificate_orders (appliance_id, hostname, kind, csr_der, status, attempts, next_attempt_at)
        VALUES ($1,'abcdefgh.gw.onegate.getsemantics.com','ONEGATE','\x00'::bytea,'PROCESSING',$2, now() - interval '1 minute') RETURNING id::text`,
		app, maxAttempts).Scan(&order); err != nil {
		t.Fatal(err)
	}
	svc := New(pool, Options{SecretsDir: t.TempDir()})
	worked, err := svc.processNextOrder(ctx, &Settings{}, nil)
	if err != nil || !worked {
		t.Fatalf("processNextOrder: %v %v", worked, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, order).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" {
		t.Fatalf("an order claimed %d times without finishing must be FAILED, got %s", maxAttempts, status)
	}
}
