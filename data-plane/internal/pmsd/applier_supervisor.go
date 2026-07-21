package pmsd

// SUPERVISED RECONCILIATION of the Stay-Event application loops.
//
// Interfaces are not static: one is published, another is put into DRAINING before decommissioning, a third is
// re-activated after maintenance. Listing them once at startup meant a new Interface silently accumulated
// unapplied events until somebody restarted the daemon, and a decommissioned one kept a worker running against
// a scope nobody owned any more.
//
// So the applier reconciles, on the same authoritative discovery the connector supervision uses:
//
//   - an Interface that appears gets a loop, without touching any other Interface;
//   - an Interface that stops being ACTIVE has its loop cancelled and drained, cleanly;
//   - an Interface that comes back gets a fresh loop (never a resurrected one);
//   - exactly one loop exists per Interface at any moment — reconciling twice concurrently cannot double it;
//   - the durable inbox stays the source of truth, so a stopped-and-restarted loop simply resumes the events
//     that are still PENDING.

import (
	"context"
	"sync"
	"time"
)

// defaultApplierReconcileInterval bounds how quickly an Interface change is picked up. It is a poll rather
// than a notification because the authoritative answer lives in the database, and a poll cannot miss an edge.
const defaultApplierReconcileInterval = 15 * time.Second

// runningLoop is one Interface's live application loop.
type runningLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// runApplierSupervisor keeps the set of application loops equal to the set of ACTIVE Interfaces until ctx ends,
// then cancels and drains every loop it started.
func runApplierSupervisor(ctx context.Context, a Assignment, repo Repo, ap StayApplier, deps *Deps) {
	var mu sync.Mutex
	loops := map[string]*runningLoop{}

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
			// A discovery failure is transient by nature: keep the loops we have (they are still applying
			// real events) and try again on the next tick rather than tearing down working application.
			logEvent(deps.log(), EventSupervisorNoAssignment, Classify(err), SafeFields{Stage: StageDiscover})
			return
		}
		want := make(map[string]struct{}, len(ifaces))
		for _, i := range ifaces {
			want[i.ID] = struct{}{}
		}

		mu.Lock()
		defer mu.Unlock()
		// stop loops whose Interface is no longer ACTIVE (disabled, draining, decommissioned or removed)
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
		// start loops for Interfaces that do not have one. The map is the single source of truth for "is there
		// already a loop", so a second concurrent reconcile cannot create a duplicate.
		for id := range want {
			if _, exists := loops[id]; exists {
				continue
			}
			lctx, cancel := context.WithCancel(ctx)
			l := &runningLoop{cancel: cancel, done: make(chan struct{})}
			loops[id] = l
			go func(id string, l *runningLoop) {
				defer close(l.done)
				runApplierScope(lctx, ap, applierScope{Tenant: a.TenantID, Site: a.SiteID, Interface: id},
					applierConfig{}, deps.log())
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
