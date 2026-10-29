//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AD-52, with a real database: a change to the sign-in settings table clears scd's cache through LISTEN/NOTIFY without
// waiting for the TTL, and a session becoming active wakes its waiter.
func TestIntegration_SettingsChangeAndSessionActiveReachScdByNotification(t *testing.T) {
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
	s := &server{db: pool, settings: newTTLCache(time.Hour), waiters: newSessionWaiters()}
	go s.runNotifications(ctx, dsn)
	deadline := time.Now().Add(10 * time.Second)
	for !s.waiters.up.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the notification connection never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	loads := 0
	load := func() (any, error) { loads++; return loads, nil }
	_, _ = s.settings.get("signin-capacity", load)
	_, _ = s.settings.get("signin-capacity", load)
	if loads != 1 {
		t.Fatalf("loads = %d before the change", loads)
	}
	// Any change to the settings table notifies (the trigger is statement-level).
	var tenant, site = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"
	if _, err := pool.Exec(ctx, `SELECT iam_v2.signin_capacity_settings_set($1::uuid,$2::uuid,'{"otp_max_attempts":6}'::jsonb,'test','notify test')`, tenant, site); err != nil {
		t.Skipf("the settings function is not available here: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		v, _ := s.settings.get("signin-capacity", load)
		if v.(int) > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cache was not cleared by the notification")
		}
		time.Sleep(20 * time.Millisecond)
	}

	ch, release := s.waiters.wait("11111111-1111-1111-1111-111111111111")
	defer release()
	if _, err := pool.Exec(ctx, `SELECT pg_notify('onegate_session_active', '11111111-1111-1111-1111-111111111111')`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the session waiter was not woken by the notification")
	}
}
