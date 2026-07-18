package pmsd

import (
	"context"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// supervisor reconciles the desired active-Interface set to one independent worker each. It runs the
// reconcile loop in a single goroutine (the workers map is owned by run()), so no lock is needed for the
// map; each worker owns its own goroutine and lifecycle.
type supervisor struct {
	cfg     iamv2.PMSConfig
	repo    Repo
	deps    *Deps
	workers map[string]*worker
}

func newSupervisor(cfg iamv2.PMSConfig, repo Repo, deps *Deps) *supervisor {
	return &supervisor{cfg: cfg, repo: repo, deps: deps, workers: map[string]*worker{}}
}

func (s *supervisor) run(ctx context.Context) error {
	t := time.NewTicker(s.deps.ReconcileInterval)
	defer t.Stop()
	s.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return nil
		case <-t.C:
			s.reconcile(ctx)
		}
	}
}

func (s *supervisor) reconcile(ctx context.Context) {
	ifaces, err := s.repo.ListActiveInterfaces(ctx)
	if err != nil {
		s.deps.log().Warn("pmsd: reconcile: list interfaces failed", "err", sanitize(err))
		return
	}
	desired := make(map[string]Interface, len(ifaces))
	for _, i := range ifaces {
		if i.LifecycleState == "ACTIVE" {
			desired[i.ID] = i
		}
	}
	// stop workers no longer desired (drain)
	for id, w := range s.workers {
		if _, ok := desired[id]; !ok {
			w.stop(s.deps.StopGrace)
			delete(s.workers, id)
		}
	}
	// start workers for newly-desired interfaces (no duplicate worker per interface)
	for id, iface := range desired {
		if _, ok := s.workers[id]; ok {
			continue
		}
		s.startWorker(ctx, iface)
	}
}

func (s *supervisor) startWorker(parent context.Context, iface Interface) {
	wctx, cancel := context.WithCancel(parent)
	w := &worker{iface: iface, repo: s.repo, deps: s.deps, cancel: cancel, done: make(chan struct{})}
	s.workers[iface.ID] = w
	go func() {
		defer close(w.done)
		// contain a panic in one worker without killing the supervisor or unrelated workers
		defer func() {
			if r := recover(); r != nil {
				s.deps.log().Error("pmsd: worker panic contained", "interface", iface.ID, "panic", r)
			}
		}()
		w.run(wctx)
	}()
}

func (s *supervisor) stopAll() {
	var wg sync.WaitGroup
	for _, w := range s.workers {
		wg.Add(1)
		go func(w *worker) { defer wg.Done(); w.stop(s.deps.StopGrace) }(w)
	}
	wg.Wait()
	s.workers = map[string]*worker{}
}

// backoff is a bounded exponential backoff with full jitter.
type backoff struct {
	min, max time.Duration
	cur      time.Duration
	rnd      func(n int64) int64 // injectable for deterministic tests
}

func newBackoff(min, max time.Duration, rnd func(int64) int64) *backoff {
	return &backoff{min: min, max: max, cur: 0, rnd: rnd}
}
func (b *backoff) reset() { b.cur = 0 }
func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = b.min
	} else {
		b.cur *= 2
		if b.cur > b.max {
			b.cur = b.max
		}
	}
	// full jitter in [0, cur]
	if b.rnd == nil {
		return b.cur
	}
	j := b.rnd(int64(b.cur))
	if j < 0 {
		j = -j
	}
	if int64(b.cur) > 0 {
		j = j % int64(b.cur)
	}
	return time.Duration(j)
}
