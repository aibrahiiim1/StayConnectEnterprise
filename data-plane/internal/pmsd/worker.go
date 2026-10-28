package pmsd

import (
	"context"
	"errors"
	"time"
)

// worker owns one PMS Interface; every axis write is a CAS on its runtime generation so a previous owner
// cannot overwrite the current owner's state.
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
	// Bounds are re-read every cycle so operator changes apply on the next reconnect. An unreadable setting
	// falls back to compiled defaults (warned once): config problems must never stop reconnecting.
	settings := ConnectionSettings{
		BackoffMin: w.deps.BackoffMin, BackoffMax: w.deps.BackoffMax,
		StableResetAfter: w.deps.StableResetAfter,
	}
	bo := newBackoff(settings.BackoffMin, settings.BackoffMax, w.deps.rnd)
	warned := false
	for {
		if ctx.Err() != nil {
			return
		}
		if s, err := w.repo.LoadConnectionSettings(ctx, w.iface.TenantID, w.iface.SiteID, w.iface.ID); err == nil {
			if s != settings {
				settings = s
				bo = newBackoff(settings.BackoffMin, settings.BackoffMax, w.deps.rnd)
			}
			warned = false
		} else if !warned {
			warned = true
			w.deps.log().Warn("pmsd: reconnect bounds unreadable; using compiled defaults",
				append(safeErrAttrs(err), "interface", w.iface.ID)...)
		}
		w.attempt++
		stable, err := w.ownAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil || stable >= settings.StableResetAfter {
			bo.reset()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(bo.next()):
		}
	}
}

// ownAndServe order matters: lock, re-read, validate, allocate generation, decrypt, dial, serve. A lock loser
// opens no socket and never decrypts or dials; on lock loss the transport closes and serving stops.
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
		return 0, coded(CodeLockNotAcquired, nil)
	}

	// Re-read after ownership: state may have changed between discovery and lock.
	iface, rev, sg, err := w.repo.LoadInterface(ctx, w.iface.TenantID, w.iface.SiteID, w.iface.ID)
	if err != nil {
		logEvent(w.deps.log(), EventWorkerNotDialable, Classify(err), sf(StageReRead))
		return 0, err
	}
	if !workerLifecycle(iface.LifecycleState) {
		logEvent(w.deps.log(), EventWorkerNotDialable, CodeInterfaceDisabled, sf(StageReRead))
		return 0, coded(CodeInterfaceDisabled, nil)
	}
	if err := rev.Validate(); err != nil {
		logEvent(w.deps.log(), EventWorkerRevisionInvalid, Classify(err), sf(StageReRead))
		return 0, err
	}
	if rev.RequiresSecret() && (sg.ID == "" || sg.Superseded || sg.ID != rev.ActiveSecretGenerationID) {
		logEvent(w.deps.log(), EventWorkerSecretMissing, CodeSecretMissing, sf(StageSecret))
		return 0, coded(CodeSecretMissing, nil)
	}
	w.iface = iface

	// A stale owner is rejected here.
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

	// Startup is CONNECTING; never fabricate heartbeat, complete-sync or healthy.
	if err := w.repo.UpdateTransport(ctx, TransportUpdate{
		axisBase: w.ax(gen), Status: TransportConnecting, LastConnectAttemptAt: ptr(w.deps.now()),
	}); err != nil {
		logEvent(w.deps.log(), EventWorkerPersistFailed, Classify(err), sf(StagePersist))
		return 0, err
	}

	// Decrypt only after ownership and generation, only for AUTH_KEY; zeroed right after dial.
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
	secret.Zero()
	if dialErr != nil {
		logEvent(w.deps.log(), EventWorkerDialFailed, Classify(dialErr), sf(StageDial))
		_ = w.repo.UpdateTransport(ctx, w.disc(gen, CodeDialFailed))
		return 0, dialErr
	}
	defer func() { _ = conn.Close() }()

	// Cancel serving immediately on lock loss.
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

	q := NewBoundedQueue(w.deps.QueueCapacity, w.deps.QueueEnqueueTimeout)
	defer q.Close()
	sink := &workerSink{w: w, ctx: sctx, gen: gen, q: q}

	start := w.deps.now()
	serr := conn.Serve(sctx, sink)
	code := CodeProtocolLinkEnded
	if serr != nil {
		code = Classify(serr)
	}
	ending := endingDetail{err: serr, publishedAt: sink.publishedAt}
	if d, ok := conn.(linkDiagnoser); ok {
		g := d.linkDiagnostics()
		ending.diag = &g
	}
	logProtocolEnded(w.deps.log(), code, sf(StageServe), ending)
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

