package pmsd

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// ---- fakes / spies ---------------------------------------------------------

type fakeRepo struct {
	mu        sync.Mutex
	ifaces    []Interface
	listErr   error
	loadFn    func(id string) (Interface, Revision, error)
	upserts   []RuntimeState
	storedGen map[string]int64
}

func newFakeRepo(ifaces ...Interface) *fakeRepo {
	return &fakeRepo{ifaces: ifaces, storedGen: map[string]int64{}}
}
func (r *fakeRepo) ListActiveInterfaces(ctx context.Context) ([]Interface, error) {
	return r.ifaces, r.listErr
}
func (r *fakeRepo) LoadInterface(ctx context.Context, t, s, id string) (Interface, Revision, error) {
	if r.loadFn != nil {
		return r.loadFn(id)
	}
	return Interface{TenantID: t, SiteID: s, ID: id, LifecycleState: "ACTIVE", CurrentRevisionID: "rev1"},
		Revision{ID: "rev1", ReadOnly: true}, nil
}
func (r *fakeRepo) UpsertRuntime(ctx context.Context, st RuntimeState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st.Generation < r.storedGen[st.PMSInterfaceID] {
		return ErrStaleGeneration
	}
	r.storedGen[st.PMSInterfaceID] = st.Generation
	r.upserts = append(r.upserts, st)
	return nil
}
func (r *fakeRepo) firstUpsert() (RuntimeState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.upserts) == 0 {
		return RuntimeState{}, false
	}
	return r.upserts[0], true
}

type fakeLocker struct {
	got     bool
	lockErr error
	lost    chan struct{}
	closed  atomic.Bool
}

func newFakeLocker(got bool) *fakeLocker { return &fakeLocker{got: got, lost: make(chan struct{})} }
func (l *fakeLocker) TryLock(ctx context.Context, key int64) (bool, error) {
	return l.got, l.lockErr
}
func (l *fakeLocker) Lost() <-chan struct{} { return l.lost }
func (l *fakeLocker) Close() error          { l.closed.Store(true); return nil }

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
	openRepo, newLocker, dial atomic.Int64
}

func fastDeps(sp *spies, repo Repo, mkLocker func() Locker, mkConn func() *fakeConn) Deps {
	return Deps{
		OpenRepo: func(ctx context.Context) (Repo, error) { sp.openRepo.Add(1); return repo, nil },
		NewLocker: func(ctx context.Context) (Locker, error) {
			sp.newLocker.Add(1)
			return mkLocker(), nil
		},
		Dial: func(ctx context.Context, i Interface, r Revision) (Conn, error) {
			sp.dial.Add(1)
			return mkConn(), nil
		},
		Now:               time.Now,
		ReconcileInterval: 20 * time.Millisecond,
		BackoffMin:        1 * time.Millisecond,
		BackoffMax:        5 * time.Millisecond,
		StableResetAfter:  1 * time.Millisecond,
		StopGrace:         200 * time.Millisecond,
		jitter:            func(n int64) int64 { return 0 },
	}
}

// fixed valid UUIDs so the UUID-strict LockKey accepts the fakes (values are arbitrary but canonical).
const (
	tTenantUUID = "11111111-1111-1111-1111-111111111111"
	tSiteUUID   = "22222222-2222-2222-2222-222222222222"
)

// ifaceUUID maps a short test label to a fixed canonical interface UUID.
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
	return Interface{TenantID: tTenantUUID, SiteID: tSiteUUID, ID: ifaceUUID(id), LifecycleState: "ACTIVE", CurrentRevisionID: "rev1"}
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

// ---- §16.1-3: flags-OFF / fail-closed -------------------------------------

