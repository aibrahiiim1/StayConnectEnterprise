package pmsd

import (
	"context"
	"errors"
	"time"
)

// worker owns exactly one PMS Interface. Its runtime generation is allocated ATOMICALLY in PostgreSQL per
// successful ownership; every axis write uses an exact compare-and-set on that generation so a previous
// owner cannot overwrite the current owner's state.
type worker struct {
	iface   Interface
	repo    Repo
	deps    *Deps
	cancel  context.CancelFunc
	done    chan struct{}
	gen     int64
	attempt int
}

func (w *worker) stop(grace time.Duration) {
	w.cancel()
	select {
	case <-w.done:
	case <-time.After(grace):
	}
}

func (w *worker) run(ctx context.Context) {
	bo := newBackoff(w.deps.BackoffMin, w.deps.BackoffMax, w.deps.rnd)
	for {
		if ctx.Err() != nil {
			return
		}
		w.attempt++
		stable, err := w.ownAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil || stable >= w.deps.StableResetAfter {
			bo.reset()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(bo.next()):
		}
	}
}

// ownAndServe performs the strict ordered ownership protocol:
//
//	lock -> re-read Interface/Revision/SecretGeneration -> validate -> allocate generation (atomic, pins
//	revision+secret) -> decrypt secret -> dial -> serve (per-axis CAS) -> disconnect-persist.
//
// A lock loser opens NO socket and NEVER loads ciphertext / decrypts / dials. On lock loss the transport is
// closed and serving stops.
func (w *worker) ownAndServe(ctx context.Context) (time.Duration, error) {
	sf := func(st Stage) SafeFields {
		return SafeFields{InterfaceID: NewUUIDValue(w.iface.ID), Generation: w.gen, Stage: st, Attempt: w.attempt}
	}

	locker, err := w.deps.NewLocker(ctx)
	if err != nil {
		logEvent(w.deps.log(), EventWorkerLockFailed, Classify(err), sf(StageLock))
		return 0, err
	}
	defer func() { _ = locker.Close() }()

	lockKey, err := LockKey(w.iface.TenantID, w.iface.SiteID, w.iface.ID)
	if err != nil {
		logEvent(w.deps.log(), EventWorkerLockFailed, Classify(err), sf(StageLock))
		return 0, err
	}
	got, err := locker.TryLock(ctx, lockKey)
	if err != nil {
		logEvent(w.deps.log(), EventWorkerLockFailed, Classify(err), sf(StageLock))
		return 0, err
	}
	if !got {
		return 0, coded(CodeLockNotAcquired, nil) // competing owner: NO socket, NO secret
	}

	// re-read AFTER ownership (lifecycle/revision/secret may have changed between discovery and lock)
	iface, rev, sg, err := w.repo.LoadInterface(ctx, w.iface.TenantID, w.iface.SiteID, w.iface.ID)
	if err != nil {
		logEvent(w.deps.log(), EventWorkerNotDialable, Classify(err), sf(StageReRead))
		return 0, err
	}
	if iface.LifecycleState != "ACTIVE" {
		logEvent(w.deps.log(), EventWorkerNotDialable, CodeInterfaceDisabled, sf(StageReRead))
		return 0, coded(CodeInterfaceDisabled, nil)
	}
	if err := rev.Validate(); err != nil {
		logEvent(w.deps.log(), EventWorkerRevisionInvalid, Classify(err), sf(StageReRead))
		return 0, err
	}
	if sg.ID == "" || sg.Superseded || sg.ID != rev.ActiveSecretGenerationID {
		logEvent(w.deps.log(), EventWorkerSecretMissing, CodeSecretMissing, sf(StageSecret))
		return 0, coded(CodeSecretMissing, nil)
	}
	w.iface = iface

	// atomic generation allocation (pins revision + secret); a stale owner is rejected here
	gen, err := w.repo.AllocateRuntimeGeneration(ctx, GenerationRequest{
		TenantID: iface.TenantID, SiteID: iface.SiteID, PMSInterfaceID: iface.ID,
		PinnedRevisionID: rev.ID, PinnedSecretGenerationID: sg.ID,
	})
	if err != nil {
		code := Classify(err)
		if errors.Is(err, ErrStaleGeneration) {
			code = CodeRuntimeGenStale
		}
		logEvent(w.deps.log(), EventWorkerGenerationStale, code, sf(StageAllocate))
		return 0, err
	}
	w.gen = gen

	// startup transport axis: CONNECTING (never fabricate heartbeat/complete-sync/HEALTHY)
	if err := w.repo.UpdateTransport(ctx, TransportUpdate{
		axisBase: w.ax(gen), Status: TransportConnecting, LastConnectAttemptAt: ptr(w.deps.now()),
	}); err != nil {
		logEvent(w.deps.log(), EventWorkerPersistFailed, Classify(err), sf(StagePersist))
		return 0, err
	}

	// decrypt the selected secret ONLY after ownership + generation; zero it right after dial
	secret, err := w.deps.DecryptSecret(ctx, iface, rev, sg)
	if err != nil {
		logEvent(w.deps.log(), EventWorkerSecretDecrypt, Classify(err), sf(StageSecret))
		_ = w.repo.UpdateTransport(ctx, w.disc(gen, CodeSecretDecryptFailed))
		return 0, err
	}

	conn, dialErr := w.deps.Dial(ctx, DialParams{Iface: iface, Rev: rev, Secret: secret})
	secret.Zero() // never retained beyond dial
	if dialErr != nil {
		logEvent(w.deps.log(), EventWorkerDialFailed, Classify(dialErr), sf(StageDial))
		_ = w.repo.UpdateTransport(ctx, w.disc(gen, CodeDialFailed))
		return 0, dialErr
	}
	defer func() { _ = conn.Close() }()

	// cancel serving immediately on lock loss (ownership gone)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	go func() {
		select {
		case <-locker.Lost():
			logEvent(w.deps.log(), EventWorkerLockLost, CodeLockSessionLost, sf(StageServe))
			scancel()
			_ = conn.Close()
		case <-sctx.Done():
		}
	}()

	// per-ownership bounded typed queue (owned + closed here)
	q := NewBoundedQueue(w.deps.QueueCapacity, w.deps.QueueEnqueueTimeout)
	defer q.Close()
	sink := &workerSink{w: w, ctx: sctx, gen: gen, q: q}

	start := w.deps.now()
	serr := conn.Serve(sctx, sink)
	code := CodeProtocolLinkEnded
	if serr != nil {
		code = Classify(serr)
	}
	logEvent(w.deps.log(), EventWorkerProtocolEnded, code, sf(StageServe))
	_ = w.repo.UpdateTransport(ctx, w.disc(gen, code))
	return w.deps.now().Sub(start), serr
}

