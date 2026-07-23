package pmsd

// Startup-ordering and reconciliation tests. These cover the two failure modes that a package-level test
// cannot see: a daemon that keeps admitting events nothing applies, and application loops that drift out of
// step with the set of live Interfaces.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// nilApplier is a concrete type whose nil pointer is stored in a non-nil interface — the classic typed-nil.
type nilApplier struct{}

func (n *nilApplier) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	return false, nil
}

// dialSpy records whether ANY PMS connection was attempted.
type dialSpy struct {
	mu sync.Mutex
	n  int
}

func (d *dialSpy) dial(ctx context.Context, p DialParams) (Conn, error) {
	d.mu.Lock()
	d.n++
	d.mu.Unlock()
	return nil, errors.New("dial should never happen in these tests")
}
func (d *dialSpy) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

// closeSpy proves the repository is released when startup fails.
type closeSpy struct {
	applierRepo
	closed bool
}

func (c *closeSpy) Close() error { c.closed = true; return nil }

// Every way the applier can fail to exist must stop the daemon BEFORE a connector worker or PMS socket starts,
// and must release the repository it had already opened.
func TestApplierConstructionFailsClosedBeforeAnyConnectorWork(t *testing.T) {
	cases := []struct {
		name    string
		factory func(ctx context.Context, a Assignment) (StayApplier, error)
	}{
		{"missing factory", nil},
		{"factory returns error", func(ctx context.Context, a Assignment) (StayApplier, error) {
			return nil, errors.New("no database")
		}},
		{"factory returns nil", func(ctx context.Context, a Assignment) (StayApplier, error) {
			return nil, nil
		}},
		{"factory returns typed nil", func(ctx context.Context, a Assignment) (StayApplier, error) {
			var typed *nilApplier // non-nil interface holding a nil pointer
			return typed, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &dialSpy{}
			repo := &closeSpy{}
			deps := Deps{
				Log:            quietLog(),
				NewStayApplier: tc.factory,
				LoadAssignment: func(ctx context.Context) (Assignment, bool, error) {
					return Assignment{ApplianceID: "ap", TenantID: "t", SiteID: "s"}, true, nil
				},
				OpenRepo: func(ctx context.Context, a Assignment) (Repo, error) { return repo, nil },
				Dial:     spy.dial,
			}
			cfg := iamv2.PMSConfig{MasterEnabled: true, PMSIngestEnabled: true, PMSConnectorEnabled: true}
			err := Run(context.Background(), cfg, deps)
			if !errors.Is(err, ErrApplierRequired) {
				t.Fatalf("err = %v, want ErrApplierRequired", err)
			}
			if spy.count() != 0 {
				t.Fatalf("a PMS connection was attempted (%d) despite a failed applier construction", spy.count())
			}
			if tc.factory != nil && !repo.closed {
				t.Fatal("the repository was not released after a failed startup")
			}
		})
	}
}

// reconcileRepo lets a test change the ACTIVE interface set between reconciliations.
type reconcileRepo struct {
	applierRepo
	mu     sync.Mutex
	active []Interface
}

func (r *reconcileRepo) set(ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = nil
	for _, id := range ids {
		r.active = append(r.active, Interface{ID: id})
	}
}
func (r *reconcileRepo) ListActiveInterfaces(ctx context.Context, tenant, site string) ([]Interface, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Interface, len(r.active))
	copy(out, r.active)
	return out, nil
}
func (r *reconcileRepo) Close() error { return nil }

// waitFor polls until cond holds or the deadline passes.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A newly published Interface gets a loop without a restart; one that stops being ACTIVE has its loop stopped;
// a returning Interface gets a fresh loop; and unrelated Interfaces are never disturbed.
func TestApplierReconcilesInterfaceChanges(t *testing.T) {
	ap := newFakeApplier()
	repo := &reconcileRepo{}
	repo.set("i1")
	deps := &Deps{Log: quietLog(), ApplierReconcileInterval: 50 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runApplierSupervisor(ctx, Assignment{TenantID: "t", SiteID: "s"}, repo, ap, deps)
	}()

	waitUntil(t, "the first interface to start applying", func() bool { return ap.callsFor("i1") > 0 })

	// ADD: a newly activated interface starts being applied, and the existing one keeps working
	repo.set("i1", "i2")
	waitUntil(t, "the new interface to start applying", func() bool { return ap.callsFor("i2") > 0 })
	before := ap.callsFor("i1")
	waitUntil(t, "the untouched interface to keep working", func() bool { return ap.callsFor("i1") > before })

	// REMOVE: an interface that is no longer ACTIVE stops, and stays stopped
	repo.set("i1")
	waitUntil(t, "the removed interface to stop", func() bool {
		a := ap.callsFor("i2")
		time.Sleep(300 * time.Millisecond)
		return ap.callsFor("i2") == a
	})

	// REACTIVATE: it comes back and is applied again
	stopped := ap.callsFor("i2")
	repo.set("i1", "i2")
	waitUntil(t, "the reactivated interface to resume", func() bool { return ap.callsFor("i2") > stopped })

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the applier supervisor did not drain on shutdown")
	}
}