func TestFlagsOff_ZeroSideEffects(t *testing.T) {
	var sp spies
	deps := Deps{
		OpenRepo:  func(context.Context) (Repo, error) { sp.openRepo.Add(1); return nil, nil },
		NewLocker: func(context.Context) (Locker, error) { sp.newLocker.Add(1); return nil, nil },
		Dial:      func(context.Context, Interface, Revision) (Conn, error) { sp.dial.Add(1); return nil, nil },
	}
	g0 := runtime.NumGoroutine()
	if err := Run(context.Background(), iamv2.DefaultPMSConfig(), deps); err != nil {
		t.Fatalf("flags-off Run must return nil, got %v", err)
	}
	if sp.openRepo.Load() != 0 || sp.newLocker.Load() != 0 || sp.dial.Load() != 0 {
		t.Fatalf("flags-off must not open repo/locker/dial: openRepo=%d newLocker=%d dial=%d",
			sp.openRepo.Load(), sp.newLocker.Load(), sp.dial.Load())
	}
	time.Sleep(20 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > g0+1 {
		t.Fatalf("flags-off must not spawn workers: goroutines %d -> %d", g0, n)
	}
}

func TestFailClosed_ChildOnMasterOff(t *testing.T) {
	var sp spies
	deps := Deps{OpenRepo: func(context.Context) (Repo, error) { sp.openRepo.Add(1); return nil, nil }}
	err := Run(context.Background(), iamv2.PMSConfig{PMSConnectorEnabled: true}, deps) // master OFF, child ON
	if err == nil {
		t.Fatal("child-on/master-off must fail closed")
	}
	if sp.openRepo.Load() != 0 {
		t.Fatal("fail-closed must not open repo")
	}
}

// ---- §16.4-5: one/two interfaces -> independent workers -------------------

func TestOneInterface_OneWorkerDials(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 1 })
	cancel()
	<-done
}

func TestTwoInterfaces_IndependentWorkers(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"), iface("i2"))
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 2 })
	cancel()
	<-done
}

// ---- §16.6,8: competing owner / lock lost before dial -> no socket --------

func TestCompetingOwner_NoSocket(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(false) }, func() *fakeConn { return &fakeConn{} })
	deps.BackoffMax = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = Run(ctx, connectorOn(), deps)
	if sp.dial.Load() != 0 {
		t.Fatalf("a competing (non-owning) worker must NOT dial; dials=%d", sp.dial.Load())
	}
	if sp.newLocker.Load() == 0 {
		t.Fatal("worker should have attempted to lock")
	}
}

// ---- §16.9: interface disabled after discovery -> no dial -----------------

func TestDisabledAfterDiscovery_NoDial(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	repo.loadFn = func(id string) (Interface, Revision, error) {
		return Interface{TenantID: "t", SiteID: "s", ID: id, LifecycleState: "AUTH_DISABLED"}, Revision{ID: "rev1", ReadOnly: true}, nil
	}
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = Run(ctx, connectorOn(), deps)
	if sp.dial.Load() != 0 {
		t.Fatalf("disabled interface must not dial; dials=%d", sp.dial.Load())
	}
}

// ---- §16.10: revision changed after lock -> latest used -------------------

func TestRevisionChangedAfterLock_LatestUsed(t *testing.T) {
	var sp spies
	var gotRev atomic.Value
	repo := newFakeRepo(iface("i1"))
	repo.loadFn = func(id string) (Interface, Revision, error) {
		return iface(id), Revision{ID: "rev2-latest", ReadOnly: true}, nil // discovery said rev1; reload says rev2
	}
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	deps.Dial = func(ctx context.Context, i Interface, r Revision) (Conn, error) {
		gotRev.Store(r.ID)
		sp.dial.Add(1)
		return &fakeConn{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() >= 1 })
	cancel()
	<-done
	if gotRev.Load() != "rev2-latest" {
		t.Fatalf("dial must use the revision re-read after lock, got %v", gotRev.Load())
	}
}

// ---- §16.7: lock DB connection loss -> PMS socket closed ------------------

func TestLockLoss_ClosesSocket(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	lk := newFakeLocker(true)
	conn := &fakeConn{}
	deps := fastDeps(&sp, repo, func() Locker { return lk }, func() *fakeConn { return conn })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() >= 1 })
	close(lk.lost) // simulate DB session death
	waitFor(t, time.Second, func() bool { return conn.closed.Load() })
	cancel()
	<-done
}

// ---- §16.14: runtime generation blocks stale worker writes ----------------

