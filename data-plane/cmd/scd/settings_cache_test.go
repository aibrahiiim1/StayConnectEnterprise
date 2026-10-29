package main

import (
	"errors"
	"testing"
	"time"
)

// AD-52: a value is kept for the TTL, an invalidation drops it at once, an error is never kept, and a value loaded
// across an invalidation is not stored.
func TestSettingsCacheKeepsInvalidatesAndNeverKeepsAnError(t *testing.T) {
	c := newTTLCache(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }
	loads := 0
	load := func() (any, error) { loads++; return loads, nil }

	for i := 0; i < 3; i++ {
		if v, _ := c.get("k", load); v != 1 {
			t.Fatalf("read %d returned %v, want the first load", i, v)
		}
	}
	if loads != 1 {
		t.Fatalf("loaded %d times for three reads", loads)
	}
	c.invalidate()
	if v, _ := c.get("k", load); v != 2 {
		t.Fatalf("after an invalidation the value was %v, want a fresh load", v)
	}
	now = now.Add(2 * time.Minute)
	if v, _ := c.get("k", load); v != 3 {
		t.Fatalf("after the TTL the value was %v, want a fresh load", v)
	}

	failing := 0
	for i := 0; i < 2; i++ {
		if _, err := c.get("bad", func() (any, error) { failing++; return nil, errors.New("db down") }); err == nil {
			t.Fatal("an error was swallowed")
		}
	}
	if failing != 2 {
		t.Fatalf("an error was cached: %d loads", failing)
	}

	// A load that straddles an invalidation must not be stored as current.
	stale := 0
	_, _ = c.get("race", func() (any, error) { c.invalidate(); stale++; return "old", nil })
	if v, _ := c.get("race", func() (any, error) { stale++; return "new", nil }); v != "new" {
		t.Fatalf("a value loaded across an invalidation was kept: %v", v)
	}

	var nilCache *ttlCache
	if v, err := nilCache.get("x", func() (any, error) { return 7, nil }); err != nil || v != 7 {
		t.Fatalf("a nil cache must simply load: %v %v", v, err)
	}
}

func TestSessionWaitersWakeOnlyTheirSession(t *testing.T) {
	w := newSessionWaiters()
	a, releaseA := w.wait("s-a")
	b, releaseB := w.wait("s-b")
	defer releaseB()
	w.wake("s-a")
	select {
	case <-a:
	case <-time.After(time.Second):
		t.Fatal("the waiter was not woken")
	}
	select {
	case <-b:
		t.Fatal("another session's waiter was woken")
	default:
	}
	releaseA()
	w.wake("s-a") // no waiter left: must not panic or block
}

func TestCeilingFollowsTheLicenceNeverBelowTheFloor(t *testing.T) {
	for _, c := range []struct{ configured, licensed, want int }{
		{0, 0, 600},       // unlicensed or unknown
		{0, 400, 600},     // a small licence keeps the floor
		{0, 1200, 600},    // 1 attempt per guest per 2 minutes = 600
		{0, 2000, 1000},   // a larger licence raises it
		{900, 2000, 900},  // a configured value wins
		{300, 2000, 1000}, // a configured value below the floor is ignored (the database refuses it too)
	} {
		if got := ceilingFor(c.configured, c.licensed); got != c.want {
			t.Errorf("ceilingFor(%d, %d) = %d, want %d", c.configured, c.licensed, got, c.want)
		}
	}
}

func TestScdPoolIsSizedAndTheDSNStillWins(t *testing.T) {
	cfg, err := scdPoolConfig("postgres://u:p@127.0.0.1/db?sslmode=disable")
	if err != nil || cfg.MaxConns != scdDefaultPoolMaxConns {
		t.Fatalf("MaxConns = %v err %v, want %d", cfg, err, scdDefaultPoolMaxConns)
	}
	cfg, err = scdPoolConfig("postgres://u:p@127.0.0.1/db?sslmode=disable&pool_max_conns=3")
	if err != nil || cfg.MaxConns != 3 {
		t.Fatalf("a pool_max_conns in the DSN must win, got %v err %v", cfg, err)
	}
}
