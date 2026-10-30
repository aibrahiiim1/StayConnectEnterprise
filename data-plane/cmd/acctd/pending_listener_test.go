package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A wake-up must bring the next submission forward, but never faster than minGap: a plan lists every live session, so it is not derived
// ten times a second because ten guests signed in.
func TestWakeUpsBringTheSubmissionForwardButNotFasterThanTheGap(t *testing.T) {
	var runs atomic.Int32
	times := make(chan time.Time, 16)
	s := newSubmitter(func() { runs.Add(1); times <- time.Now() })
	s.minGap = 150 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.loop(ctx, func() time.Duration { return time.Hour }) // the period never fires in this test: only wake-ups do

	s.kick()
	first := <-times
	// A burst of wake-ups right after the first submission: one more run, no sooner than the gap.
	for i := 0; i < 20; i++ {
		s.kick()
	}
	second := <-times
	if d := second.Sub(first); d < 140*time.Millisecond {
		t.Fatalf("second submission %v after the first, want at least the 150 ms gap", d)
	}
	select {
	case <-times:
		t.Fatal("twenty wake-ups while one was running produced more than one extra submission")
	case <-time.After(400 * time.Millisecond):
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("%d submissions, want 2", got)
	}
}

// The period still fires with no wake-up at all (the fallback when the notification connection is down), and a gap of zero keeps the earlier
// behaviour of every other kick (an expiry is delivered at once).
func TestThePeriodIsStillTheFallbackAndAZeroGapIsImmediate(t *testing.T) {
	var runs atomic.Int32
	s := newSubmitter(func() { runs.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.loop(ctx, func() time.Duration { return 60 * time.Millisecond })
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runs.Load() < 3 {
		t.Fatalf("the periodic submission did not run: %d", runs.Load())
	}
	before := runs.Load()
	s.kick()
	deadline = time.Now().Add(50 * time.Millisecond)
	for runs.Load() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() == before {
		t.Fatal("a kick with no gap was not delivered at once")
	}
}

func TestAcctdPoolIsSizedExplicitlyAndADsnValueWins(t *testing.T) {
	cfg, err := acctdPoolConfig("postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != acctdDefaultPoolMaxConns {
		t.Fatalf("MaxConns %d, want %d", cfg.MaxConns, acctdDefaultPoolMaxConns)
	}
	cfg, err = acctdPoolConfig("postgres://u:p@127.0.0.1:5432/db?sslmode=disable&pool_max_conns=3")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 3 {
		t.Fatalf("MaxConns %d, want the DSN's 3", cfg.MaxConns)
	}
	// Six plus the notification connection stay inside svc_acctd's connection limit of 10.
	if acctdDefaultPoolMaxConns+1 > 10 {
		t.Fatal("the pool and the listener exceed the role's connection limit")
	}
}