// loopLedger watches the supervisor's loop lifecycle from outside and records, per Interface, every start and
// stop with its generation. It is what makes "exactly one live loop per Interface" an assertion rather than a
// hope: counting live loops directly is the only way to distinguish a REPLACEMENT from a DUPLICATE.
type loopLedger struct {
	mu    sync.Mutex
	live  map[string]int      // interface -> currently live loops
	peak  map[string]int      // interface -> highest simultaneous live loops ever observed
	order map[string][]string // interface -> ordered lifecycle events ("start:1", "stop:1", ...)
}

func newLoopLedger() *loopLedger {
	return &loopLedger{live: map[string]int{}, peak: map[string]int{}, order: map[string][]string{}}
}

func (l *loopLedger) loopStarted(iface string, gen int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live[iface]++
	if l.live[iface] > l.peak[iface] {
		l.peak[iface] = l.live[iface]
	}
	l.order[iface] = append(l.order[iface], "start:"+itoa(gen))
}

func (l *loopLedger) loopStopped(iface string, gen int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live[iface]--
	l.order[iface] = append(l.order[iface], "stop:"+itoa(gen))
}

func (l *loopLedger) snapshot(iface string) (live, peak int, events []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live[iface], l.peak[iface], append([]string(nil), l.order[iface]...)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ONE production supervisor, hammered with concurrent reconcile-triggering changes: there must never be two
// live loops for one Interface, and a removed loop must fully stop before its replacement starts (generations
// must not overlap). The previous four-supervisor test could not show this — four supervisors keep four
// independent loop maps, so it only ever proved that they all drain.
func TestSingleSupervisorKeepsExactlyOneLoopPerInterface(t *testing.T) {
	ap := newFakeApplier()
	repo := &reconcileRepo{}
	repo.set("i1", "i2")
	deps := &Deps{Log: quietLog(), ApplierReconcileInterval: 20 * time.Millisecond}
	ledger := newLoopLedger()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runApplierSupervisorObserved(ctx, Assignment{TenantID: "t", SiteID: "s"}, repo, ap, deps, ledger)
	}()

	waitUntil(t, "both interfaces to be applying", func() bool {
		return ap.callsFor("i1") > 0 && ap.callsFor("i2") > 0
	})

	// flip i2 in and out repeatedly while i1 is untouched
	for i := 0; i < 6; i++ {
		repo.set("i1")
		time.Sleep(40 * time.Millisecond)
		repo.set("i1", "i2")
		time.Sleep(40 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the supervisor did not drain")
	}

	for _, iface := range []string{"i1", "i2"} {
		live, peak, events := ledger.snapshot(iface)
		if peak > 1 {
			t.Fatalf("%s had %d simultaneous loops: %v", iface, peak, events)
		}
		if live != 0 {
			t.Fatalf("%s left %d loops running after shutdown: %v", iface, live, events)
		}
		// generations must alternate start/stop with no overlap: start:1 stop:1 start:2 stop:2 ...
		for i, e := range events {
			wantPrefix := "start:"
			if i%2 == 1 {
				wantPrefix = "stop:"
			}
			if len(e) < len(wantPrefix) || e[:len(wantPrefix)] != wantPrefix {
				t.Fatalf("%s lifecycle is not strictly alternating: %v", iface, events)
			}
			if i%2 == 1 && e[len(wantPrefix):] != events[i-1][len("start:"):] {
				t.Fatalf("%s stopped a different generation than it started: %v", iface, events)
			}
		}
	}
	// i1 was never removed, so it must have run exactly one generation for the whole test
	if _, _, events := ledger.snapshot("i1"); len(events) != 2 || events[0] != "start:1" {
		t.Fatalf("an untouched interface was disturbed: %v", events)
	}
	// i2 must have been replaced, never duplicated
	if _, _, events := ledger.snapshot("i2"); len(events) < 4 {
		t.Fatalf("i2 was expected to stop and restart at least once: %v", events)
	}
}

// A slow-draining removed loop must not overlap with its replacement: the supervisor waits for the old
// generation to finish before a new one for the same Interface can start.
func TestReplacementLoopWaitsForTheOldOneToDrain(t *testing.T) {
	slow := &slowApplier{delay: 200 * time.Millisecond, inner: newFakeApplier()}
	repo := &reconcileRepo{}
	repo.set("i1")
	deps := &Deps{Log: quietLog(), ApplierReconcileInterval: 20 * time.Millisecond}
	ledger := newLoopLedger()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runApplierSupervisorObserved(ctx, Assignment{TenantID: "t", SiteID: "s"}, repo, slow, deps, ledger)
	}()
	waitUntil(t, "the slow interface to start", func() bool { return slow.inner.callsFor("i1") > 0 })

	repo.set() // remove it while an application is mid-flight
	time.Sleep(50 * time.Millisecond)
	repo.set("i1") // and immediately bring it back
	waitUntil(t, "the replacement loop", func() bool {
		_, _, events := ledger.snapshot("i1")
		return len(events) >= 3
	})
	cancel()
	<-done

	_, peak, events := ledger.snapshot("i1")
	if peak > 1 {
		t.Fatalf("a replacement loop overlapped a draining one: %v", events)
	}
}

// slowApplier makes each application take measurable time, so a removal lands mid-flight.
type slowApplier struct {
	delay time.Duration
	inner *fakeApplier
}

func (s *slowApplier) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(s.delay):
	}
	return s.inner.ProcessNext(ctx, tenant, site, iface)
}
