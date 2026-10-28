package pmsd

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newBarrierSink(t *testing.T) (*workerSink, *fakeRepo, string) {
	t.Helper()
	repo := newFakeRepo(iface("i1"))
	id := ifaceUUID("i1")
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	w := &worker{iface: iface("i1"), repo: repo, deps: &Deps{Now: time.Now}}
	s := &workerSink{w: w, ctx: context.Background(), gen: gen, q: NewBoundedQueue(16, time.Second)}
	return s, repo, id
}

func counts(r *fakeRepo) (live, staged int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.liveRows), len(r.stagedRows)
}

// No live admission before the initial publish; records inside DS→DE are staged only; a valid DE publishes atomically.
func TestBarrier_FullResyncLifecycle(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	ctx := context.Background()

	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("A")); err != nil {
		t.Fatal(err)
	}
	if live, staged := counts(repo); live != 0 || staged != 0 {
		t.Fatalf("before DS: live=%d staged=%d, want 0/0", live, staged)
	}

	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("B")); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("C")); err != nil {
		t.Fatal(err)
	}
	if live, staged := counts(repo); live != 0 || staged != 2 {
		t.Fatalf("during window: live=%d staged=%d, want 0/2", live, staged)
	}
	// A heartbeat must not lower the barrier.
	if err := s.OnHeartbeat(time.Now()); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 0 {
		t.Fatalf("heartbeat admitted live events: %d", live)
	}
	if repo.publishedGen[id] != 0 {
		t.Fatal("nothing may be published before DE")
	}

	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if repo.publishedGen[id] == 0 {
		t.Fatal("valid DE must publish the resync generation")
	}
	if err := s.OnDomainEvent(ctx, ev("D")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 1 {
		t.Fatalf("after publish: live=%d, want 1", live)
	}
}

func TestBarrier_NoPublishWithoutDE(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	ctx := context.Background()
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("R1")); err != nil {
		t.Fatal(err)
	}
	if err := s.OnContinuityFault(ctx, CodeEventInvalid); err != nil {
		t.Fatal(err)
	}
	if repo.publishedGen[id] != 0 {
		t.Fatal("a fault before DE must publish nothing")
	}
	if err := s.OnDomainEvent(ctx, ev("R2")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 0 {
		t.Fatalf("no live admission without a published generation, got %d", live)
	}
}

// Audit P7 L1: after a continuity fault on a synced link, nothing is admitted LIVE and continuity is not set back to
// CONTINUOUS until the requested resync publishes.
func TestBarrier_GapClosesLiveAdmissionUntilResyncPublishes(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	ctx := context.Background()
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("L1")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 1 {
		t.Fatalf("synced link: live=%d, want 1", live)
	}
	if err := s.OnContinuityFault(ctx, CodeEventInvalid); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("L2")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 1 {
		t.Fatalf("after a gap, before DS: live=%d, want 1 (nothing new admitted)", live)
	}
	repo.mu.Lock()
	cont := repo.continuity[id]
	repo.mu.Unlock()
	if cont != ContinuityGapDetected {
		t.Fatalf("continuity after a gap and a later record = %q, want GAP_DETECTED", cont)
	}
	// The resync re-establishes the roster and lowers the barrier.
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("L3")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 2 {
		t.Fatalf("after the resync publishes: live=%d, want 2", live)
	}
}

// Audit P7 M2: the same record twice inside one window is staged once, counted as skipped, and the generation still
// publishes.
func TestBarrier_RepeatedRecordInsideResyncIsSkipped(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	ctx := context.Background()
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{ev("G1"), ev("G1"), ev("G2")} {
		if err := s.OnDomainEvent(ctx, e); err != nil {
			t.Fatalf("a repeated record must not end the window: %v", err)
		}
	}
	s.RecordSkipped(2) // the adapter's own skips add to the repeats
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if _, staged := counts(repo); staged != 2 {
		t.Fatalf("staged = %d, want 2", staged)
	}
	if repo.publishedGen[id] == 0 {
		t.Fatal("the generation must publish")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.lastSkipped != 3 || repo.lastReceived != 2 {
		t.Fatalf("skipped=%d received=%d, want 3 and 2", repo.lastSkipped, repo.lastReceived)
	}
}

// Audit P7 L9: a disconnect inside a DS..DE window records the INTERRUPTED stage.
func TestBarrier_DisconnectMidWindowIsInterrupted(t *testing.T) {
	s, repo, _ := newBarrierSink(t)
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDisconnected(time.Now(), CodeDialFailed); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if n := len(repo.stages); n == 0 || repo.stages[n-1] != string(StageInterrupted) {
		t.Fatalf("stages = %v, want last %s", repo.stages, StageInterrupted)
	}
}

func TestBarrier_DEWithoutDSPublishesNothing(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if repo.publishedGen[id] != 0 {
		t.Fatal("a DE without a DS must publish nothing")
	}
	if s.synced {
		t.Fatal("barrier must remain up after a spurious DE")
	}
}

func TestBarrier_StaleOwnerAdmitsNothing(t *testing.T) {
	s, repo, _ := newBarrierSink(t)
	if _, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: ifaceUUID("i1")}); err != nil {
		t.Fatal(err)
	}
	s.synced = true // even past the barrier, a stale owner must admit nothing
	if err := s.OnDomainEvent(context.Background(), ev("X")); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale owner OnDomainEvent = %v, want ErrStaleGeneration", err)
	}
	if live, staged := counts(repo); live != 0 || staged != 0 {
		t.Fatalf("stale owner admitted/staged: live=%d staged=%d", live, staged)
	}
}
