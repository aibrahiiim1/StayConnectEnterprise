package pmsd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// ---- fixed valid UUIDs -----------------------------------------------------

const (
	tTenantUUID = "11111111-1111-1111-1111-111111111111"
	tSiteUUID   = "22222222-2222-2222-2222-222222222222"
	tAppliance  = "appliance-1"
	tRevUUID    = "33333333-0000-4000-8000-000000000001"
	tSecUUID    = "44444444-0000-4000-8000-000000000001"
)

func ifaceUUID(short string) string {
	switch short {
	case "i1":
		return "aaaaaaaa-0000-4000-8000-000000000001"
	case "i2":
		return "aaaaaaaa-0000-4000-8000-000000000002"
	case "i3":
		return "aaaaaaaa-0000-4000-8000-000000000003"
	default:
		return "aaaaaaaa-0000-4000-8000-0000000000ff"
	}
}

func iface(id string) Interface {
	return Interface{TenantID: tTenantUUID, SiteID: tSiteUUID, ID: ifaceUUID(id), ConnectorKind: "protel-fias", LifecycleState: "ACTIVE", CurrentRevisionID: tRevUUID}
}

func validRevision() Revision {
	return Revision{
		ID: tRevUUID, ConnectorKind: "protel-fias", Endpoint: "127.0.0.1:15010", SourceTimezone: "UTC",
		ReadOnly: true, NormalizationVersion: 1, DialTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, HeartbeatInterval: time.Second, HeartbeatTimeout: time.Second,
		FeedFreshnessBound: time.Second, CompleteSyncBound: time.Second, ResyncSupported: true,
		Published: true, ActiveSecretGenerationID: tSecUUID,
	}
}
func validSecret() SecretGeneration { return SecretGeneration{ID: tSecUUID, GenerationNo: 1} }

// ---- fake repo (assignment-scoped, atomic generation, independent-axis CAS) -----

type fakeRepo struct {
	mu       sync.Mutex
	ifaces   []Interface
	listErr  error
	loadFn   func(id string) (Interface, Revision, SecretGeneration, error)
	allocErr error

	gen        map[string]int64 // stored generation (CAS truth)
	transport  map[string]TransportStatus
	continuity map[string]ContinuityStatus
	syncS      map[string]SyncStatus
	gapReason  map[string]Code
	markGapErr error
	updates    int64
	closed     atomic.Bool
	lastScope  [2]string

	resyncSeq    map[string]int64
	publishedGen map[string]int64
	liveRows     []string
	stagedRows   []string
}

func newFakeRepo(ifaces ...Interface) *fakeRepo {
	return &fakeRepo{ifaces: ifaces, gen: map[string]int64{}, transport: map[string]TransportStatus{}, continuity: map[string]ContinuityStatus{}, syncS: map[string]SyncStatus{}, gapReason: map[string]Code{}, resyncSeq: map[string]int64{}, publishedGen: map[string]int64{}}
}

func (r *fakeRepo) ListActiveInterfaces(ctx context.Context, t, s string) ([]Interface, error) {
	r.mu.Lock()
	r.lastScope = [2]string{t, s}
	r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []Interface
	for _, i := range r.ifaces {
		if i.TenantID == t && i.SiteID == s {
			out = append(out, i)
		}
	}
	return out, nil
}

func (r *fakeRepo) LoadInterface(ctx context.Context, t, s, id string) (Interface, Revision, SecretGeneration, error) {
	if r.loadFn != nil {
		return r.loadFn(id)
	}
	return Interface{TenantID: t, SiteID: s, ID: id, ConnectorKind: "protel-fias", LifecycleState: "ACTIVE", CurrentRevisionID: tRevUUID},
		validRevision(), validSecret(), nil
}

