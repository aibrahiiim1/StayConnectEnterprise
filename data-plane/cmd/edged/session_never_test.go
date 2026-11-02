package main

import (
	"testing"
	"time"
)

// Migration 0204: 0 means never, for the idle limit and the longest session independently.
func TestSessionLimitsCanBeNever(t *testing.T) {
	created := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	seen := created.Add(3 * time.Hour)
	sess := &session{CreatedAt: created, LastSeen: seen}

	normal := sessionPolicy{IdleTimeoutMinutes: 30, MaxSessionHours: 12}
	if got := sessionEnds(sess, normal); !got.Equal(seen.Add(30 * time.Minute)) {
		t.Fatalf("the idle limit ends it first: %v", got)
	}
	neverIdle := sessionPolicy{IdleTimeoutMinutes: 0, MaxSessionHours: 12}
	if got := sessionEnds(sess, neverIdle); !got.Equal(created.Add(12 * time.Hour)) {
		t.Fatalf("never idle: only the longest session ends it: %v", got)
	}
	neverMax := sessionPolicy{IdleTimeoutMinutes: 30, MaxSessionHours: 0}
	if got := sessionEnds(sess, neverMax); !got.Equal(seen.Add(30 * time.Minute)) {
		t.Fatalf("never longest: only inactivity ends it: %v", got)
	}
	both := sessionPolicy{}
	if got := sessionEnds(sess, both); got.Before(created.Add(50 * 365 * 24 * time.Hour)) {
		t.Fatalf("never and never: the session does not end by itself: %v", got)
	}
	if both.idle() <= 0 || both.max() <= 0 {
		t.Fatal("a never limit must not overflow into a negative duration")
	}
}

func TestSessionBoundsAllowNeverOnBothLimits(t *testing.T) {
	if len(sessionBounds) != 2 {
		t.Fatalf("two limits: %d", len(sessionBounds))
	}
	for _, b := range sessionBounds {
		if !b.AllowNever {
			t.Errorf("%s must offer Never", b.Key)
		}
	}
}