// workerSink uses the serve context (never Background) and the pinned generation; any persist error,
// including a stale generation, is returned so the adapter stops and the transport closes.
//
// The fields below are touched only by the single Serve goroutine (no lock):
//   - synced: a complete DS→DE generation is published; until then LIVE admission is barred.
//   - resyncing/resyncGen: inside a DS→DE window, records are staged under the allocated generation.
//
// The queue is a best-effort wakeup only (durable rows are authoritative); a full queue is benign, never a gap.
type workerSink struct {
	w   *worker
	ctx context.Context
	gen int64
	q   *BoundedQueue

	synced    bool
	resyncing bool
	resyncGen int64

	// publishedAt is when this link last published a resync generation (wall clock, diagnostics only).
	publishedAt time.Time

	// received counts records staged under the open generation; reset at DS so the number describes this roster.
	received int64
	// adapterSkipped is the adapter's last per-roster skipped total; dupSkipped counts records repeated inside the
	// open window (staged once). Both reset at DS.
	adapterSkipped int64
	dupSkipped     int64
	// stageBuf holds RESYNC rows not yet written when the repository stages in batches (D6-M2).
	stageBuf []InboxRow

	// Coverage of the sweep in flight, held until the publish succeeds: an unpublished sweep never
	// produced an authoritative roster.
	covSeen      bool
	covRooms     []string
	covRoster    int
	covVacant    int
	covConflicts int
}

// stage swallows write failures: progress reporting must never abort a resync.
func (s *workerSink) stage(st SyncStage, u StageUpdate) {
	u.axisBase = s.ax()
	u.Stage = st
	_ = s.w.repo.UpdateSyncStage(s.ctx, u)
}

func (s *workerSink) ax() axisBase {
	return axisBase{TenantID: s.w.iface.TenantID, SiteID: s.w.iface.SiteID, PMSInterfaceID: s.w.iface.ID, ExpectedGeneration: s.gen, At: s.w.deps.now()}
}

func (s *workerSink) OnConnected(at time.Time) error {
	// Without this line a recovered PMS looks like a dead one.
	logProgress(s.w.deps.log(), EventWorkerConnected, SafeFields{
		InterfaceID: NewUUIDValue(s.w.iface.ID), Generation: s.gen, Stage: StageServe})
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportConnected, LastConnectedAt: &at})
}
func (s *workerSink) OnHeartbeat(at time.Time) error {
	// Transport axis only; must not erase continuity/sync evidence.
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportConnected, LastHeartbeatAt: &at})
}

func (s *workerSink) RequireInitialResync(at time.Time) error {
	s.synced = false
	s.resyncing = false
	s.adapterSkipped, s.dupSkipped = 0, 0
	s.stageBuf = s.stageBuf[:0]
	// The automatic sync uses the same stage model as an operator's. Counters are zeroed here, not at DS, so
	// the previous roster's totals aren't shown between request and receipt.
	zero := int64(0)
	s.stage(StageRequesting, StageUpdate{RecordsReceived: &zero, RecordsSkipped: &zero})
	logProgress(s.w.deps.log(), EventWorkerResyncRequested, SafeFields{
		InterfaceID: NewUUIDValue(s.w.iface.ID), Generation: s.gen, Stage: StageServe})
	return s.w.repo.UpdateSync(s.ctx, SyncUpdate{axisBase: s.ax(), Status: SyncResyncRequired, ResyncRequestedAt: &at})
}

func (s *workerSink) OnFullSyncRequested() {
	s.stage(StageWaiting, StageUpdate{})
}