func (r *fakeRepo) AllocateRuntimeGeneration(ctx context.Context, req GenerationRequest) (int64, error) {
	if r.allocErr != nil {
		return 0, r.allocErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen[req.PMSInterfaceID]++
	return r.gen[req.PMSInterfaceID], nil
}

func (r *fakeRepo) cas(id string, expect int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen[id] != expect {
		return ErrStaleGeneration
	}
	r.updates++
	return nil
}
func (r *fakeRepo) UpdateTransport(ctx context.Context, u TransportUpdate) error {
	if err := r.cas(u.PMSInterfaceID, u.ExpectedGeneration); err != nil {
		return err
	}
	r.mu.Lock()
	r.transport[u.PMSInterfaceID] = u.Status
	r.mu.Unlock()
	return nil
}
func (r *fakeRepo) UpdateContinuity(ctx context.Context, u ContinuityUpdate) error {
	if err := r.cas(u.PMSInterfaceID, u.ExpectedGeneration); err != nil {
		return err
	}
	r.mu.Lock()
	r.continuity[u.PMSInterfaceID] = u.Status
	r.mu.Unlock()
	return nil
}
func (r *fakeRepo) UpdateSync(ctx context.Context, u SyncUpdate) error {
	if err := r.cas(u.PMSInterfaceID, u.ExpectedGeneration); err != nil {
		return err
	}
	r.mu.Lock()
	r.syncS[u.PMSInterfaceID] = u.Status
	r.mu.Unlock()
	return nil
}

// MarkGapAndRequireResync moves BOTH axes atomically under a single CAS (both or neither): a stale generation
// changes nothing, mirroring the real single-row transactional UPDATE.
func (r *fakeRepo) MarkGapAndRequireResync(ctx context.Context, req GapResyncRequest) error {
	if r.markGapErr != nil {
		return r.markGapErr
	}
	if err := r.cas(req.PMSInterfaceID, req.ExpectedGeneration); err != nil {
		return err // neither axis changes
	}
	r.mu.Lock()
	r.continuity[req.PMSInterfaceID] = ContinuityGapDetected
	r.syncS[req.PMSInterfaceID] = SyncResyncRequired
	r.gapReason[req.PMSInterfaceID] = req.Reason
	r.mu.Unlock()
	return nil
}
func (r *fakeRepo) AllocateResyncGeneration(ctx context.Context, req ResyncScope) (int64, error) {
	if err := r.cas(req.PMSInterfaceID, req.ExpectedGeneration); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resyncSeq[req.PMSInterfaceID]++
	return r.resyncSeq[req.PMSInterfaceID], nil
}
func (r *fakeRepo) AdmitLiveEvent(ctx context.Context, row InboxRow) (string, error) {
	if err := r.cas(row.PMSInterfaceID, row.ExpectedGeneration); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveRows = append(r.liveRows, row.ExternalEventIdentity)
	return "live-" + row.ExternalEventIdentity, nil
}
func (r *fakeRepo) StageResyncEvent(ctx context.Context, row InboxRow) (string, error) {
	if err := r.cas(row.PMSInterfaceID, row.ExpectedGeneration); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stagedRows = append(r.stagedRows, row.ExternalEventIdentity)
	return "resync-" + row.ExternalEventIdentity, nil
}
func (r *fakeRepo) PublishResyncGeneration(ctx context.Context, req ResyncScope, g int64) error {
	if err := r.cas(req.PMSInterfaceID, req.ExpectedGeneration); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishedGen[req.PMSInterfaceID] = g
	return nil
}
func (r *fakeRepo) Close() error { r.closed.Store(true); return nil }

func (r *fakeRepo) transportOf(id string) TransportStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transport[id]
}
func (r *fakeRepo) scope() [2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastScope
}

// ---- fake locker / conn / spies -------------------------------------------

type fakeLocker struct {
	got    bool
	lost   chan struct{}
	closed atomic.Bool
}

func newFakeLocker(got bool) *fakeLocker                                   { return &fakeLocker{got: got, lost: make(chan struct{})} }
func (l *fakeLocker) TryLock(ctx context.Context, key int64) (bool, error) { return l.got, nil }
func (l *fakeLocker) Lost() <-chan struct{}                                { return l.lost }
func (l *fakeLocker) Close() error                                         { l.closed.Store(true); return nil }

type fakeConn struct {
	serveFn func(ctx context.Context, sink AxisSink) error
	closed  atomic.Bool
}

func (c *fakeConn) Serve(ctx context.Context, sink AxisSink) error {
	if c.serveFn != nil {
		return c.serveFn(ctx, sink)
	}
	<-ctx.Done()
	return ctx.Err()
}
func (c *fakeConn) Close() error { c.closed.Store(true); return nil }

type spies struct {
	loadAssign, openRepo, newLocker, decrypt, dial atomic.Int64
}

