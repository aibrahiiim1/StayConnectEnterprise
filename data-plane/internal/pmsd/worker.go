package pmsd

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	errCompetingOwner = errors.New("pmsd: competing owner holds the interface lock")
	errNotDialable    = errors.New("pmsd: interface not ACTIVE / not read-only-capable")
)

// worker owns exactly one PMS Interface. Its runtime-generation is monotonic per successful ownership and
// is written with optimistic checks so a previous owner cannot overwrite the current owner's state.
type worker struct {
	iface  Interface
	repo   Repo
	deps   *Deps
	cancel context.CancelFunc
	done   chan struct{}
	gen    int64
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
		stable, err := w.ownAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		// reset backoff only after a meaningfully stable connection interval
		if err == nil || stable >= w.deps.StableResetAfter {
			bo.reset()
		}
		d := bo.next()
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// ownAndServe performs the ordered ownership protocol and returns how long a connection stayed up.
// Ordering is strict: lock -> re-read -> startup-persist(UNKNOWN) -> dial -> serve. If the lock is not
// acquired, NO socket is opened. On lock loss the connection is closed and serving stops.
func (w *worker) ownAndServe(ctx context.Context) (time.Duration, error) {
	locker, err := w.deps.NewLocker(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = locker.Close() }()

	got, err := locker.TryLock(ctx, LockKey(w.iface.TenantID, w.iface.SiteID, w.iface.ID))
	if err != nil {
		return 0, err
	}
	if !got {
		// competing owner: do NOT open a PMS socket.
		return 0, errCompetingOwner
	}

	// re-read AFTER acquiring ownership (lifecycle/revision/read-only capability may have changed)
	iface, rev, err := w.repo.LoadInterface(ctx, w.iface.TenantID, w.iface.SiteID, w.iface.ID)
	if err != nil {
		return 0, err
	}
	if iface.LifecycleState != "ACTIVE" || !rev.ReadOnly {
		return 0, errNotDialable
	}
	w.iface = iface

	// persist startup axes: UNKNOWN. Never fabricate a heartbeat / complete-sync / HEALTHY on startup.
	w.gen++
	startup := RuntimeState{
		TenantID: iface.TenantID, SiteID: iface.SiteID, PMSInterfaceID: iface.ID,
		PinnedRevisionID: rev.ID, Generation: w.gen, UpdatedAt: w.deps.now(),
		Transport: TransportConnecting, Continuity: ContinuityUnknown, Sync: SyncUnknown,
		LastConnectAttemptAt: ptr(w.deps.now()),
	}
	if err := w.repo.UpsertRuntime(ctx, startup); err != nil {
		return 0, err // includes ErrStaleGeneration (a newer owner exists)
	}

	// dial only AFTER lock + re-read + startup-persist succeed
	conn, err := w.deps.Dial(ctx, iface, rev)
	if err != nil {
		_ = w.repo.UpsertRuntime(ctx, w.disconnected(rev, sanitize(err)))
		return 0, err
	}
	defer func() { _ = conn.Close() }()

	// cancel serving immediately on lock loss (ownership gone)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	go func() {
		select {
		case <-locker.Lost():
			scancel()
			_ = conn.Close()
		case <-sctx.Done():
		}
	}()

	start := w.deps.now()
	serr := conn.Serve(sctx, &axisSink{w: w, rev: rev})
	// record the disconnect (sanitized)
	code := ""
	if serr != nil {
		code = sanitize(serr)
	}
	_ = w.repo.UpsertRuntime(ctx, w.disconnected(rev, code))
	return w.deps.now().Sub(start), serr
}

func (w *worker) disconnected(rev Revision, code string) RuntimeState {
	now := w.deps.now()
	return RuntimeState{
		TenantID: w.iface.TenantID, SiteID: w.iface.SiteID, PMSInterfaceID: w.iface.ID,
		PinnedRevisionID: rev.ID, Generation: w.gen, UpdatedAt: now,
		Transport: TransportDisconnected, DisconnectedSince: ptr(now), TransportErrorCode: code,
		Continuity: ContinuityUnknown, Sync: SyncUnknown,
	}
}

// axisSink turns real protocol evidence into optimistic RuntimeState upserts. It NEVER writes a healthy
// state that is not backed by an observed axis event.
type axisSink struct {
	w   *worker
	rev Revision
}

func (a *axisSink) base(t TransportStatus, c ContinuityStatus, s SyncStatus) RuntimeState {
	return RuntimeState{
		TenantID: a.w.iface.TenantID, SiteID: a.w.iface.SiteID, PMSInterfaceID: a.w.iface.ID,
		PinnedRevisionID: a.rev.ID, Generation: a.w.gen, UpdatedAt: a.w.deps.now(),
		Transport: t, Continuity: c, Sync: s,
	}
}
func (a *axisSink) OnConnected(at time.Time) {
	st := a.base(TransportConnected, ContinuityUnknown, SyncUnknown)
	st.LastConnectedAt = ptr(at)
	_ = a.w.repo.UpsertRuntime(context.Background(), st)
}
func (a *axisSink) OnHeartbeat(at time.Time) {
	st := a.base(TransportConnected, ContinuityUnknown, SyncUnknown)
	st.LastHeartbeatAt = ptr(at)
	_ = a.w.repo.UpsertRuntime(context.Background(), st)
}
func (a *axisSink) OnValidEvent(at time.Time, cursor string) {
	st := a.base(TransportConnected, ContinuityContinuous, SyncUnknown)
	st.LastValidEventAt = ptr(at)
	st.LastEventCursor = clip(cursor, 4096)
	_ = a.w.repo.UpsertRuntime(context.Background(), st)
}
func (a *axisSink) OnResyncMarker(at time.Time) {
	st := a.base(TransportConnected, ContinuityContinuous, SyncResyncInProgress)
	st.LastResyncMarkerAt = ptr(at)
	_ = a.w.repo.UpsertRuntime(context.Background(), st)
}
func (a *axisSink) OnCompleteSync(at time.Time, cursor string) {
	st := a.base(TransportConnected, ContinuityContinuous, SyncInSync)
	st.LastCompleteSyncAt = ptr(at)
	st.SyncCursor = clip(cursor, 4096)
	_ = a.w.repo.UpsertRuntime(context.Background(), st)
}
func (a *axisSink) OnDisconnected(at time.Time, sanitizedCode string) {
	st := a.base(TransportDisconnected, ContinuityUnknown, SyncUnknown)
	st.DisconnectedSince = ptr(at)
	st.TransportErrorCode = clip(sanitizedCode, 200)
	_ = a.w.repo.UpsertRuntime(context.Background(), st)
}

func ptr(t time.Time) *time.Time { return &t }
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sanitize reduces an error to a bounded machine-safe code (never a raw PMS payload / guest data /
// credential). It keeps only an uppercased, underscore-joined, length-bounded token.
func sanitize(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	// keep only the first token-ish segment, uppercased, bounded
	if i := strings.IndexAny(s, ":\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' {
			b.WriteRune('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "ERROR"
	}
	return out
}

func (d *Deps) rnd(n int64) int64 { // default jitter source (deterministic-friendly override in tests)
	if d.jitter != nil {
		return d.jitter(n)
	}
	return time.Now().UnixNano()
}