func (w *worker) ax(gen int64) axisBase {
	return axisBase{TenantID: w.iface.TenantID, SiteID: w.iface.SiteID, PMSInterfaceID: w.iface.ID, ExpectedGeneration: gen, At: w.deps.now()}
}
func (w *worker) disc(gen int64, code Code) TransportUpdate {
	now := w.deps.now()
	return TransportUpdate{axisBase: w.ax(gen), Status: TransportDisconnected, DisconnectedSince: &now, ErrorCode: code}
}

func ptr(t time.Time) *time.Time { return &t }

// workerSink translates protocol evidence into INDEPENDENT-axis CAS updates and typed queue events. It uses
// the worker (serve) context — never context.Background() — and the pinned generation. Any persist error
// (including stale generation) is returned so the adapter stops serving and the transport closes.
type workerSink struct {
	w   *worker
	ctx context.Context
	gen int64
	q   *BoundedQueue
}

func (s *workerSink) ax() axisBase {
	return axisBase{TenantID: s.w.iface.TenantID, SiteID: s.w.iface.SiteID, PMSInterfaceID: s.w.iface.ID, ExpectedGeneration: s.gen, At: s.w.deps.now()}
}

func (s *workerSink) OnConnected(at time.Time) error {
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportConnected, LastConnectedAt: &at})
}
func (s *workerSink) OnHeartbeat(at time.Time) error {
	// transport axis ONLY — must not erase continuity/sync evidence
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportConnected, LastHeartbeatAt: &at})
}
func (s *workerSink) OnResyncStart(at time.Time) error {
	return s.w.repo.UpdateSync(s.ctx, SyncUpdate{axisBase: s.ax(), Status: SyncResyncInProgress, ResyncStartedAt: &at})
}
func (s *workerSink) OnResyncComplete(at time.Time, cursor string) error {
	if err := s.w.repo.UpdateSync(s.ctx, SyncUpdate{axisBase: s.ax(), Status: SyncInSync, LastCompleteSyncAt: &at, SyncCursor: clip(cursor, maxCursorLen)}); err != nil {
		return err
	}
	// a verified complete resync restores feed continuity
	return s.w.repo.UpdateContinuity(s.ctx, ContinuityUpdate{axisBase: s.ax(), Status: ContinuityContinuous, LastResyncMarkerAt: &at})
}
func (s *workerSink) OnDisconnected(at time.Time, code Code) error {
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportDisconnected, DisconnectedSince: &at, ErrorCode: code})
}

// OnDomainEvent records feed continuity for a valid event, then enqueues the typed event. On queue overflow
// it drives continuity→GAP_DETECTED and sync→RESYNC_REQUIRED and returns QUEUE_OVERFLOW so the adapter
// stops normal application until a verified full resync.
func (s *workerSink) OnDomainEvent(ctx context.Context, ev Event) error {
	if err := s.q.Enqueue(ctx, ev); err != nil {
		if Classify(err) == CodeQueueOverflow {
			// Persist the gap/resync transition ATOMICALLY — a DB/generation failure here must NOT be
			// swallowed: it is returned so the transport closes rather than leaving a silent, unrecorded gap.
			if perr := s.markGapResync(CodeQueueOverflow); perr != nil {
				return perr
			}
		}
		return err
	}
	return s.w.repo.UpdateContinuity(s.ctx, ContinuityUpdate{axisBase: s.ax(), Status: ContinuityContinuous, LastValidEventAt: ptr(ev.NormalizedAt), LastEventCursor: clip(ev.Cursor, maxCursorLen)})
}

// OnContinuityFault durably drives continuity→GAP_DETECTED and sync→RESYNC_REQUIRED for a record the adapter
// could not admit (malformed/overlong/duplicate/failed normalization). Both axes move in ONE transaction under
// the pinned generation; a failure is returned so the adapter closes the transport instead of continuing over
// an unrecorded gap.
func (s *workerSink) OnContinuityFault(ctx context.Context, code Code) error {
	return s.markGapResync(code)
}

// markGapResync performs the ATOMIC two-axis continuity-gap + resync-required transition, tagging it with a
// bounded typed reason code. It returns ErrStaleGeneration if ownership changed and any DB error otherwise.
func (s *workerSink) markGapResync(reason Code) error {
	return s.w.repo.MarkGapAndRequireResync(s.ctx, GapResyncRequest{axisBase: s.ax(), Reason: reason})
}

// QueueForTest exposes the per-ownership queue for integration tests only.
func (s *workerSink) queue() *BoundedQueue { return s.q }

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (d *Deps) rnd(n int64) int64 {
	if d.jitter != nil {
		return d.jitter(n)
	}
	return time.Now().UnixNano()
}
