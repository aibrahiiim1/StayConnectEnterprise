package pmsd

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingApplier struct {
	pending atomic.Int32
	applied atomic.Int32
	calls   atomic.Int32
}

func (a *countingApplier) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	a.calls.Add(1)
	if a.pending.Load() > 0 {
		a.pending.Add(-1)
		a.applied.Add(1)
		return true, nil
	}
	return false, nil
}

// Performance audit D6-H1 step 1: an idle applier applies a newly claimable event as soon as the worker reports it,
// not after its idle pause.
func TestApplierWake_IdleLoopWakesOnNotify(t *testing.T) {
	wake := newApplierWake()
	ap := &countingApplier{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runApplierScope(ctx, ap, applierScope{Interface: "i1"}, applierConfig{IdlePause: time.Hour, wake: wake}, nil)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for ap.calls.Load() == 0 { // the loop has found the inbox empty and is idle
		if time.Now().After(deadline) {
			t.Fatal("applier loop never ran")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	ap.pending.Store(1)
	wake.notify("i2") // another interface's wake does not concern this loop
	time.Sleep(20 * time.Millisecond)
	if ap.applied.Load() != 0 {
		t.Fatal("a wake for another interface woke this loop")
	}
	start := time.Now()
	wake.notify("i1")
	for ap.applied.Load() == 0 {
		if time.Since(start) > time.Second {
			t.Fatal("the wake did not end the idle pause")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

// Admission of a LIVE record and a published generation both wake the applier.
func TestApplierWake_WorkerNotifies(t *testing.T) {
	s, _, _ := newBarrierSink(t)
	s.w.deps.wake = newApplierWake()
	c := s.w.deps.wake.chanFor(s.w.iface.ID)
	drained := func() bool {
		select {
		case <-c:
			return true
		default:
			return false
		}
	}
	ctx := context.Background()
	if err := s.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.OnDomainEvent(ctx, ev("S1")); err != nil {
		t.Fatal(err)
	}
	if drained() {
		t.Fatal("a staged record is not claimable and must not wake the applier")
	}
	if err := s.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if !drained() {
		t.Fatal("a published generation must wake the applier")
	}
	if err := s.OnDomainEvent(ctx, ev("L1")); err != nil {
		t.Fatal(err)
	}
	if !drained() {
		t.Fatal("an admitted LIVE record must wake the applier")
	}
}