func assignedDeps(sp *spies, repo Repo, mkLocker func() Locker, mkConn func() *fakeConn) Deps {
	return Deps{
		LoadAssignment: func(ctx context.Context) (Assignment, bool, error) {
			sp.loadAssign.Add(1)
			return Assignment{ApplianceID: tAppliance, TenantID: tTenantUUID, SiteID: tSiteUUID}, true, nil
		},
		OpenRepo:  func(ctx context.Context, a Assignment) (Repo, error) { sp.openRepo.Add(1); return repo, nil },
		NewLocker: func(ctx context.Context) (Locker, error) { sp.newLocker.Add(1); return mkLocker(), nil },
		DecryptSecret: func(ctx context.Context, i Interface, r Revision, sg SecretGeneration) (SecretMaterial, error) {
			sp.decrypt.Add(1)
			return NewSecretMaterial([]byte("secret")), nil
		},
		Dial: func(ctx context.Context, p DialParams) (Conn, error) { sp.dial.Add(1); return mkConn(), nil },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:  time.Now, ReconcileInterval: 20 * time.Millisecond, BackoffMin: time.Millisecond,
		BackoffMax: 5 * time.Millisecond, StableResetAfter: time.Millisecond, StopGrace: 300 * time.Millisecond,
		QueueCapacity: 16, QueueEnqueueTimeout: 20 * time.Millisecond, jitter: func(int64) int64 { return 0 },
	}
}

func connectorOn() iamv2.PMSConfig {
	return iamv2.PMSConfig{MasterEnabled: true, PMSConnectorEnabled: true}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

// ---- flags-off / fail-closed / assignment ---------------------------------

func TestFlagsOff_ZeroSideEffects(t *testing.T) {
	var sp spies
	deps := Deps{
		LoadAssignment: func(context.Context) (Assignment, bool, error) { sp.loadAssign.Add(1); return Assignment{}, false, nil },
		OpenRepo:       func(context.Context, Assignment) (Repo, error) { sp.openRepo.Add(1); return nil, nil },
		NewLocker:      func(context.Context) (Locker, error) { sp.newLocker.Add(1); return nil, nil },
		Dial:           func(context.Context, DialParams) (Conn, error) { sp.dial.Add(1); return nil, nil },
	}
	g0 := runtime.NumGoroutine()
	if err := Run(context.Background(), iamv2.DefaultPMSConfig(), deps); err != nil {
		t.Fatalf("flags-off Run must return nil, got %v", err)
	}
	if sp.loadAssign.Load() != 0 || sp.openRepo.Load() != 0 || sp.newLocker.Load() != 0 || sp.dial.Load() != 0 {
		t.Fatalf("flags-off must not load assignment / open repo / lock / dial")
	}
	time.Sleep(20 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > g0+1 {
		t.Fatalf("flags-off must not spawn workers: goroutines %d -> %d", g0, n)
	}
}

func TestFailClosed_ChildOnMasterOff(t *testing.T) {
	var sp spies
	deps := Deps{LoadAssignment: func(context.Context) (Assignment, bool, error) { sp.loadAssign.Add(1); return Assignment{}, false, nil }}
	if err := Run(context.Background(), iamv2.PMSConfig{PMSConnectorEnabled: true}, deps); err == nil {
		t.Fatal("child-on/master-off must fail closed")
	}
	if sp.loadAssign.Load() != 0 {
		t.Fatal("fail-closed must not load assignment")
	}
}

func TestUnassigned_FailsClosedNoWork(t *testing.T) {
	var sp spies
	deps := assignedDeps(&sp, newFakeRepo(), func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	deps.LoadAssignment = func(context.Context) (Assignment, bool, error) { sp.loadAssign.Add(1); return Assignment{}, false, nil }
	err := Run(context.Background(), connectorOn(), deps)
	if !errors.Is(err, ErrNoAssignment) {
		t.Fatalf("unassigned appliance must fail closed with ErrNoAssignment, got %v", err)
	}
	if sp.openRepo.Load() != 0 || sp.dial.Load() != 0 {
		t.Fatal("unassigned appliance must do zero PMS work")
	}
}

// ---- discovery / independent workers --------------------------------------

func TestOneInterface_OneWorkerDials(t *testing.T) {
	var sp spies
	deps := assignedDeps(&sp, newFakeRepo(iface("i1")), func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 1 })
	cancel()
	<-done
}

func TestTwoInterfaces_IndependentWorkers(t *testing.T) {
	var sp spies
	deps := assignedDeps(&sp, newFakeRepo(iface("i1"), iface("i2")), func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 2 })
	cancel()
	<-done
}

// ---- cross-scope rejection ------------------------------------------------

func TestCrossScope_NotDiscovered(t *testing.T) {
	var sp spies
	other := Interface{TenantID: "99999999-9999-9999-9999-999999999999", SiteID: tSiteUUID, ID: ifaceUUID("i2"), ConnectorKind: "protel-fias", LifecycleState: "ACTIVE", CurrentRevisionID: tRevUUID}
	repo := newFakeRepo(iface("i1"), other)
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 1 })
	time.Sleep(60 * time.Millisecond)
	if sp.dial.Load() != 1 {
		t.Fatalf("cross-scope interface must not be discovered/dialed; dials=%d", sp.dial.Load())
	}
	if repo.scope() != [2]string{tTenantUUID, tSiteUUID} {
		t.Fatalf("list must be scoped to the assignment, got %v", repo.scope())
	}
	cancel()
	<-done
}

