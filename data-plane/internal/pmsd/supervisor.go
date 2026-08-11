package pmsd

import (
	"context"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

var errAssignmentChanged = &typedErr{code: CodeAssignmentChanged}

// supervisor runs a single-goroutine reconcile loop over the ACTIVE PMS Interfaces within the assigned
// Tenant/Site scope, keeping exactly one worker per Interface. It compares the COMPLETE desired identity
// (interface id + current revision id), contains worker panics (logging only a closed event, never the
// recovered value), and drains every worker before returning. The workers map is owned by run()'s single
// goroutine, so no lock is needed.
type supervisor struct {
	cfg     iamv2.PMSConfig
	assign  Assignment
	repo    Repo
	deps    *Deps
	workers map[string]*worker // keyed by interface id
	desired map[string]string  // interface id -> current revision id (complete-identity comparison)
}

func newSupervisor(cfg iamv2.PMSConfig, assign Assignment, repo Repo, deps *Deps) *supervisor {
	return &supervisor{
		cfg: cfg, assign: assign, repo: repo, deps: deps,
		workers: map[string]*worker{}, desired: map[string]string{},
	}
}

func (s *supervisor) run(ctx context.Context) error {
	defer s.stopAll()
	t := time.NewTicker(s.deps.ReconcileInterval)
	defer t.Stop()
	if err := s.reconcile(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := s.reconcile(ctx); err != nil {
				return err
			}
		}
	}
}

// reconcile re-verifies the assignment, lists the assigned scope's ACTIVE interfaces, and converges the
// worker set to the complete desired identity. An assignment change returns errAssignmentChanged so Run can
// drain + re-scope.
func (s *supervisor) reconcile(ctx context.Context) error {
	if a, assigned, err := s.deps.LoadAssignment(ctx); err == nil {
		if !assigned || a.TenantID != s.assign.TenantID || a.SiteID != s.assign.SiteID || a.ApplianceID != s.assign.ApplianceID {
			return errAssignmentChanged
		}
	}
	ifaces, err := s.repo.ListActiveInterfaces(ctx, s.assign.TenantID, s.assign.SiteID)
	if err != nil {
		logEvent(s.deps.log(), EventSupervisorReconcileErr, Classify(err), SafeFields{Stage: StageDiscover})
		return nil // transient; retry next tick (do not tear down healthy workers)
	}
	next := map[string]struct{}{}
	for _, iface := range ifaces {
		if iface.TenantID != s.assign.TenantID || iface.SiteID != s.assign.SiteID {
			continue // defense in depth: never accept a cross-scope row
		}
		if iface.LifecycleState != "ACTIVE" {
			continue
		}
		next[iface.ID] = struct{}{}
		if w, ok := s.workers[iface.ID]; ok {
			if s.desired[iface.ID] == iface.CurrentRevisionID {
				continue
			}
			w.stop(s.deps.StopGrace) // complete identity (revision) changed → drain and replace
			delete(s.workers, iface.ID)
		}
		s.startWorker(ctx, iface)
	}
	for id, w := range s.workers {
		if _, keep := next[id]; !keep {
			w.stop(s.deps.StopGrace)
			delete(s.workers, id)
			delete(s.desired, id)
		}
	}
	return nil
}

func (s *supervisor) startWorker(parent context.Context, iface Interface) {
	wctx, cancel := context.WithCancel(parent)
	w := &worker{iface: iface, repo: s.repo, deps: s.deps, cancel: cancel, done: make(chan struct{})}
	s.workers[iface.ID] = w
	s.desired[iface.ID] = iface.CurrentRevisionID
	go func() {
		defer close(w.done)
		defer func() {
			if r := recover(); r != nil {
				// log ONLY a closed event + safe fields; never render the recovered panic value
				_ = r
				logEvent(s.deps.log(), EventWorkerPanicRecovered, CodePanicRecovered,
					SafeFields{InterfaceID: NewUUIDValue(iface.ID), Generation: w.gen, Stage: StageServe, Attempt: w.attempt})
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
	s.desired = map[string]string{}
}

// backoff is a bounded exponential backoff with full jitter.
type backoff struct {
	min, max time.Duration
	cur      time.Duration
	rnd      func(n int64) int64
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
