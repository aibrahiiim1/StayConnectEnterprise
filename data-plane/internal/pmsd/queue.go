package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Event is one normalized protocol observation handed from the read-only adapter to the (future, Increment-4)
// Stay ingestion engine. The queue only buffers events in memory; it never logs their contents.
type Event struct {
	Record     string // raw FIAS record text (opaque to the queue; never logged by pmsd)
	ReceivedAt time.Time
}

// ErrQueueClosed is returned by Dequeue once the queue is closed and fully drained.
var ErrQueueClosed = errors.New("pmsd: event queue closed")

// BoundedQueue is a fixed-capacity, back-pressured event queue between the protocol adapter and the
// ingestion engine. It creates ZERO goroutines of its own (a passive channel), so it can never leak one.
// On sustained overflow it does NOT silently drop events: Enqueue returns a QUEUE_OVERFLOW-coded error and
// latches an "overflow → resync required" flag so the worker can drive the sync axis to RESYNC_REQUIRED and
// request a full resynchronization rather than proceed with a gap.
type BoundedQueue struct {
	ch             chan Event
	done           chan struct{}
	enqueueTimeout time.Duration

	mu             sync.Mutex
	closed         bool
	overflowResync bool
}

// NewBoundedQueue creates a queue with the given capacity (>=1) and per-enqueue back-pressure timeout
// (>0). Zero/negative values are clamped to safe defaults.
func NewBoundedQueue(capacity int, enqueueTimeout time.Duration) *BoundedQueue {
	if capacity < 1 {
		capacity = 1
	}
	if enqueueTimeout <= 0 {
		enqueueTimeout = 2 * time.Second
	}
	return &BoundedQueue{
		ch:             make(chan Event, capacity),
		done:           make(chan struct{}),
		enqueueTimeout: enqueueTimeout,
	}
}

// Cap reports the configured capacity.
func (q *BoundedQueue) Cap() int { return cap(q.ch) }

// Len reports the current number of buffered events.
func (q *BoundedQueue) Len() int { return len(q.ch) }

// Enqueue adds an event, applying bounded back-pressure. Ordering of outcomes:
//   - closed queue                      -> ErrQueueClosed
//   - space available (now or within timeout) -> nil
//   - ctx cancelled while waiting        -> CONTEXT_CANCELED-coded error
//   - queue closed while waiting         -> ErrQueueClosed
//   - still full after the timeout       -> QUEUE_OVERFLOW-coded error + latch overflowResync (no drop)
func (q *BoundedQueue) Enqueue(ctx context.Context, ev Event) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	q.mu.Unlock()

	// fast path
	select {
	case q.ch <- ev:
		return nil
	default:
	}
	// back-pressure path
	t := time.NewTimer(q.enqueueTimeout)
	defer t.Stop()
	select {
	case q.ch <- ev:
		return nil
	case <-ctx.Done():
		return coded(CodeContextCanceled, ctx.Err())
	case <-q.done:
		return ErrQueueClosed
	case <-t.C:
		q.mu.Lock()
		q.overflowResync = true
		q.mu.Unlock()
		return coded(CodeQueueOverflow, nil)
	}
}

// Dequeue returns the next event, blocking until one is available, the ctx is cancelled, or the queue is
// closed AND drained. A closed-but-non-empty queue keeps returning buffered events until empty.
func (q *BoundedQueue) Dequeue(ctx context.Context) (Event, error) {
	select {
	case ev := <-q.ch:
		return ev, nil
	default:
	}
	select {
	case ev := <-q.ch:
		return ev, nil
	case <-ctx.Done():
		return Event{}, coded(CodeContextCanceled, ctx.Err())
	case <-q.done:
		// drain any remaining buffered events before reporting closed
		select {
		case ev := <-q.ch:
			return ev, nil
		default:
			return Event{}, ErrQueueClosed
		}
	}
}

// OverflowResyncRequested reports whether a sustained overflow has latched a resync requirement.
func (q *BoundedQueue) OverflowResyncRequested() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.overflowResync
}

// ClearOverflowResync resets the latch after the worker has driven a resync.
func (q *BoundedQueue) ClearOverflowResync() {
	q.mu.Lock()
	q.overflowResync = false
	q.mu.Unlock()
}

// Close idempotently closes the queue. Buffered events remain drainable via Dequeue until empty.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.done)
}
