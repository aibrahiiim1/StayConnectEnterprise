package applianceauth

import (
	"fmt"
	"testing"
	"time"
)

// Audit Phase 1 L20: entries expire in order, the cache stops at a hard ceiling instead of growing, and a token
// is still refused within its TTL.
func TestReplayCacheExpiresAndIsBounded(t *testing.T) {
	rc := NewReplayCache(time.Minute, 4)
	t0 := time.Unix(1_700_000_000, 0)
	ceiling := 4 * replayCeilingFactor
	for i := 0; i < ceiling; i++ {
		if err := rc.Use(fmt.Sprintf("j%d", i), t0); err != nil {
			t.Fatalf("token %d within the ceiling: %v", i, err)
		}
	}
	if err := rc.Use("one-too-many", t0); err != ErrReplayCacheFull {
		t.Fatalf("past the ceiling of unexpired tokens Use must refuse, got %v", err)
	}
	if err := rc.Use("j0", t0.Add(30*time.Second)); err != ErrReplay {
		t.Fatalf("a token within its TTL is a replay, got %v", err)
	}
	// After the TTL everything has expired: the cache is empty again and the old ids are usable.
	later := t0.Add(time.Minute + time.Second)
	if err := rc.Use("j0", later); err != nil {
		t.Fatalf("an expired id may be used again: %v", err)
	}
	if len(rc.seen) != 1 || len(rc.order)-rc.head != 1 {
		t.Fatalf("expired entries must be gone: map %d, queue %d", len(rc.seen), len(rc.order)-rc.head)
	}
	if err := rc.Use("j0", later.Add(time.Second)); err != ErrReplay {
		t.Fatalf("the re-used id is a replay within its new TTL, got %v", err)
	}
}
