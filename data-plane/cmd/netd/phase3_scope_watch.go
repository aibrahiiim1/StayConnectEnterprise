package main

// netd RE-READS ITS PHASE-3 SCOPE WHILE IT RUNS (audit B-11).
//
// The mode -- whether Phase 3 is live, and for which tenant, site, appliance and assignment generation -- was
// resolved once at start. scd adopts a new signed assignment at runtime (Central reassigns the appliance, or the
// first assignment arrives after enrollment), acctd follows it, and netd went on comparing every submitted plan
// with the scope it had read at boot: every plan was refused as out of scope, and every guest went offline when
// their bounded lease ran out, until somebody restarted netd.
//
// So the scope is re-resolved on a short cadence from the same sources as at start (phase3_mode.go). A change is
// adopted under the writer's lock, so no plan is judged half against one scope and half against the other.
// Becoming live still needs everything starting live needs: an authenticated producer, and the Phase-3 writer
// boundary installed in the database. If either is missing the appliance stays dark and says why -- it never
// starts accepting plans from anyone.

import (
	"context"
	"log/slog"
	"time"
)

// scopeWatch re-resolves the Phase-3 mode.
type scopeWatch struct {
	p *phase3Shaping
	// load resolves the mode exactly as startup does.
	load func(ctx context.Context) (phase3Mode, error)
	// verifyBoundary checks the database-side writer boundary before the appliance goes live.
	verifyBoundary func(ctx context.Context) error
}

// check adopts a changed scope. It returns whether the mode changed.
func (w scopeWatch) check(ctx context.Context) bool {
	next, err := w.load(ctx)
	if err != nil {
		// Unreadable configuration is not a new scope. Keep the one in force and say so.
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
