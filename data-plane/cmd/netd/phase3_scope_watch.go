package main

// The scope is re-resolved periodically because scd adopts new signed assignments at runtime; a stale scope
// would refuse every plan. A change is adopted under the writer's lock. Going live still needs an
// authenticated producer and the installed writer boundary, otherwise the appliance stays dark.

import (
	"context"
	"log/slog"
	"time"
)

type scopeWatch struct {
	p              *phase3Shaping
	load           func(ctx context.Context) (phase3Mode, error)
	verifyBoundary func(ctx context.Context) error
}

// check returns whether the mode changed.
func (w scopeWatch) check(ctx context.Context) bool {
	next, err := w.load(ctx)
	if err != nil {
		slog.Error("netd: the Phase-3 scope could not be re-read; keeping the scope in force", "err", err)
		return false
	}
	p := w.p
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.mode
	if next == cur {
		return false
	}
	if next.Active && !cur.Active {
		if !p.authz.configured {
			slog.Error("netd: Phase 3 became live but NETD_PHASE3_PRODUCER_UID is unset; staying dark")
			return false
		}
		if w.verifyBoundary != nil {
			if err := w.verifyBoundary(ctx); err != nil {
				slog.Error("netd: Phase 3 became live but the writer boundary is not installed; staying dark", "err", err)
				return false
			}
		}
	}
	p.mode = next
	if e, ok := p.enforcement.(*pgEnforcement); ok {
		e.tenant, e.site = next.TenantID, next.SiteID
	}
	slog.Info("netd: Phase-3 scope changed while running; adopted",
		"was_active", cur.Active, "active", next.Active,
		"was_tenant", cur.TenantID, "tenant", next.TenantID,
		"was_site", cur.SiteID, "site", next.SiteID,
		"was_assignment_gen", cur.AssignGen, "assignment_gen", next.AssignGen)
	return true
}

func (w scopeWatch) loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.check(ctx)
		}
	}
}
