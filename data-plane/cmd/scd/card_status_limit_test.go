package main

import (
	"fmt"
	"testing"
	"time"
)

// P6 L15: a full limiter prunes expired entries; a transaction checked a moment ago stays limited.
func TestCardStatusLimiterPrunesInsteadOfResetting(t *testing.T) {
	s := &server{card: &cardState{lastStatus: map[string]time.Time{}}}
	old := time.Now().Add(-time.Minute)
	for i := 0; i < 5000; i++ {
		s.card.lastStatus[fmt.Sprintf("old-%d", i)] = old
	}
	if !s.statusAllowed("recent") {
		t.Fatal("first check of a transaction must pass")
	}
	if s.statusAllowed("recent") {
		t.Fatal("the transaction just checked must stay limited after the limiter filled up")
	}
	if len(s.card.lastStatus) > 10 {
		t.Fatalf("expired entries were not pruned: %d left", len(s.card.lastStatus))
	}
}
