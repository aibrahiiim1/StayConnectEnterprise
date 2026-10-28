package pmsd

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// batchRepo is fakeRepo plus the batched writes; it records each batch's size.
type batchRepo struct {
	*fakeRepo
	batches []int
}

func (r *batchRepo) StageResyncEvents(ctx context.Context, rows []InboxRow) (int, int, error) {
	r.batches = append(r.batches, len(rows))
	staged, dups := 0, 0
	for _, row := range rows {
		if _, err := r.fakeRepo.StageResyncEvent(ctx, row); err != nil {
			if err == ErrDuplicateResyncEvent {
				dups++
				continue
			}
			return 0, 0, err
		}
		staged++
	}
	return staged, dups, nil
}

// Performance audit D6-M2: roster rows go to the repository in batches of stageBatchSize, the remainder before the
// publish; nothing is published before every record is written, and repeats are counted as skipped.
func TestBatchStaging_FlushesAtSizeAndBeforePublish(t *testing.T) {
	base := newFakeRepo(iface("i1"))
	repo := &batchRepo{fakeRepo: base}
	id := ifaceUUID("i1")
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	w := &worker{iface: iface("i1"), repo: repo, deps: &Deps{Now: time.Now}}
	s := &workerSink{w: w, ctx: context.Background(), gen: gen}
	ctx := context.Background()
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2*stageBatchSize+20; i++ {
		if err := s.OnDomainEvent(ctx, ev(fmt.Sprintf("R%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.OnDomainEvent(ctx, ev("R005")); err != nil { // a repeat
		t.Fatal(err)
	}
	if len(repo.batches) != 2 || repo.publishedGen[id] != 0 {
		t.Fatalf("before DE: batches=%v published=%d, want two full batches and nothing published", repo.batches, repo.publishedGen[id])
	}
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if len(repo.batches) != 3 || repo.batches[2] != 21 {
		t.Fatalf("batches = %v, want [50 50 21]", repo.batches)
	}
	if repo.publishedGen[id] == 0 {
		t.Fatal("the generation must publish after the last batch")
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.stagedRows) != 2*stageBatchSize+20 || base.lastReceived != 2*stageBatchSize+20 || base.lastSkipped != 1 {
		t.Fatalf("staged=%d received=%d skipped=%d", len(base.stagedRows), base.lastReceived, base.lastSkipped)
	}
}

// A link lost mid-window drops the unwritten buffer; the next window starts empty.
func TestBatchStaging_DisconnectDropsBuffer(t *testing.T) {
	base := newFakeRepo(iface("i1"))
	repo := &batchRepo{fakeRepo: base}
	gen, _ := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: ifaceUUID("i1")})
	w := &worker{iface: iface("i1"), repo: repo, deps: &Deps{Now: time.Now}}
	s := &workerSink{w: w, ctx: context.Background(), gen: gen}
	_ = s.RequireInitialResync(time.Now())
	_ = s.OnResyncStart(time.Now())
	_ = s.OnDomainEvent(context.Background(), ev("X1"))
	_ = s.OnDisconnected(time.Now(), CodeDialFailed)
	if len(s.stageBuf) != 0 || len(repo.batches) != 0 {
		t.Fatalf("buffer=%d batches=%v after a drop", len(s.stageBuf), repo.batches)
	}
}
