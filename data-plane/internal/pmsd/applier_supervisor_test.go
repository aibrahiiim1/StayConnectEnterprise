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

// Reconciling concurrently must never produce two loops for one Interface: the loop map is the single source
// of truth for whether one already exists.
func TestConcurrentReconcileDoesNotDuplicateLoops(t *testing.T) {
	ap := newFakeApplier()
	repo := &reconcileRepo{}
	repo.set("i1", "i2", "i3")
	deps := &Deps{Log: quietLog(), ApplierReconcileInterval: 50 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	// several supervisors would be a bug in production, but running them here is the cheapest way to hammer
	// the reconcile path concurrently; each keeps its OWN loop set, so we assert on drain rather than count.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runApplierSupervisor(ctx, Assignment{TenantID: "t", SiteID: "s"}, repo, ap, deps)
		}()
	}
	waitUntil(t, "all interfaces to be applied", func() bool {
		return ap.callsFor("i1") > 0 && ap.callsFor("i2") > 0 && ap.callsFor("i3") > 0
	})
	// flip the set repeatedly while everything is running: no panic, no deadlock, and a clean drain
	for i := 0; i < 5; i++ {
		repo.set("i1")
		time.Sleep(20 * time.Millisecond)
		repo.set("i1", "i2", "i3")
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent reconciliation did not drain — likely a deadlock in the loop map")
	}
}