func TestRuntimeGeneration_RejectsStale(t *testing.T) {
	repo := newFakeRepo()
	if err := repo.UpsertRuntime(context.Background(), RuntimeState{PMSInterfaceID: "i1", Generation: 5}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertRuntime(context.Background(), RuntimeState{PMSInterfaceID: "i1", Generation: 3}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale generation must be rejected, got %v", err)
	}
	if err := repo.UpsertRuntime(context.Background(), RuntimeState{PMSInterfaceID: "i1", Generation: 6}); err != nil {
		t.Fatalf("newer generation must be accepted, got %v", err)
	}
}

// ---- §16.15: startup writes no fake HEALTHY -------------------------------

func TestStartup_NoFakeHealthy(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"))
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { _, ok := repo.firstUpsert(); return ok })
	cancel()
	<-done
	st, _ := repo.firstUpsert()
	if st.Transport == TransportConnected {
		t.Fatal("startup must not report CONNECTED before real evidence")
	}
	if st.Continuity != ContinuityUnknown || st.Sync != SyncUnknown {
		t.Fatalf("startup continuity/sync must be UNKNOWN, got %s/%s", st.Continuity, st.Sync)
	}
	if st.LastHeartbeatAt != nil || st.LastCompleteSyncAt != nil {
		t.Fatal("startup must not fabricate a heartbeat / complete-sync")
	}
}

// ---- §16.18: outbound allowlist rejects PS (no financial posting) ---------

func TestOutboundAllowlist_RejectsPS(t *testing.T) {
	for _, ok := range []string{"LS", "LD", "LR", "LA", "DR"} {
		if err := CheckOutbound(ok); err != nil {
			t.Fatalf("read-only record %q must be allowed: %v", ok, err)
		}
	}
	for _, bad := range []string{"PS", "PA", "XX", ""} {
		if err := CheckOutbound(bad); err == nil {
			t.Fatalf("record %q must be rejected", bad)
		}
	}
	if !IsFinancialRecord("PS") || IsFinancialRecord("LS") {
		t.Fatal("PS is financial; LS is not")
	}
}

// ---- §16.19: sanitize -> bounded machine code, no PII ---------------------

func TestSanitize_NoLeak(t *testing.T) {
	got := sanitize(errors.New("dial tcp 10.0.0.5:5001: guest SMITH room 14215 reservation 262224"))
	if len(got) > 64 {
		t.Fatalf("sanitized code must be bounded, got %q", got)
	}
	for _, leak := range []string{"SMITH", "14215", "262224", "10.0.0.5", "5001"} {
		if strings.Contains(got, leak) {
			t.Fatalf("sanitized code leaked %q: %q", leak, got)
		}
	}
	for _, r := range got {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			t.Fatalf("sanitized code has non-machine char: %q", got)
		}
	}
}

// ---- §16.13,20: backoff bounded + SIGTERM drain w/o goroutine leak --------

func TestBackoff_Bounded(t *testing.T) {
	b := newBackoff(1*time.Millisecond, 8*time.Millisecond, nil)
	for i := 0; i < 20; i++ {
		if d := b.next(); d > 8*time.Millisecond {
			t.Fatalf("backoff exceeded max: %s", d)
		}
	}
}

func TestSIGTERMDrain_NoGoroutineLeak(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("i1"), iface("i2"), iface("i3"))
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	g0 := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() == 3 })
	cancel()
	<-done
	// allow watcher goroutines to unwind
	waitFor(t, 2*time.Second, func() bool { return runtime.NumGoroutine() <= g0+2 })
}

// ---- §16.12: one interface failure does not stop another ------------------

func TestOneFailureDoesNotStopAnother(t *testing.T) {
	var sp spies
	repo := newFakeRepo(iface("bad"), iface("good"))
	deps := fastDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	var goodServed atomic.Bool
	deps.Dial = func(ctx context.Context, i Interface, r Revision) (Conn, error) {
		sp.dial.Add(1)
		if i.ID == "bad" {
			return nil, errors.New("DIAL_FAIL")
		}
		return &fakeConn{serveFn: func(ctx context.Context, s AxisSink) error {
			goodServed.Store(true)
			<-ctx.Done()
			return ctx.Err()
		}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, 2*time.Second, func() bool { return goodServed.Load() })
	cancel()
	<-done
}
