package pmsd

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ev(id string) Event {
	iface := "aaaaaaaa-0000-4000-8000-000000000001"
	se := newSourceEvent(iface, RecGI, 1, "protel-fias/v1", "", []FieldPair{{Code: "G#", Value: id}, {Code: "RN", Value: "101"}})
	fp, fpv := ComputeSourceFingerprint([]byte("test-identity-key"), 1, se)
	h, ver := ComputeEvidenceHMAC([]byte("test-evidence-key"), 1, id)
	return Event{
		InterfaceID: iface, RevisionID: "33333333-0000-4000-8000-000000000001",
		SecretGenerationID: "44444444-0000-4000-8000-000000000001", NormalizationVer: 1,
		RecordType: RecGI, ReservationRef: id, RoomNumber: "101",
		SourceEventFingerprint: fp, FingerprintKeyVersion: fpv, ExternalEventIdentity: fp,
		StayResolutionCandidate: DeriveStayResolutionCandidate(tTenantUUID, tSiteUUID, iface, id),
		SourceEvidenceHash:      h, EvidenceKeyVersion: ver, NormalizedAt: time.Now(),
	}
}

func TestQueue_EnqueueDequeueFIFO(t *testing.T) {
	q := NewBoundedQueue(4, time.Second)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(ctx, ev(fmt.Sprintf("E%d", i))); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		e, err := q.Dequeue(ctx)
		if err != nil {
			t.Fatalf("dequeue %d: %v", i, err)
		}
		if e.ReservationRef != fmt.Sprintf("E%d", i) {
			t.Errorf("FIFO violated: got %q want E%d", e.ReservationRef, i)
		}
	}
}

func TestQueue_OverflowIsErrorNotSilentDrop(t *testing.T) {
	q := NewBoundedQueue(2, 50*time.Millisecond)
	ctx := context.Background()
	_ = q.Enqueue(ctx, ev("1"))
	_ = q.Enqueue(ctx, ev("2"))
	err := q.Enqueue(ctx, ev("3"))
	if err == nil {
		t.Fatal("expected overflow error, got nil (silent drop)")
	}
	if Classify(err) != CodeQueueOverflow {
		t.Errorf("overflow code = %q, want QUEUE_OVERFLOW", Classify(err))
	}
	if !q.OverflowResyncRequested() {
		t.Error("overflow must latch resync-required")
	}
	if q.Len() != 2 {
		t.Errorf("expected 2 buffered events, got %d", q.Len())
	}
	q.ClearOverflowResync()
	if q.OverflowResyncRequested() {
		t.Error("resync latch should clear")
	}
}

func TestQueue_EnqueueRespectsContextCancel(t *testing.T) {
	q := NewBoundedQueue(1, 5*time.Second)
	_ = q.Enqueue(context.Background(), ev("full"))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := q.Enqueue(ctx, ev("blocked"))
	if Classify(err) != CodeContextCanceled {
		t.Errorf("expected CONTEXT_CANCELED on cancel, got %q", Classify(err))
	}
}

func TestQueue_DequeueRespectsContextCancel(t *testing.T) {
	q := NewBoundedQueue(1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := q.Dequeue(ctx)
	if Classify(err) != CodeContextCanceled {
		t.Errorf("expected CONTEXT_CANCELED, got %q", Classify(err))
	}
}

func TestQueue_CloseDrainsThenErrors(t *testing.T) {
	q := NewBoundedQueue(4, time.Second)
	ctx := context.Background()
	_ = q.Enqueue(ctx, ev("x"))
	_ = q.Enqueue(ctx, ev("y"))
	q.Close()
	if err := q.Enqueue(ctx, ev("z")); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("enqueue after close = %v, want ErrQueueClosed", err)
	}
	if e, err := q.Dequeue(ctx); err != nil || e.ReservationRef != "x" {
		t.Errorf("drain1 = %v/%v", e.ReservationRef, err)
	}
	if e, err := q.Dequeue(ctx); err != nil || e.ReservationRef != "y" {
		t.Errorf("drain2 = %v/%v", e.ReservationRef, err)
	}
	if _, err := q.Dequeue(ctx); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("post-drain dequeue = %v, want ErrQueueClosed", err)
	}
}

func TestQueue_CloseIdempotent(t *testing.T) {
	q := NewBoundedQueue(2, time.Second)
	q.Close()
	q.Close() // must not panic
	q.Close()
}

