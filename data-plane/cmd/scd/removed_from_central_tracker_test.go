package main

import (
	"testing"
	"time"
)

// A deletion is acted on only when sustained: orphanConfirmReplies consecutive replies over at least orphanConfirmWindow.

func TestOrphanTrackerTwoQuickRepliesDoNotConfirm(t *testing.T) {
	var o orphanTracker
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	if o.observe(t0, true, true) || o.observe(t0.Add(3*time.Second), true, true) {
		t.Fatal("two replies three seconds apart confirmed a deletion")
	}
}

func TestOrphanTrackerEnoughRepliesButTooShortAWindowDoNotConfirm(t *testing.T) {
	var o orphanTracker
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ { // many replies, all inside the window
		if o.observe(t0.Add(time.Duration(i)*30*time.Second), true, true) {
			t.Fatalf("confirmed after %d replies within %v", i+1, time.Duration(i)*30*time.Second)
		}
	}
}

func TestOrphanTrackerSustainedRepliesConfirm(t *testing.T) {
	var o orphanTracker
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	// The default five-minute hello cadence: t0, t0+5m, t0+10m.
	if o.observe(t0, true, true) || o.observe(t0.Add(5*time.Minute), true, true) {
		t.Fatal("confirmed early")
	}
	if !o.observe(t0.Add(10*time.Minute), true, true) {
		t.Fatal("three replies over ten minutes did not confirm")
	}
}

func TestOrphanTrackerWindowAloneIsNotEnough(t *testing.T) {
	var o orphanTracker
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	// Two replies an hour apart: a long window, but fewer replies than required.
	if o.observe(t0, true, true) || o.observe(t0.Add(time.Hour), true, true) {
		t.Fatal("two replies confirmed a deletion")
	}
}

func TestOrphanTrackerAnyOtherReplyResets(t *testing.T) {
	var o orphanTracker
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	o.observe(t0, true, true)
	o.observe(t0.Add(5*time.Minute), true, true)
	o.observe(t0.Add(7*time.Minute), true, false) // a successful hello (or any other definite reply)
	if o.observe(t0.Add(10*time.Minute), true, true) {
		t.Fatal("a reset run confirmed")
	}
	// The run starts again from the reply after the reset.
	if o.observe(t0.Add(15*time.Minute), true, true) {
		t.Fatal("confirmed after two replies of the new run")
	}
	if !o.observe(t0.Add(20*time.Minute), true, true) {
		t.Fatal("the new run did not confirm once sustained")
	}
}

func TestOrphanTrackerNoReplyIsNoEvidence(t *testing.T) {
	var o orphanTracker
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	o.observe(t0, true, true)
	o.observe(t0.Add(5*time.Minute), false, false) // Central unreachable: neither resets nor counts
	if o.observe(t0.Add(8*time.Minute), true, true) {
		t.Fatal("confirmed inside the window")
	}
	if !o.observe(t0.Add(10*time.Minute), true, true) {
		t.Fatal("a transport failure reset the run")
	}
	// After confirming, the tracker starts afresh.
	if o.observe(t0.Add(11*time.Minute), true, true) {
		t.Fatal("confirmation did not reset the tracker")
	}
}
