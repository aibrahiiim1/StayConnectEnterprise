package pmsd

// Composition-root tests for the Stay-Event application worker: what the DAEMON does, not what the packages
// can do. These are the tests that would have caught "the engine exists but nothing runs it".

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeApplier records what the worker asked it to do.
type fakeApplier struct {
	mu       sync.Mutex
	calls    map[string]int
	remain   map[string]int // events still pending per interface
	failWith map[string]error
}

func newFakeApplier() *fakeApplier {
	return &fakeApplier{calls: map[string]int{}, remain: map[string]int{}, failWith: map[string]error{}}
}

func (f *fakeApplier) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[iface]++
	if err, ok := f.failWith[iface]; ok {
		return false, err
	}
	if f.remain[iface] > 0 {
		f.remain[iface]--
		return true, nil
	}
	return false, nil
}

func (f *fakeApplier) callsFor(iface string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[iface]
}

// fakeRepo is the minimum of Repo the applier path uses.
type applierRepo struct {
	Repo
	ifaces []Interface
	listed atomic.Int32
	err    error
}

func (r *applierRepo) ListActiveInterfaces(ctx context.Context, tenant, site string) ([]Interface, error) {
	r.listed.Add(1)
	return r.ifaces, r.err
}
func (r *applierRepo) Close() error { return nil }

// While dark, the applier is NEVER constructed: no database handle, no goroutine, no work.
func TestApplierNotConstructedWhileDark(t *testing.T) {
	var built atomic.Int32
	deps := &Deps{
		Log: quietLog(),
		NewStayApplier: func(ctx context.Context, a Assignment) (StayApplier, error) {
			built.Add(1)
			return newFakeApplier(), nil
		},
	}
	repo := &applierRepo{}
	for _, cfg := range []iamv2.PMSConfig{
		{},                    // everything off
		{MasterEnabled: true}, // master on, ingest off
		{MasterEnabled: true, PMSConnectorEnabled: true}, // connector only
	} {
		ap, err := buildStayApplier(context.Background(), cfg, Assignment{TenantID: "t", SiteID: "s"}, deps)
		if err != nil {
			t.Fatalf("dark build returned an error: %v", err)
		}
		done := startStayApplier(context.Background(), cfg, Assignment{TenantID: "t", SiteID: "s"}, repo, ap, deps)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("dark applier did not return immediately for %+v", cfg)
		}
	}
	if built.Load() != 0 {
		t.Fatalf("the applier was constructed %d times while dark", built.Load())
	}
	if repo.listed.Load() != 0 {
		t.Fatal("a dark applier touched the repository")
	}
}

// The ingest surface without an applier is a startup failure, not a silently unapplied inbox.
func TestRunFailsClosedWithoutAnApplier(t *testing.T) {
	cfg := iamv2.PMSConfig{MasterEnabled: true, PMSIngestEnabled: true}
	err := Run(context.Background(), cfg, Deps{Log: quietLog()})
	if !errors.Is(err, ErrApplierRequired) {
		t.Fatalf("err = %v, want ErrApplierRequired", err)
	}
}

// Every assigned Interface gets its own loop, and each drains its own backlog.
func TestApplierRunsOneLoopPerInterface(t *testing.T) {
	ap := newFakeApplier()
	ap.remain["i1"] = 3
	ap.remain["i2"] = 5
	repo := &applierRepo{ifaces: []Interface{{ID: "i1"}, {ID: "i2"}}}
	deps := &Deps{Log: quietLog(), NewStayApplier: func(ctx context.Context, a Assignment) (StayApplier, error) { return ap, nil }}
	cfg := iamv2.PMSConfig{MasterEnabled: true, PMSIngestEnabled: true}

	ctx, cancel := context.WithCancel(context.Background())
	ap2, err := buildStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	done := startStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, repo, ap2, deps)
	// wait until both backlogs are drained
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ap.mu.Lock()
		drained := ap.remain["i1"] == 0 && ap.remain["i2"] == 0
		ap.mu.Unlock()
		if drained {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the applier did not drain on shutdown")
	}
	if ap.callsFor("i1") < 3 || ap.callsFor("i2") < 5 {
		t.Fatalf("backlogs were not drained: i1=%d i2=%d", ap.callsFor("i1"), ap.callsFor("i2"))
	}
}

// One Interface failing must not stop another: its loop backs off and keeps going while the healthy one works.
func TestOneInterfaceFailureDoesNotStallAnother(t *testing.T) {
	ap := newFakeApplier()
	ap.failWith["bad"] = errors.New("interface is down")
	ap.remain["good"] = 4
	repo := &applierRepo{ifaces: []Interface{{ID: "bad"}, {ID: "good"}}}
	deps := &Deps{Log: quietLog(), NewStayApplier: func(ctx context.Context, a Assignment) (StayApplier, error) { return ap, nil }}
	cfg := iamv2.PMSConfig{MasterEnabled: true, PMSIngestEnabled: true}

	ctx, cancel := context.WithCancel(context.Background())
	ap2, err := buildStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	done := startStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, repo, ap2, deps)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ap.mu.Lock()
		drained := ap.remain["good"] == 0
		ap.mu.Unlock()
		if drained {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if ap.remain["good"] != 0 {
		t.Fatal("the healthy interface was stalled by the failing one")
	}
	if ap.callsFor("bad") == 0 {
		t.Fatal("the failing interface was never attempted")
	}
}

// A shutdown request stops the loops promptly and drains them.
func TestApplierDrainsOnShutdown(t *testing.T) {
	ap := newFakeApplier()
	repo := &applierRepo{ifaces: []Interface{{ID: "i1"}}}
	deps := &Deps{Log: quietLog(), NewStayApplier: func(ctx context.Context, a Assignment) (StayApplier, error) { return ap, nil }}
	cfg := iamv2.PMSConfig{MasterEnabled: true, PMSIngestEnabled: true}
	ctx, cancel := context.WithCancel(context.Background())
	ap2, err := buildStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	done := startStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, repo, ap2, deps)
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the applier did not drain within the shutdown window")
	}
}

// A failed application must not be treated as progress: the event stays pending and is retried.
func TestFailedApplicationIsRetriedNotDropped(t *testing.T) {
	ap := newFakeApplier()
	ap.failWith["i1"] = errors.New("transient")
	repo := &applierRepo{ifaces: []Interface{{ID: "i1"}}}
	deps := &Deps{Log: quietLog(), NewStayApplier: func(ctx context.Context, a Assignment) (StayApplier, error) { return ap, nil }}
	cfg := iamv2.PMSConfig{MasterEnabled: true, PMSIngestEnabled: true}
	ctx, cancel := context.WithCancel(context.Background())
	ap2, err := buildStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	done := startStayApplier(ctx, cfg, Assignment{TenantID: "t", SiteID: "s"}, repo, ap2, deps)
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if ap.callsFor("i1") < 1 {
		t.Fatal("a failing scope stopped attempting entirely")
	}
}
