//go:build integration

package main

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A session committed by scd announces itself with pg_notify at commit: acctd's listener must turn that into a kick, and a rolled-back one must not.
func TestIntegration_PendingNotificationKicksTheSubmitter(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var kicks atomic.Int32
	go listenPending(ctx, dsn, func() { kicks.Add(1) })
	// The listener kicks once on connect (a session created while it was away is picked up); wait for that, then count only what we send.
	waitFor := func(n int32, d time.Duration) bool {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			if kicks.Load() >= n {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !waitFor(1, 5*time.Second) {
		t.Fatal("the listener never connected")
	}
	base := kicks.Load()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('onegate_session_pending', 'rolled-back')`); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	time.Sleep(300 * time.Millisecond)
	if kicks.Load() != base {
		t.Fatal("a rolled-back sign-in woke the submitter")
	}

	if _, err := pool.Exec(ctx, `SELECT pg_notify('onegate_session_pending', 'committed')`); err != nil {
		t.Fatal(err)
	}
	if !waitFor(base+1, 3*time.Second) {
		t.Fatal("a committed notification did not kick the submitter")
	}
}
