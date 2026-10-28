package pmsd

import (
	"context"
	"sync"
	"time"
)

// applierWake lets the worker, in the same process, wake an Interface's idle applier loop the moment an event
// becomes claimable (a LIVE admission, a published generation, a reconciliation), instead of the loop finding it
// on its next idle poll. Room sign-in is refused while a claimable event waits, so the idle pause was pure closure
// time (performance audit D6-H1 step 1). The poll stays as the fallback; a wake carries no data and losing one is
// harmless.
type applierWake struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

func newApplierWake() *applierWake { return &applierWake{ch: map[string]chan struct{}{}} }

func (w *applierWake) chanFor(iface string) chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.ch[iface]
	if c == nil {
		c = make(chan struct{}, 1)
		w.ch[iface] = c
	}
	return c
}

// notify never blocks; a nil hub (tests, dark paths) is inert.
func (w *applierWake) notify(iface string) {
	if w == nil {
		return
	}
	select {
	case w.chanFor(iface) <- struct{}{}:
	default: // a wake is already pending
	}
}

// wait sleeps up to d, returning early on a wake for iface or when ctx ends.
func (w *applierWake) wait(ctx context.Context, iface string, d time.Duration) {
	if w == nil {
		sleepCtx(ctx, d)
		return
	}
	c := w.chanFor(iface)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-c:
	}
}
