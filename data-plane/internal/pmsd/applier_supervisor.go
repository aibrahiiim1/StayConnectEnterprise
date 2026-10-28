package pmsd

// The applier reconciles its loops against the ACTIVE Interface set: new Interfaces get a loop, ones that stop
// being ACTIVE are cancelled and drained, and exactly one loop exists per Interface at any moment.

import (
	"context"
	"sync"
	"time"
)

// defaultApplierReconcileInterval is a poll, not a notification, because the authoritative answer lives in the database.
const defaultApplierReconcileInterval = 15 * time.Second

// runningLoop is one Interface's live loop; generation increases per creation, distinguishing a replacement from a duplicate.
type runningLoop struct {
	cancel     context.CancelFunc
	done       chan struct{}
	generation int
}

// loopObserver lets a test watch the loop lifecycle from outside the supervisor; production leaves it nil.
type loopObserver interface {
	loopStarted(iface string, generation int)
	loopStopped(iface string, generation int)
}

func runApplierSupervisor(ctx context.Context, a Assignment, repo Repo, ap StayApplier, deps *Deps) {
	runApplierSupervisorObserved(ctx, a, repo, ap, deps, nil)
}

func runApplierSupervisorObserved(ctx context.Context, a Assignment, repo Repo, ap StayApplier, deps *Deps, obs loopObserver) {
	var mu sync.Mutex
	loops := map[string]*runningLoop{}
	generations := map[string]int{} // per-Interface loop generation, monotonic for the life of the supervisor

	stopAll := func() {
		mu.Lock()
		defer mu.Unlock()
		for id, l := range loops {
			l.cancel()
			<-l.done
			delete(loops, id)
		}
	}
	defer stopAll()

	reconcile := func() {
		ifaces, err := repo.ListActiveInterfaces(ctx, a.TenantID, a.SiteID)
		if err != nil {
			// A discovery failure is transient: keep the running loops and retry on the next tick.
			logEvent(deps.log(), EventSupervisorNoAssignment, Classify(err), SafeFields{Stage: StageDiscover})
			return
		}
		want := make(map[string]struct{}, len(ifaces))
		for _, i := range ifaces {
			// The listing also carries DRAINING interfaces (room charges to finish); stay events apply for ACTIVE only.
			if i.LifecycleState != "" && i.LifecycleState != "ACTIVE" {
				continue
			}
			want[i.ID] = struct{}{}
		}
		expireDueReviews(ctx, a, repo, deps)

		mu.Lock()
		defer mu.Unlock()
		for id, l := range loops {
			if _, ok := want[id]; ok {
				continue
			}
			l.cancel()
			mu.Unlock()
			<-l.done // drain outside the lock so a slow drain cannot block an unrelated Interface
			mu.Lock()
			delete(loops, id)
		}
		// The map is the single source of truth for "is there a loop", so concurrent reconciles cannot duplicate one.
		for id := range want {
			if _, exists := loops[id]; exists {
				continue
			}
			lctx, cancel := context.WithCancel(ctx)
			generations[id]++
			l := &runningLoop{cancel: cancel, done: make(chan struct{}), generation: generations[id]}
			loops[id] = l
			if obs != nil {
				obs.loopStarted(id, l.generation)
			}
			go func(id string, l *runningLoop) {
				defer func() {
					if obs != nil {
						obs.loopStopped(id, l.generation)
					}
					close(l.done)
				}()
				runApplierScope(lctx, ap, applierScope{Tenant: a.TenantID, Site: a.SiteID, Interface: id},
					applierConfig{wake: deps.wake}, deps.log())
			}(id, l)
		}
	}

	reconcile()
	every := deps.ApplierReconcileInterval
	if every <= 0 {
		every = defaultApplierReconcileInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile()
		}
	}
}
