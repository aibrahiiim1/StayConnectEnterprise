package pmsd

import (
	"context"
	"testing"
	"time"
)

// newBarrierSink builds a workerSink over a fakeRepo with an owned runtime generation, for driving the §G/§H
// state machine directly (the adapter calls these methods serially in one goroutine).
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

// TestBarrier_FullResyncLifecycle proves §H + §G end to end at the sink: no LIVE admission before the initial
// publish; records inside DS→DE are STAGED only; a heartbeat cannot lower the barrier; a valid DE atomically
// publishes and only THEN does LIVE admission occur.
func TestBarrier_FullResyncLifecycle(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	ctx := context.Background()

	// barrier up at connect
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	// a LIVE GI before any DS → held (zero live, zero staged)
	if err := s.OnDomainEvent(ctx, ev("A")); err != nil {
		t.Fatal(err)
	}
	if live, staged := counts(repo); live != 0 || staged != 0 {
		t.Fatalf("before DS: live=%d staged=%d, want 0/0", live, staged)
	}

	// DS opens the window; records are STAGED (never live)
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
	// a heartbeat during the window must NOT lower the barrier or admit anything
	if err := s.OnHeartbeat(time.Now()); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 0 {
		t.Fatalf("heartbeat admitted live events: %d", live)
	}
	if repo.publishedGen[id] != 0 {
		t.Fatal("nothing may be published before DE")
	}

	// DE publishes the complete generation atomically → barrier down
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if repo.publishedGen[id] == 0 {
		t.Fatal("valid DE must publish the resync generation")
	}
	// now LIVE admission works
	if err := s.OnDomainEvent(ctx, ev("D")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 1 {
		t.Fatalf("after publish: live=%d, want 1", live)
	}
}

// TestBarrier_NoPublishWithoutDE proves a partial/failed resync (DS without a valid DE) publishes nothing and
// leaves the barrier up, and that a malformed record before DE does not publish either.
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
	// a malformed record before DE → continuity fault; still nothing published, barrier stays up
	if err := s.OnContinuityFault(ctx, CodeEventInvalid); err != nil {
		t.Fatal(err)
	}
	if repo.publishedGen[id] != 0 {
		t.Fatal("a fault before DE must publish nothing")
	}
	// a disconnect now (no DE) — nothing was activated; a later LIVE event is still held
	if err := s.OnDomainEvent(ctx, ev("R2")); err != nil {
		t.Fatal(err)
	}
	if live, _ := counts(repo); live != 0 {
		t.Fatalf("no live admission without a published generation, got %d", live)
	}
}

// TestBarrier_DEWithoutDSPublishesNothing proves a spurious DE (no preceding DS) is a no-op (never publishes).
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

// TestBarrier_StaleOwnerAdmitsNothing proves a generation-CAS miss (newer owner) admits/stages nothing.
func TestBarrier_StaleOwnerAdmitsNothing(t *testing.T) {
	s, repo, _ := newBarrierSink(t)
	// a newer owner bumps the stored generation so the sink's pinned gen is stale
	if _, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: ifaceUUID("i1")}); err != nil {
		t.Fatal(err)
	}
	s.synced = true // even past the barrier, a stale owner must admit nothing
	if err := s.OnDomainEvent(context.Background(), ev("X")); err != ErrStaleGeneration {
		t.Fatalf("stale owner OnDomainEvent = %v, want ErrStaleGeneration", err)
	}
	if live, staged := counts(repo); live != 0 || staged != 0 {
		t.Fatalf("stale owner admitted/staged: live=%d staged=%d", live, staged)
	}
}