func (s *workerSink) OnResyncStart(at time.Time) error {
	g, err := s.w.repo.AllocateResyncGeneration(s.ctx, ResyncScope{s.ax()})
	if err != nil {
		return err
	}
	s.resyncGen = g
	s.resyncing = true
	s.received = 0
	s.adapterSkipped, s.dupSkipped = 0, 0
	s.stageBuf = s.stageBuf[:0]
	zero := int64(0)
	s.stage(StageReceiving, StageUpdate{RecordsReceived: &zero})
	return s.w.repo.UpdateSync(s.ctx, SyncUpdate{axisBase: s.ax(), Status: SyncResyncInProgress, ResyncStartedAt: &at})
}

// RecordCoverage is held until the publish so an interrupted sweep leaves no coverage row for a roster that
// never became authoritative.
func (s *workerSink) RecordCoverage(rooms []string, rosterRecords, vacantRecords, conflictingRooms int) {
	s.covRooms, s.covRoster, s.covVacant = rooms, rosterRecords, vacantRecords
	s.covConflicts = conflictingRooms
	s.covSeen = true
}

// OnResyncComplete publishes the generation atomically and lowers the barrier; nothing is published unless a window was open.
func (s *workerSink) OnResyncComplete(at time.Time, _ string) error {
	if !s.resyncing || s.resyncGen == 0 {
		// A DE without a DS is spurious: leave the barrier up.
		return nil
	}
	// Every buffered record is durable before the generation can be published.
	if err := s.flushStaged(); err != nil {
		return err
	}
	// Written before the publish so a slow publish shows its final step, not RECEIVING.
	s.stage(StagePublishing, StageUpdate{})
	if err := s.w.repo.PublishResyncGeneration(s.ctx, ResyncScope{s.ax()}, s.resyncGen); err != nil {
		// Not published: the previous mirror stays live (stale, never partial).
		s.stage(StageFailed, StageUpdate{FailureCode: "PUBLISH_FAILED"})
		return err
	}
	s.resyncing = false
	s.synced = true
	s.publishedAt = time.Now()
	// The generation is now claimable (D6-H1).
	s.w.deps.wake.notify(s.w.iface.ID)
	// Coverage first, then reconcile, both after the publish. Neither can fail the sync (guests already
	// authenticate against the published roster): a problem is logged and the next complete sweep retries.
	if s.covSeen {
		if err := s.w.repo.RecordResyncCoverage(s.ctx, ResyncScope{s.ax()}, s.resyncGen,
			s.covRooms, s.covRoster, s.covVacant, s.covConflicts); err != nil {
			s.w.deps.log().Warn("pmsd: could not record what the sweep observed; reconciliation will defer",
				append(safeErrAttrs(err), "generation", s.resyncGen)...)
		}
		s.covSeen = false
	}
	if out, err := s.w.repo.ReconcileRoster(s.ctx, ResyncScope{s.ax()}, s.resyncGen); err != nil {
		s.w.deps.log().Warn("pmsd: automatic reconciliation could not run; the next complete sweep retries",
			append(safeErrAttrs(err), "generation", s.resyncGen)...)
	} else {
		// Check-outs the reconciliation queued are claimable too.
		s.w.deps.wake.notify(s.w.iface.ID)
		s.w.deps.log().Info("pmsd: automatic roster reconciliation",
			"generation", s.resyncGen, "outcome", out.Outcome, "stays_closed", out.StaysClosed,
			"absent_from_roster", out.AbsentFromRoster, "protected", out.Protected,
			"rooms_enumerated", out.RoomsEnumerated, "rooms_expected", out.RoomsExpected)
	}
	got := s.received
	s.stage(StageComplete, StageUpdate{RecordsReceived: &got})
	// The record count is a magnitude, never which records.
	logProgress(s.w.deps.log(), EventWorkerResyncPublished, SafeFields{
		InterfaceID: NewUUIDValue(s.w.iface.ID), Generation: s.resyncGen, Stage: StageServe, Records: got})
	return nil
}
func (s *workerSink) OnDisconnected(at time.Time, code Code) error {
	switch {
	case s.resyncing:
		// Lost mid-roster: nothing published, the last known-good mirror is untouched (INTERRUPTED, not FAILED).
		s.stage(StageInterrupted, StageUpdate{FailureCode: code.String()})
		s.resyncing = false
		s.stageBuf = s.stageBuf[:0] // never written; the generation is abandoned unpublished
	case !s.synced:
		// Lost before DS: the sync never started, so FAILED with the transport code, not INTERRUPTED.
		s.stage(StageFailed, StageUpdate{FailureCode: code.String()})
	}
	return s.w.repo.UpdateTransport(s.ctx, TransportUpdate{axisBase: s.ax(), Status: TransportDisconnected, DisconnectedSince: &at, ErrorCode: code})
}

