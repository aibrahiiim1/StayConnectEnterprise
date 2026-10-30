package main

// Outage continuity, producer side (docs/architecture/ONEGATE_OUTAGE_CONTINUITY.md). While acctd is healthy every plan carries
// a continuity block: per-session hard bounds and per-entitlement byte budgets, which netd turns into a bounded lease if plans
// stop. The block is sent only while accounting is demonstrably current; an acctd whose accounting has stalled stops vouching
// for budgets, and netd then gives its guests the strict lease.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	// contRefresh is how often the budgets are re-read from the database.
	contRefresh = 30 * time.Second
	// contAccountingFresh: the block is withheld unless the last accounting pass completed this recently, because the budget
	// is only as true as the usage the database has counted.
	contAccountingFresh = 30 * time.Second
	contSettingRefresh  = 30 * time.Second
)

func isUndefinedColumn(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42703"
}

// readContinuityMinutes reads the setting; found is false on an older schema.
func readContinuityMinutes(ctx context.Context, db rowQuerier) (minutes int, found bool, err error) {
	err = db.QueryRow(ctx, `SELECT outage_continuity_minutes FROM public.enforcement_policy LIMIT 1`).Scan(&minutes)
	if err == nil {
		if minutes < 0 {
			minutes = 0
		}
		if minutes > shapeplan.MaxContinuityWindowSeconds/60 {
			minutes = shapeplan.MaxContinuityWindowSeconds / 60
		}
		return minutes, true, nil
	}
	if err == pgx.ErrNoRows || isUndefinedTable(err) || isUndefinedColumn(err) {
		return 0, false, nil
	}
	return 0, false, err
}

// watchContinuity keeps contWindow in step with the stored setting. A failed read keeps the value in force.
func (p *phase3) watchContinuity(ctx context.Context, db rowQuerier, every time.Duration) {
	if p == nil {
		return
	}
	read := func() {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		m, found, err := readContinuityMinutes(rctx, db)
		if err != nil {
			slog.Warn("acctd: the outage-continuity setting could not be read; the value in force is kept", "err", err)
			return
		}
		if !found {
			return
		}
		p.contKnown.Store(true)
		if int64(m)*60 != p.contWindow.Load() {
			slog.Info("acctd: outage-continuity window changed", "from_min", p.contWindow.Load()/60, "to_min", m)
			p.contWindow.Store(int64(m) * 60)
			p.smu.Lock()
			p.contBlock = nil // a changed setting is sent at once
			p.smu.Unlock()
		}
	}
	read()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			read()
		}
	}
}

// continuityFor returns the block for this submission, or nil (strict) when the window is zero, accounting is not current,
// or the budgets cannot be read.
func (p *phase3) continuityFor(ctx context.Context, plan enforce.Plan, now time.Time) *shapeplan.Continuity {
	if p == nil || p.enf == nil {
		return nil
	}
	secs := p.contWindow.Load()
	if secs <= 0 {
		if p.contKnown.Load() {
			return shapeplan.OffContinuity() // the operator's 0 is stated, so netd drops what it holds
		}
		return nil // setting not read yet: say nothing rather than switch continuity off by accident
	}
	p.smu.Lock()
	blk, at := p.contBlock, p.contAt
	p.smu.Unlock()
	if p.acctDegraded != "" || p.lastPassOK.IsZero() || now.Sub(p.lastPassOK) > contAccountingFresh {
		return nil
	}
	if blk != nil && now.Sub(at) < contRefresh && blk.WindowSeconds == int(secs) {
		return blk
	}
	nb, err := p.enf.ContinuityFor(ctx, plan, time.Duration(secs)*time.Second)
	if err != nil {
		slog.Warn("phase3: the continuity budgets could not be read; this plan carries none", "err", err)
		return nil
	}
	p.smu.Lock()
	p.contBlock, p.contAt = nb, now
	p.smu.Unlock()
	return nb
}
