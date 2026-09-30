package pmsd

import (
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// ---- the scheduler: cadence, serialization, backoff and the unanswered release ------------------------------

func TestFinResync_AsksAtMostOncePerMinuteAndNeverWhileResyncing(t *testing.T) {
	var f finResyncScheduler
	t0 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	asks := 0
	ask := func() bool { asks++; return false }
	if f.shouldRequest(t0, true, ask) || asks != 0 {
		t.Fatal("a resync already outstanding is never joined by another, and the database is not even asked")
	}
	f.shouldRequest(t0, false, ask)
	f.shouldRequest(t0.Add(30*time.Second), false, ask)
	if asks != 1 {
		t.Fatalf("asked %d times within one minute, want 1", asks)
	}
	f.shouldRequest(t0.Add(61*time.Second), false, ask)
	if asks != 2 {
		t.Fatalf("a minute later it asks again: %d", asks)
	}
}

func TestFinResync_BacksOffAfterEachRequestAndResetsOnlyOnACompleteResync(t *testing.T) {
	var f finResyncScheduler
	t0 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	due := func() bool { return true }
	if !f.shouldRequest(t0, false, due) {
		t.Fatal("due and idle: request")
	}
	f.requested(t0)
	// Unanswered and due again: not before the 10-minute backoff, then 20, then capped at 40.
	for _, step := range []struct{ wait time.Duration }{{10 * time.Minute}, {20 * time.Minute}, {40 * time.Minute}, {40 * time.Minute}} {
		at := f.requestedAt
		if f.shouldRequest(at.Add(step.wait-2*time.Minute), false, due) {
			t.Fatalf("requested again before the %v backoff", step.wait)
		}
		next := at.Add(step.wait + time.Minute)
		if !f.shouldRequest(next, false, due) {
			t.Fatalf("not requested after the %v backoff", step.wait)
		}
		f.requested(next)
	}
	f.completed()
	if f.backoff != finResyncBackoffMin {
		t.Fatalf("a complete resync resets the backoff: %v", f.backoff)
	}
}

func TestFinResync_AnUnansweredRequestReleasesOnceAndAStartedOneNever(t *testing.T) {
	var f finResyncScheduler
	t0 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.requested(t0)
	if f.unanswered(t0.Add(4 * time.Minute)) {
		t.Fatal("released before the 5-minute bound")
	}
	if !f.unanswered(t0.Add(6*time.Minute)) || f.unanswered(t0.Add(7*time.Minute)) {
		t.Fatal("an unanswered request is released exactly once")
	}
	f.requested(t0.Add(20 * time.Minute))
	f.started()
	if f.unanswered(t0.Add(40 * time.Minute)) {
		t.Fatal("a request the PMS began answering (DS) is never released by the timeout")
	}
}

// ---- the adapter: one DR from the owning loop, never inside a roster, none when not due ---------------------

func TestFinResync_TheAdapterRequestsOneDRWhenDue(t *testing.T) {
	due := true
	sink := &recordingSink{finDue: &due}
	frames := runAdapterWithFrames(t, sink,
		pms.BuildLS("260930", "120000"), // inside the initial sync: not asked
		"DS|",
		"DE|",
		pms.BuildLS("260930", "120100"), // first frame after the initial sync: due -> one DR
		pms.BuildLS("260930", "120200"), // our DR is outstanding: nothing more
		pms.BuildLS("260930", "120300"),
	)
	if got := countFrames(frames, "DR"); got != 2 {
		t.Fatalf("want the initial DR plus exactly one financial DR, got %d: %v", got, frames)
	}
}

func TestFinResync_NotDueMeansNoExtraDR(t *testing.T) {
	notDue := false
	sink := &recordingSink{finDue: &notDue}
	frames := runAdapterWithFrames(t, sink,
		pms.BuildLS("260930", "120000"),
		"DS|",
		"DE|",
		pms.BuildLS("260930", "120100"),
	)
	if got := countFrames(frames, "DR"); got != 1 {
		t.Fatalf("only the automatic initial DR, got %d: %v", got, frames)
	}
	if sink.finAsks == 0 {
		t.Fatal("the adapter never asked whether the financial mirror was due")
	}
}