// OnDomainEvent routes through the barrier:
//   - inside a DS→DE window: staged under the resync generation (not live);
//   - barrier up (not synced, no window): not admitted, the requested resync re-establishes the roster;
//   - synced: AdmitLiveEvent, then record continuity.
//
// A stale owner admits nothing and gets ErrStaleGeneration so the transport closes.
func (s *workerSink) OnDomainEvent(ctx context.Context, ev Event) error {
	row := s.inboxRow(ev)
	switch {
	case s.resyncing:
		row.ResyncGeneration = s.resyncGen
		if _, ok := s.w.repo.(resyncBatchStager); ok {
			// Buffered and written in batches under one ownership proof (D6-M2); flushed at the batch size and
			// before the publish.
			s.stageBuf = append(s.stageBuf, row)
			if len(s.stageBuf) >= stageBatchSize {
				return s.flushStaged()
			}
			return nil
		}
		if _, err := s.w.repo.StageResyncEvent(s.ctx, row); err != nil {
			if errors.Is(err, ErrDuplicateResyncEvent) {
				// The same record twice in one window: staged once, counted as skipped, the window continues.
				s.dupSkipped++
				total := s.adapterSkipped + s.dupSkipped
				s.stage(StageReceiving, StageUpdate{RecordsSkipped: &total})
				s.w.deps.log().Info("pmsd: repeated record inside one resync skipped",
					"interface", NewUUIDValue(s.w.iface.ID).String(), "record_type", safeRecordType(ev.RecordType),
					"duplicates_this_roster", s.dupSkipped)
				return nil
			}
			return failedAt(stepStageResync, err)
		}
		// Counts durably staged records; reported every 25 to avoid thousands of writes (exact count at COMPLETE).
		s.received++
		if s.received%25 == 0 {
			got := s.received
			s.stage(StageReceiving, StageUpdate{RecordsReceived: &got})
		}
		return nil
	case !s.synced:
		// Barrier: no LIVE admission before the initial sync completes.
		return nil
	default:
		cont := ContinuityUpdate{axisBase: s.ax(), Status: ContinuityContinuous, LastValidEventAt: ptr(ev.NormalizedAt), LastEventCursor: clip(ev.Cursor, maxCursorLen)}
		if one, ok := s.w.repo.(liveContinuityAdmitter); ok {
			// Admission and continuity in one transaction (D6-M2 step 3).
			if _, err := one.AdmitLiveEventAndContinuity(s.ctx, row, cont); err != nil {
				var dup *DuplicateLiveEventError
				if errors.As(err, &dup) {
					s.w.deps.log().Info("pmsd: duplicate live record ignored",
						"interface", NewUUIDValue(s.w.iface.ID).String(), "record_type", safeRecordType(ev.RecordType),
						"processing_status", safeProcessingStatus(dup.ProcessingStatus))
					return nil
				}
				return failedAt(stepAdmitLive, err)
			}
			s.w.deps.wake.notify(s.w.iface.ID)
			return nil
		}
		if _, err := s.w.repo.AdmitLiveEvent(s.ctx, row); err != nil {
			var dup *DuplicateLiveEventError
			if errors.As(err, &dup) {
				// An exact repeat carries no new state, so continuity is not advanced either.
				s.w.deps.log().Info("pmsd: duplicate live record ignored",
					"interface", NewUUIDValue(s.w.iface.ID).String(), "record_type", safeRecordType(ev.RecordType),
					"processing_status", safeProcessingStatus(dup.ProcessingStatus))
				return nil
			}
			return failedAt(stepAdmitLive, err)
		}
		// The row is committed and claimable: wake the applier rather than wait for its idle poll (D6-H1).
		s.w.deps.wake.notify(s.w.iface.ID)
		return failedAt(stepRecordContinuity, s.w.repo.UpdateContinuity(s.ctx, cont))
	}
}