// TestQueue_LinearizableClose_NoAcceptAfterClose is the core §1 property under heavy concurrency:
// once Close() RETURNS, no Enqueue may have been accepted after that boundary, and the total
// accepted == total drained (no event lost, none double-counted). Runs repeatedly with 100+ producers,
// concurrent consumers, and a concurrent close.
func TestQueue_LinearizableClose_NoAcceptAfterClose(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		q := NewBoundedQueue(64, 100*time.Millisecond)
		ctx := context.Background()

		var accepted int64 // Enqueue returned nil
		var acceptedAfterClose int64
		var closeReturned int32

		var drained int64
		consumerDone := make(chan struct{})
		var consumerWG sync.WaitGroup
		for c := 0; c < 4; c++ {
			consumerWG.Add(1)
			go func() {
				defer consumerWG.Done()
				for {
					select {
					case <-consumerDone:
						// final drain
						for {
							if _, err := q.Dequeue(ctx); err != nil {
								return
							}
							atomic.AddInt64(&drained, 1)
						}
					default:
						cctx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
						_, err := q.Dequeue(cctx)
						cancel()
						if err == nil {
							atomic.AddInt64(&drained, 1)
						} else if errors.Is(err, ErrQueueClosed) {
							return
						}
					}
				}
			}()
		}

		var prodWG sync.WaitGroup
		for p := 0; p < 100; p++ {
			prodWG.Add(1)
			go func(p int) {
				defer prodWG.Done()
				for i := 0; i < 20; i++ {
					// Observe the close boundary BEFORE the call: the guarantee is that an Enqueue which
					// STARTS after Close() has returned is never accepted. Reading closeReturned AFTER the
					// call would falsely flag an Enqueue that legitimately committed while Close() was still
					// blocked on the write lock and was only observed afterward (a measurement race, not a
					// linearizability violation).
					startedAfterClose := atomic.LoadInt32(&closeReturned) == 1
					err := q.Enqueue(ctx, ev(fmt.Sprintf("p%d-%d", p, i)))
					if err == nil {
						atomic.AddInt64(&accepted, 1)
						if startedAfterClose {
							atomic.AddInt64(&acceptedAfterClose, 1)
						}
					}
				}
			}(p)
		}

		time.Sleep(2 * time.Millisecond)
		q.Close()
		atomic.StoreInt32(&closeReturned, 1)

		prodWG.Wait()
		close(consumerDone)
		consumerWG.Wait()

		if acceptedAfterClose != 0 {
			t.Fatalf("iter %d: %d events accepted after Close() returned", iter, acceptedAfterClose)
		}
		if atomic.LoadInt64(&accepted) != atomic.LoadInt64(&drained) {
			t.Fatalf("iter %d: accounting mismatch accepted=%d drained=%d", iter, accepted, drained)
		}
	}
}

func TestQueue_InvalidEventRejectedBeforeCapacity(t *testing.T) {
	q := NewBoundedQueue(4, time.Second)
	bad := Event{RecordType: "GI"} // missing UUIDs / normalization / timestamp
	if err := q.Enqueue(context.Background(), bad); Classify(err) != CodeEventInvalid {
		t.Fatalf("invalid event must be EVENT_INVALID, got %v", Classify(err))
	}
	if q.Len() != 0 {
		t.Fatalf("invalid event must not consume capacity, len=%d", q.Len())
	}
}

// TestQueue_CloseLatencyBounded proves Close returns within ~enqueueTimeout even with a producer blocked on
// a full queue (it cannot exceed the worker shutdown deadline).
func TestQueue_CloseLatencyBounded(t *testing.T) {
	timeout := 60 * time.Millisecond
	q := NewBoundedQueue(1, timeout)
	_ = q.Enqueue(context.Background(), ev("fill"))
	go func() { _ = q.Enqueue(context.Background(), ev("blocked")) }() // will back-pressure up to timeout
	time.Sleep(5 * time.Millisecond)                                   // let the producer enter back-pressure
	start := time.Now()
	q.Close()
	if d := time.Since(start); d > timeout+40*time.Millisecond {
		t.Fatalf("Close latency %s exceeded bound ~%s", d, timeout)
	}
}

// TestQueue_OverflowLatchPersists proves the resync latch is NOT cleared by ordinary activity — only an
// explicit ClearOverflowResync (which the worker calls after a verified resync) clears it.
func TestQueue_OverflowLatchPersists(t *testing.T) {
	q := NewBoundedQueue(1, 20*time.Millisecond)
	_ = q.Enqueue(context.Background(), ev("1"))
	_ = q.Enqueue(context.Background(), ev("2")) // overflow -> latch
	if !q.OverflowResyncRequested() {
		t.Fatal("overflow must latch resync")
	}
	// draining a buffered event (ordinary activity) must NOT clear the latch
	_, _ = q.Dequeue(context.Background())
	if !q.OverflowResyncRequested() {
		t.Fatal("draining must not clear the overflow latch")
	}
	q.ClearOverflowResync()
	if q.OverflowResyncRequested() {
		t.Fatal("explicit clear should reset the latch")
	}
}

func TestQueue_NoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		q := NewBoundedQueue(8, 10*time.Millisecond)
		ctx := context.Background()
		for j := 0; j < 8; j++ {
			_ = q.Enqueue(ctx, ev("e"))
		}
		_ = q.Enqueue(ctx, ev("overflow"))
		q.Close()
	}
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}