// ---- competing owner: no secret, no dial ----------------------------------

func TestCompetingOwner_NoSecretNoSocket(t *testing.T) {
	var sp spies
	deps := assignedDeps(&sp, newFakeRepo(iface("i1")), func() Locker { return newFakeLocker(false) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = Run(ctx, connectorOn(), deps)
	if sp.dial.Load() != 0 {
		t.Fatalf("lock loser must NOT dial; dials=%d", sp.dial.Load())
	}
	if sp.decrypt.Load() != 0 {
		t.Fatalf("lock loser must NOT decrypt a secret; decrypts=%d", sp.decrypt.Load())
	}
	if sp.newLocker.Load() == 0 {
		t.Fatal("worker should have attempted to lock")
	}
}

// ---- disabled / invalid-revision / missing-secret after re-read -> no dial -

func TestDisabledAfterDiscovery_NoDial(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	repo.loadFn = func(id string) (Interface, Revision, SecretGeneration, error) {
		return Interface{TenantID: tTenantUUID, SiteID: tSiteUUID, ID: id, LifecycleState: "AUTH_DISABLED"}, validRevision(), validSecret(), nil
	}
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = Run(ctx, connectorOn(), deps)
	if sp.dial.Load() != 0 || sp.decrypt.Load() != 0 {
		t.Fatalf("disabled interface must not decrypt/dial; dials=%d decrypts=%d", sp.dial.Load(), sp.decrypt.Load())
	}
}

func TestRevisionInvalid_NoDial(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	repo.loadFn = func(id string) (Interface, Revision, SecretGeneration, error) {
		rev := validRevision()
		rev.ReadOnly = false // read-only capability absent -> must fail closed
		return iface("i1"), rev, validSecret(), nil
	}
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = Run(ctx, connectorOn(), deps)
	if sp.dial.Load() != 0 || sp.decrypt.Load() != 0 {
		t.Fatalf("invalid (non-read-only) revision must not decrypt/dial; dials=%d", sp.dial.Load())
	}
}

// ---- startup persists CONNECTING, never a fabricated healthy ---------------

func TestStartup_NoFakeHealthy(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return repo.transportOf(ifaceUUID("i1")) != "" })
	if got := repo.transportOf(ifaceUUID("i1")); got != TransportConnecting && got != TransportConnected {
		t.Fatalf("startup transport must be CONNECTING (or CONNECTED after OnConnected), got %q", got)
	}
	repo.mu.Lock()
	c := repo.continuity[ifaceUUID("i1")]
	s := repo.syncS[ifaceUUID("i1")]
	repo.mu.Unlock()
	if c == ContinuityContinuous || s == SyncInSync {
		t.Fatalf("startup must not fabricate healthy continuity/sync: continuity=%q sync=%q", c, s)
	}
	cancel()
	<-done
}

// ---- lock loss closes the socket ------------------------------------------

func TestLockLoss_ClosesSocket(t *testing.T) {
	var sp spies
	lk := newFakeLocker(true)
	conn := &fakeConn{}
	deps := assignedDeps(&sp, newFakeRepo(iface("i1")), func() Locker { return lk }, func() *fakeConn { return conn })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() >= 1 })
	close(lk.lost) // simulate ownership loss
	waitFor(t, time.Second, func() bool { return conn.closed.Load() })
	cancel()
	<-done
}

// ---- independent axes: heartbeat preserves continuity/sync -----------------