// stageBatchSize bounds how many RESYNC rows share one transaction and one ownership proof (D6-M2).
const stageBatchSize = 50

// resyncBatchStager and liveContinuityAdmitter are the batched writes a repository may offer; one that does not
// is driven a row at a time, as before.
type resyncBatchStager interface {
	StageResyncEvents(ctx context.Context, rows []InboxRow) (staged, duplicates int, err error)
}

type liveContinuityAdmitter interface {
	AdmitLiveEventAndContinuity(ctx context.Context, row InboxRow, u ContinuityUpdate) (string, error)
}

// flushStaged writes the buffered RESYNC rows; a failure ends the cycle like a single-row staging failure did.
func (s *workerSink) flushStaged() error {
	if len(s.stageBuf) == 0 {
		return nil
	}
	b, _ := s.w.repo.(resyncBatchStager)
	staged, dups, err := b.StageResyncEvents(s.ctx, s.stageBuf)
	s.stageBuf = s.stageBuf[:0]
	if err != nil {
		return failedAt(stepStageResync, err)
	}
	before := s.received
	s.received += int64(staged)
	if dups > 0 {
		s.dupSkipped += int64(dups)
		total := s.adapterSkipped + s.dupSkipped
		s.stage(StageReceiving, StageUpdate{RecordsSkipped: &total})
		s.w.deps.log().Info("pmsd: repeated records inside one resync skipped",
			"interface", NewUUIDValue(s.w.iface.ID).String(), "duplicates_this_roster", s.dupSkipped)
	}
	if s.received/25 != before/25 {
		got := s.received
		s.stage(StageReceiving, StageUpdate{RecordsReceived: &got})
	}
	return nil
}

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

// OnContinuityFault returns a persist failure so the adapter closes the transport rather than continue over
// an unrecorded gap.
func (s *workerSink) OnContinuityFault(ctx context.Context, code Code) error {
	return s.markGapResync(code)
}

// markGapResync raises the LIVE barrier again (audit P7 L1): after a gap nothing is admitted LIVE, and continuity is
// not set back to CONTINUOUS by a later record, until the requested resync publishes. A window already open keeps
// staging as before.
func (s *workerSink) markGapResync(reason Code) error {
	s.synced = false
	return s.w.repo.MarkGapAndRequireResync(s.ctx, GapResyncRequest{axisBase: s.ax(), Reason: reason})
}

// queue is for integration tests only.
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

// FinancialResyncDue treats any failure as "no": a failed status read must not start a resync.
func (s *workerSink) FinancialResyncDue() bool {
	q, ok := s.w.repo.(interface {
		FinancialResyncDue(ctx context.Context, scope ResyncScope) (bool, error)
	})
	if !ok {
		return false
	}
	due, err := q.FinancialResyncDue(s.ctx, ResyncScope{s.ax()})
	return err == nil && due
}

// ClaimOperatorResync treats a claim failure as "no command": closing a healthy transport over an unreadable
// status table would turn a database hiccup into a guest-facing outage.
func (s *workerSink) ClaimOperatorResync() *ResyncCommand {
	cmd, err := s.w.repo.ClaimResyncCommand(s.ctx, ResyncScope{s.ax()})
	if err != nil {
		return nil
	}
	return cmd
}

// RecordSkipped takes the adapter's per-roster total and adds the repeats the sink skipped itself.
func (s *workerSink) RecordSkipped(n int64) {
	s.adapterSkipped = n
	total := n + s.dupSkipped
	s.stage(s.currentStage(), StageUpdate{RecordsSkipped: &total})
}

// currentStage keeps a skipped-record update from moving the sync's stage.
func (s *workerSink) currentStage() SyncStage {
	if s.resyncing {
		return StageReceiving
	}
	return StageComplete
}
