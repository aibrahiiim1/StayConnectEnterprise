package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Event is a normalized, typed observation. The raw STX/ETX frame never leaves the adapter: it is never
// logged, queued, persisted, returned in errors or exported.
type Event struct {
	// Provenance, identity only. RuntimeGeneration/ResyncGeneration are stamped by the worker at admission and
	// bind the inbox row to the ownership and resync cycle that produced it.
	InterfaceID        string
	RevisionID         string
	SecretGenerationID string
	NormalizationVer   int
	RuntimeGeneration  int64
	ResyncGeneration   int64

	RecordType RecordType

	// Idempotency: keyed-HMAC fingerprint over the canonical source record; ExternalEventIdentity carries the
	// same value. StayResolutionCandidate is a non-authoritative hint (not unique, not a database identity,
	// never enough to apply a Stay mutation): the Stay engine resolves identity transactionally, since
	// arrival evidence can be corrected within a Stay and the connector has no lifecycle_version.
	SourceEventFingerprint  string
	FingerprintKeyVersion   int
	ExternalEventIdentity   string
	StayResolutionCandidate string

	// Arrival/Departure are Stay dates, not event time; PMSEvent* comes only from a verified FIAS timestamp
	// field parsed under the pinned timezone.
	ArrivalRaw           string
	DepartureRaw         string
	PMSEventTimestampRaw string
	PMSEventAt           *time.Time
	NormalizedAt         time.Time
	ClockSuspect         bool

	Cursor string

	// Protel map: RN=room, G#=reservation, GN/GF=last/first. No display name here: concatenating two valid
	// names could exceed a single-field bound and wrongly fail the record.
	ReservationRef string
	RoomNumber     string
	FolioRef       string
	GuestLastName  string
	GuestFirstName string

	// FIAS A0 travel agent, A1 VIP. nil = not stated (leave the stay's value alone); a stated empty travel agent clears it.
	TravelAgent *string
	VIP         *bool

	// Sharers is set only by REST connectors; the FIAS adapter never sets it, keeping FIAS payloads byte-identical.
	Sharers []EventSharer

	// Keyed-HMAC digest of the source evidence; the key is never stored here.
	SourceEvidenceHash string
	EvidenceKeyVersion int

	ReceivedAt time.Time
}

var ErrQueueClosed = errors.New("pmsd: event queue closed")

// BoundedQueue is a back-pressured, linearizable event queue with no goroutines of its own.
//
// Enqueue holds the read-lock for the whole send and Close takes the write lock, so no Event is accepted
// after Close returns and there is no send-on-closed-channel panic (only `done` is closed, never `ch`).
//
// Overflow never silently drops: Enqueue returns QUEUE_OVERFLOW and latches a resync-required flag so the
// worker drives GAP_DETECTED/RESYNC_REQUIRED.
type BoundedQueue struct {
	ch             chan Event
	done           chan struct{}
	enqueueTimeout time.Duration

	mu     sync.RWMutex // RLock during a send, Lock during Close
	closed bool

	ovMu           sync.Mutex
	overflowResync bool
}

// NewBoundedQueue clamps a capacity below 1 and a non-positive timeout to safe defaults.
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

// Enqueue waits up to the timeout for space, then returns QUEUE_OVERFLOW and latches overflowResync (no
// drop). The read-lock spans the call, so Close waits for it, bounded by that timeout.
func (q *BoundedQueue) Enqueue(ctx context.Context, ev Event) error {
	// Reject before it can consume capacity.
	if err := ev.Validate(); err != nil {
		return coded(CodeEventInvalid, err)
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return ErrQueueClosed
	}
	select {
	case q.ch <- ev:
		return nil
	default:
	}
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

// Dequeue keeps returning buffered events after Close until the queue is drained.
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

// Close is idempotent and waits for in-flight Enqueues; buffered events stay drainable.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.done)
}