func TestIndependentAxes_HeartbeatPreservesContinuitySync(t *testing.T) {
	repo := newFakeRepo(iface("i1"))
	id := ifaceUUID("i1")
	// simulate an owned generation
	gen, _ := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: id})
	w := &worker{iface: iface("i1"), repo: repo, deps: &Deps{Now: time.Now}}
	sink := &workerSink{w: w, ctx: context.Background(), gen: gen, q: NewBoundedQueue(4, time.Second)}
	// establish continuity + sync
	if err := repo.UpdateContinuity(context.Background(), ContinuityUpdate{axisBase: axisBase{PMSInterfaceID: id, ExpectedGeneration: gen}, Status: ContinuityContinuous}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSync(context.Background(), SyncUpdate{axisBase: axisBase{PMSInterfaceID: id, ExpectedGeneration: gen}, Status: SyncInSync}); err != nil {
		t.Fatal(err)
	}
	// a heartbeat updates ONLY transport
	if err := sink.OnHeartbeat(time.Now()); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.continuity[id] != ContinuityContinuous {
		t.Errorf("heartbeat erased continuity: %q", repo.continuity[id])
	}
	if repo.syncS[id] != SyncInSync {
		t.Errorf("heartbeat erased sync: %q", repo.syncS[id])
	}
	if repo.transport[id] != TransportConnected {
		t.Errorf("heartbeat should set transport CONNECTED, got %q", repo.transport[id])
	}
}

// ---- stale generation rejected --------------------------------------------

func TestRuntimeGeneration_StaleRejected(t *testing.T) {
	repo := newFakeRepo(iface("i1"))
	id := ifaceUUID("i1")
	genA, _ := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: id}) // 1
	genB, _ := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: id}) // 2 (restart)
	if genB <= genA {
		t.Fatalf("restart must obtain a higher generation: %d !> %d", genB, genA)
	}
	// old owner A can no longer update
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: axisBase{PMSInterfaceID: id, ExpectedGeneration: genA}, Status: TransportConnected}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale owner A must be rejected, got %v", err)
	}
	// new owner B can
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: axisBase{PMSInterfaceID: id, ExpectedGeneration: genB}, Status: TransportConnected}); err != nil {
		t.Fatalf("current owner B must update, got %v", err)
	}
}

// ---- assignment change drains + re-scopes ---------------------------------

func TestAssignmentChange_DrainsWorkers(t *testing.T) {
	var sp spies
	var phase atomic.Int32
	deps := assignedDeps(&sp, newFakeRepo(iface("i1")), func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	deps.LoadAssignment = func(context.Context) (Assignment, bool, error) {
		sp.loadAssign.Add(1)
		if phase.Load() == 0 {
			return Assignment{ApplianceID: tAppliance, TenantID: tTenantUUID, SiteID: tSiteUUID}, true, nil
		}
		// after flip, a different site → drain + re-scope
		return Assignment{ApplianceID: tAppliance, TenantID: tTenantUUID, SiteID: "55555555-5555-5555-5555-555555555555"}, true, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() >= 1 })
	openBefore := sp.openRepo.Load()
	phase.Store(1)
	waitFor(t, 2*time.Second, func() bool { return sp.openRepo.Load() > openBefore }) // re-scoped → repo reopened
	cancel()
	<-done
}

// ---- SIGTERM/cancel drains, no goroutine leak -----------------------------

func TestCancelDrain_NoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	var sp spies
	deps := assignedDeps(&sp, newFakeRepo(iface("i1"), iface("i2")), func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 2 })
	cancel()
	<-done
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutine leak after drain: before=%d after=%d", before, after)
	}
}

// ---- one worker failure does not stop another -----------------------------

func TestOneFailureDoesNotStopAnother(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"), iface("i2"))
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn {
		return &fakeConn{serveFn: func(ctx context.Context, sink AxisSink) error {
			<-ctx.Done()
			return ctx.Err()
		}}
	})
	// i1's dial always fails; i2 must still dial
	base := deps.Dial
	deps.Dial = func(ctx context.Context, p DialParams) (Conn, error) {
		if p.Iface.ID == ifaceUUID("i1") {
			sp.dial.Add(1)
			return nil, errors.New("dial boom")
		}
		return base(ctx, p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, 2*time.Second, func() bool { return sp.dial.Load() >= 2 })
	cancel()
	<-done
}
