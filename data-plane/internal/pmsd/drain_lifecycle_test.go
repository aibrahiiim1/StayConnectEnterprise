package pmsd

import (
	"context"
	"testing"
	"time"
)

// AUDIT H-8: WIND DOWN DRAINS, PAUSE DOES NOT KEEP THE LINK.
//
// The repository lists a DRAINING interface only while it still has room charges to finish
// (iam_v2.pms_interface_has_undrained_postings). Such an interface must keep its worker -- and so its PMS link
// -- until the outbox is empty. It used to be dropped like any non-ACTIVE interface, so queued charges could
// never be sent.

func TestWorkerLifecycle(t *testing.T) {
	for state, want := range map[string]bool{
		"ACTIVE": true, "DRAINING": true, "AUTH_DISABLED": false, "DECOMMISSIONED": false, "": false,
	} {
		if got := workerLifecycle(state); got != want {
			t.Errorf("workerLifecycle(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestDrainingInterfaceWithQueuedChargesKeepsItsLink(t *testing.T) {
	var sp spies
	draining := iface("i1")
	draining.LifecycleState = "DRAINING"
	repo := newFakeRepo(draining)
	repo.loadFn = func(id string) (Interface, Revision, SecretGeneration, error) {
		return draining, validRevision(), validSecret(), nil
	}
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = Run(ctx, connectorOn(), deps); close(done) }()
	waitFor(t, time.Second, func() bool { return sp.dial.Load() >= 1 })
	cancel()
	<-done
}

func TestPausedInterfaceIsNotDialed(t *testing.T) {
	var sp spies
	paused := iface("i1")
	paused.LifecycleState = "AUTH_DISABLED"
	repo := newFakeRepo(paused)
	deps := assignedDeps(&sp, repo, func() Locker { return newFakeLocker(true) }, func() *fakeConn { return &fakeConn{} })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = Run(ctx, connectorOn(), deps)
	if sp.dial.Load() != 0 {
		t.Fatalf("a paused interface must not be dialed by the supervisor; dials=%d", sp.dial.Load())
	}
}
