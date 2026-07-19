package pmsd

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestQueue_EnqueueDequeueFIFO(t *testing.T) {
	q := NewBoundedQueue(4, time.Second)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(ctx, Event{Record: string(rune('A' + i))}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		ev, err := q.Dequeue(ctx)
		if err != nil {
			t.Fatalf("dequeue %d: %v", i, err)
		}
		if ev.Record != string(rune('A'+i)) {
			t.Errorf("FIFO violated: got %q want %q", ev.Record, string(rune('A'+i)))
		}
	}
}

func TestQueue_OverflowIsErrorNotSilentDrop(t *testing.T) {
	q := NewBoundedQueue(2, 50*time.Millisecond)
	ctx := context.Background()
	_ = q.Enqueue(ctx, Event{Record: "1"})
	_ = q.Enqueue(ctx, Event{Record: "2"})
	// third enqueue: full, must return QUEUE_OVERFLOW after the back-pressure timeout, and latch resync
	err := q.Enqueue(ctx, Event{Record: "3"})
	if err == nil {
		t.Fatal("expected overflow error, got nil (silent drop)")
	}
	if Classify(err) != CodeQueueOverflow {
		t.Errorf("overflow code = %q, want QUEUE_OVERFLOW", Classify(err))
	}
	if !q.OverflowResyncRequested() {
		t.Error("overflow must latch resync-required")
	}
	// the two accepted events are still present (no loss)
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
	_ = q.Enqueue(context.Background(), Event{Record: "full"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := q.Enqueue(ctx, Event{Record: "blocked"})
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
	_ = q.Enqueue(ctx, Event{Record: "x"})
	_ = q.Enqueue(ctx, Event{Record: "y"})
	q.Close()
	// enqueue after close is rejected
	if err := q.Enqueue(ctx, Event{Record: "z"}); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("enqueue after close = %v, want ErrQueueClosed", err)
	}
	// buffered events still drain
	if ev, err := q.Dequeue(ctx); err != nil || ev.Record != "x" {
		t.Errorf("drain1 = %v/%v", ev.Record, err)
	}
	if ev, err := q.Dequeue(ctx); err != nil || ev.Record != "y" {
		t.Errorf("drain2 = %v/%v", ev.Record, err)
	}
	// then closed
	if _, err := q.Dequeue(ctx); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("post-drain dequeue = %v, want ErrQueueClosed", err)
	}
}

func TestQueue_NoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		q := NewBoundedQueue(8, 10*time.Millisecond)
		ctx := context.Background()
		for j := 0; j < 8; j++ {
			_ = q.Enqueue(ctx, Event{Record: "e"})
		}
		_ = q.Enqueue(ctx, Event{Record: "overflow"}) // forces the timeout path
		q.Close()
	}
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}
