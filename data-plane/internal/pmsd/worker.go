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
	// A Secret Generation is required + validated ONLY for AUTH_KEY. A NONE (no-auth, e.g. Protel FIAS)
	// connector must have NO Secret Generation (rev.Validate already rejects a NONE carrying one).
	if rev.RequiresSecret() && (sg.ID == "" || sg.Superseded || sg.ID != rev.ActiveSecretGenerationID) {
		logEvent(w.deps.log(), EventWorkerSecretMissing, CodeSecretMissing, sf(StageSecret))
		return 0, coded(CodeSecretMissing, nil)
	}
	w.iface = iface

	// atomic generation allocation (pins revision + credential mode + secret when AUTH_KEY); a stale owner is
	// rejected here
	gen, err := w.repo.AllocateRuntimeGeneration(ctx, GenerationRequest{
		TenantID: iface.TenantID, SiteID: iface.SiteID, PMSInterfaceID: iface.ID,
		PinnedRevisionID: rev.ID, PinnedSecretGenerationID: sg.ID, CredentialMode: rev.CredentialMode,
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

	// decrypt the selected secret ONLY after ownership + generation, and ONLY for AUTH_KEY; zero it right
	// after dial. A NONE connector decrypts nothing and dials with no secret material.
	var secret SecretMaterial
	if rev.RequiresSecret() {
		secret, err = w.deps.DecryptSecret(ctx, iface, rev, sg)
		if err != nil {
			logEvent(w.deps.log(), EventWorkerSecretDecrypt, Classify(err), sf(StageSecret))
			_ = w.repo.UpdateTransport(ctx, w.disc(gen, CodeSecretDecryptFailed))
			return 0, err
		}
	}

	conn, dialErr := w.deps.Dial(ctx, DialParams{Iface: iface, Rev: rev, Secret: secret})
	secret.Zero() // never retained beyond dial (safe no-op for a NONE connector's empty secret)
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

// workerSink translates protocol evidence into INDEPENDENT-axis CAS updates and DURABLE inbox rows. It uses
// the worker (serve) context — never context.Background() — and the pinned generation. Any persist error
// (including stale generation) is returned so the adapter stops serving and the transport closes.
//
// Application barrier (§H) + resync state machine (§G) live here. The fields below are touched ONLY by the
// single Serve goroutine (the adapter calls sink methods serially), so they need no lock:
//   - synced: a complete DS→DE resync generation has been PUBLISHED; until then LIVE admission is barred.
//   - resyncing/resyncGen: inside a DS→DE window, domain records are STAGED under the allocated generation.
//
// The bounded queue is now a best-effort WAKEUP channel only (durable rows are the authoritative store); a
// full queue is benign (a poll backstop covers a dropped wakeup) and never forces a gap.
type workerSink struct {
	w   *worker
	ctx context.Context
	gen int64
	q   *BoundedQueue

	synced    bool
	resyncing bool
	resyncGen int64
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

// RequireInitialResync raises the barrier at connect (§H): sync→RESYNC_REQUIRED, no LIVE admission until a
// complete DS→DE generation is published.
func (s *workerSink) RequireInitialResync(at time.Time) error {
	s.synced = false
	s.resyncing = false
	return s.w.repo.UpdateSync(s.ctx, SyncUpdate{axisBase: s.ax(), Status: SyncResyncRequired, ResyncRequestedAt: &at})
}

// OnResyncStart (DS) allocates a NEW typed resync generation under the exact runtime-generation CAS and opens
// the staging window (sync→RESYNC_IN_PROGRESS). Domain records until DE are STAGED under this generation.
func (s *workerSink) OnResyncStart(at time.Time) error {
	g, err := s.w.repo.AllocateResyncGeneration(s.ctx, ResyncScope{s.ax()})
	if err != nil {
		return err
	}
	s.resyncGen = g
	s.resyncing = true
	return s.w.repo.UpdateSync(s.ctx, SyncUpdate{axisBase: s.ax(), Status: SyncResyncInProgress, ResyncStartedAt: &at})
}

// OnResyncComplete (DE) ATOMICALLY publishes the complete resync generation (one runtime-row boundary update
// + IN_SYNC/CONTINUOUS), then lowers the barrier. Nothing is published unless a resync window was open.
func (s *workerSink) OnResyncComplete(at time.Time, _ string) error {
	if !s.resyncing || s.resyncGen == 0 {
		// a DE without a preceding DS publishes nothing (partial/spurious) — leave the barrier up.
		return nil
	}
	if err := s.w.repo.PublishResyncGeneration(s.ctx, ResyncScope{s.ax()}, s.resyncGen); err != nil {
		return err
	}
	s.resyncing = false
	s.synced = true
	return nil
}
func (s *workerSink) OnDisconnected(at time.Time, code Code) error {
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportDisconnected, DisconnectedSince: &at, ErrorCode: code})
}

// OnDomainEvent routes a validated domain Event through the barrier + durable inbox:
//   - inside a DS→DE window  → StageResyncEvent (immutable, under the resync generation; NOT live).
//   - barrier up (not synced, no window) → NOT admitted (a poll/resync will re-establish the roster); the
//     barrier stays up. Zero LIVE admissions occur until a generation is published.
//   - synced → AdmitLiveEvent (durable append-first, ownership-proof + runtime-generation CAS) then record
//     feed continuity. The bounded queue receives a best-effort WAKEUP with the durable row id.
//
// The durable row is the AUTHORITATIVE store; a full wakeup queue is benign (never a gap). A stale owner
// admits/stages nothing (ErrStaleGeneration is returned so the transport closes).
func (s *workerSink) OnDomainEvent(ctx context.Context, ev Event) error {
	row := s.inboxRow(ev)
	switch {
	case s.resyncing:
		row.ResyncGeneration = s.resyncGen
		if _, err := s.w.repo.StageResyncEvent(s.ctx, row); err != nil {
			return err
		}
		return nil
	case !s.synced:
		// §H barrier: no LIVE admission before the initial sync completes. Hold (do not admit); the
		// already-requested resync will produce the authoritative roster.
		return nil
	default:
		if _, err := s.w.repo.AdmitLiveEvent(s.ctx, row); err != nil {
			return err
		}
		return s.w.repo.UpdateContinuity(s.ctx, ContinuityUpdate{axisBase: s.ax(), Status: ContinuityContinuous, LastValidEventAt: ptr(ev.NormalizedAt), LastEventCursor: clip(ev.Cursor, maxCursorLen)})
	}
}

// inboxRow builds the durable, provenance-bound inbox row from a validated Event. It carries only bounded
// typed fields (never the raw frame / secret) as a JSON payload for the Increment-4 Stay engine.
func (s *workerSink) inboxRow(ev Event) InboxRow {
	return InboxRow{
		axisBase:              s.ax(),
		ExternalEventIdentity: ev.ExternalEventIdentity,
		FingerprintKeyVersion: ev.FingerprintKeyVersion,
		EventType:             string(ev.RecordType),
		PMSTimestampRaw:       ev.PMSEventTimestampRaw,
		PMSTimestampUTC:       ev.PMSEventAt,
		ReceivedAt:            nonZeroTime(ev.ReceivedAt, s.w.deps.now()),
		SequenceVersion:       0,
		NormalizationVersion:  ev.NormalizationVer,
		ClockSuspect:          ev.ClockSuspect,
		Payload:               eventPayloadJSON(ev),
	}
}

// OnDomainEvent's continuity-gap counterpart for records the adapter could not admit.

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
