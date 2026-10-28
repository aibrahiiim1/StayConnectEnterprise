package enforce

import (
	"errors"
	"testing"
	"time"
)

// Audit perf D5-L1: a catalog probe is reused within its TTL, re-run after it, and a failure is never cached.
func TestProbeCacheReusesWithinTTLAndNeverCachesFailures(t *testing.T) {
	clock := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c := &probeCache{now: func() time.Time { return clock }}
	calls := 0
	probe := func() ([]bool, error) { calls++; return []bool{true}, nil }

	for i := 0; i < 5; i++ {
		if v, err := c.get("k", probe); err != nil || !v[0] {
			t.Fatalf("get: %v %v", v, err)
		}
	}
	if calls != 1 {
		t.Fatalf("probed %d times within the TTL, want 1", calls)
	}
	clock = clock.Add(catalogProbeTTL)
	_, _ = c.get("k", probe)
	if calls != 2 {
		t.Fatalf("probed %d times after the TTL, want 2", calls)
	}

	failing := func() ([]bool, error) { calls++; return nil, errors.New("db down") }
	if _, err := c.get("other", failing); err == nil {
		t.Fatal("a failed probe returned no error")
	}
	if _, err := c.get("other", failing); err == nil || calls != 4 {
		t.Fatalf("a failed probe was cached (calls=%d)", calls)
	}
}
