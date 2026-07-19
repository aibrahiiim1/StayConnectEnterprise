package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Event is one NORMALIZED, typed protocol observation handed from the read-only adapter to the (future,
// Increment-4) Stay ingestion engine. It deliberately carries ONLY the typed fields ingestion needs —
// never the raw STX/ETX frame bytes, which stay inside the protocol adapter boundary and are never logged,
// queued, persisted unredacted, returned through errors, or exported.
type Event struct {
	// provenance (identity only — never secret material). RuntimeGeneration/ResyncGeneration are stamped by
	// the owning worker at admission time (the adapter does not know them); they bind the durable inbox row
	// to the exact ownership + resync cycle that produced it.
	InterfaceID        string
	RevisionID         string
	SecretGenerationID string
	NormalizationVer   int
	RuntimeGeneration  int64
	ResyncGeneration   int64

	// record classification
	RecordType RecordType // closed enum (domain vs control)

	// idempotency: keyed-HMAC fingerprint over the CANONICAL complete source record (purpose
	// PMS_EVENT_IDENTITY). ExternalEventIdentity carries the same value (the durable stay_events identity).
	//
	// StayResolutionCandidate is a NON-AUTHORITATIVE hint only: it is NOT unique, NEVER a database identity,
	// NEVER a lifecycle/episode decision, and NEVER sufficient to apply a Stay mutation. The authoritative
	// Stay / lifecycle_version / Room / Folio resolution is done TRANSACTIONALLY by the Increment-4 Stay
	// engine (existing Stay vs Room Move vs new lifecycle vs Reinstatement vs Manual Review). The connector
	// must not decide Stay identity — arrival evidence can be corrected within the same Stay, and the
	// connector has no authoritative lifecycle_version.
	SourceEventFingerprint  string
	FingerprintKeyVersion   int
	ExternalEventIdentity   string
	StayResolutionCandidate string

	// timestamps: Arrival/Departure are Stay dates (NOT event time); ReceivedAt is the local receipt clock;
	// PMSEvent* is populated ONLY from a verified FIAS event-timestamp field parsed under the pinned tz.
	ArrivalRaw           string
	DepartureRaw         string
	PMSEventTimestampRaw string
	PMSEventAt           *time.Time
	NormalizedAt         time.Time
	ClockSuspect         bool

	// feed-continuity evidence
	Cursor string

	// typed Stay/Guest/Folio attributes (authoritative Protel map: RN=room, G#=reservation, GN/GF=last/first).
	// No derived display name is carried here: concatenating two individually-valid names could exceed a
	// single-field bound and wrongly fail an otherwise-valid record, and a display string is a presentation
	// concern. Increment 4 / presentation derives display text from the original validated first/last.
	ReservationRef string
	RoomNumber     string
	FolioRef       string
	GuestLastName  string
	GuestFirstName string

	// keyed-HMAC provenance digest of the source evidence (never the raw frame); key is never stored here
	SourceEvidenceHash string
	EvidenceKeyVersion int

	ReceivedAt time.Time
}

// ErrQueueClosed is returned by Enqueue after Close, and by Dequeue once closed AND drained.
var ErrQueueClosed = errors.New("pmsd: event queue closed")

// BoundedQueue is a fixed-capacity, back-pressured, LINEARIZABLE event queue between the protocol adapter
// and the ingestion engine. It creates ZERO goroutines of its own (a passive channel), so it can never
// leak one.
//
// Close/Enqueue linearizability: Enqueue holds a read-lock for the whole send, and Close takes the write
// lock; therefore `closed` cannot flip while any Enqueue is mid-send. Once Close() RETURNS, every later
// Enqueue observes closed and returns ErrQueueClosed — no Event is ever accepted after the close boundary,
// and there is no send-on-closed-channel panic (only `done` is closed, never `ch`).
//
// On sustained overflow it does NOT silently drop: Enqueue returns a QUEUE_OVERFLOW-coded error and latches
// an "overflow → resync required" flag so the worker can drive continuity→GAP_DETECTED and sync→
// RESYNC_REQUIRED instead of proceeding with a gap.
type BoundedQueue struct {
	ch             chan Event
	done           chan struct{}
	enqueueTimeout time.Duration

	mu     sync.RWMutex // RLock during a send; Lock (exclusive) during Close — makes close/enqueue linearizable
	closed bool

	ovMu           sync.Mutex
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

func (q *BoundedQueue) Cap() int { return cap(q.ch) }
func (q *BoundedQueue) Len() int { return len(q.ch) }

// Enqueue adds an event with bounded back-pressure. Outcome ordering:
//   - closed queue                              -> ErrQueueClosed
//   - space available (now or within timeout)   -> nil (Event accepted)
//   - ctx cancelled while waiting               -> CONTEXT_CANCELED-coded error
//   - still full after the timeout              -> QUEUE_OVERFLOW-coded error + latch overflowResync (no drop)
//
// The read-lock is held for the whole call so Close (write-lock) cannot interleave: an in-flight Enqueue
// either commits before Close proceeds, or — if it is waiting on back-pressure — is bounded by the timeout,
// after which Close proceeds and subsequent Enqueues see closed.
func (q *BoundedQueue) Enqueue(ctx context.Context, ev Event) error {
	// reject an invalid typed Event BEFORE it can consume queue capacity
	if err := ev.Validate(); err != nil {
		return coded(CodeEventInvalid, err)
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return ErrQueueClosed
	}
	// fast path
	select {
	case q.ch <- ev:
		return nil
	default:
	}
	// back-pressure path (bounded; closed cannot flip while we hold RLock)
	t := time.NewTimer(q.enqueueTimeout)
	defer t.Stop()
	select {
	case q.ch <- ev:
		return nil
	case <-ctx.Done():
		return coded(CodeContextCanceled, ctx.Err())
	case <-t.C:
		q.ovMu.Lock()
		q.overflowResync = true
		q.ovMu.Unlock()
		return coded(CodeQueueOverflow, nil)
	}
}

// Dequeue returns the next event, blocking until one is available, ctx is cancelled, or the queue is closed
// AND drained. A closed-but-non-empty queue keeps returning buffered events until empty.
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
		select {
		case ev := <-q.ch:
			return ev, nil
		default:
			return Event{}, ErrQueueClosed
		}
	}
}

func (q *BoundedQueue) OverflowResyncRequested() bool {
	q.ovMu.Lock()
	defer q.ovMu.Unlock()
	return q.overflowResync
}

func (q *BoundedQueue) ClearOverflowResync() {
	q.ovMu.Lock()
	q.overflowResync = false
	q.ovMu.Unlock()
}

// Close idempotently closes the queue. It takes the exclusive lock, so it waits for every in-flight Enqueue
// to finish and guarantees no Enqueue can accept an Event afterward. Buffered events remain drainable via
// Dequeue until empty.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.done)
}
