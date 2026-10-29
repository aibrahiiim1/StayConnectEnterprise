package main

// AD-47 (a): acctd's one serial 1 s loop is split into three independent tickers, and the period is a setting.
//
//	accounting  measures every online device, then applies the plan's session limits, the retention purge and the
//	            aggregate sweep. It is the slow one (a write per online device) and the only one that must keep its order.
//	expiry      ends entitlements whose window or quota has run out. One query when nothing is due, so a slow accounting
//	            pass can no longer delay a time-based cut-off.
//	submitter   derives the NEWEST desired state and hands it to netd. It coalesces: if a submission is still running when
//	            the next is due (or kicked) there is one more, never a queue, and it always plans from current durable state.
//
// The period is public.enforcement_policy.cutoff_seconds (default 5, 1..60), read every 30 s, so a hotel changes how soon a
// used-up allowance cuts a guest off without a restart. An unreadable setting (older schema, database down) keeps the previous
// period and, before the first successful read, ACCTD_TICK_SECONDS / 1 s: exactly the earlier behaviour.
//
// The plan submitter never waits longer than maxTickSeconds whatever the cut-off period, because netd renews a lease only
// under a current plan (planValidity). Whenever an expiry or a session limit ends something the submitter is kicked at once,
// so the cut-off reaches netd in well under a period.

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/livez"
)

const (
	minCutoffSeconds = 1
	maxCutoffSeconds = 60
	// cutoffRefreshEvery is how often the setting is re-read.
	cutoffRefreshEvery = 30 * time.Second
)

// clampCutoff keeps a stored value inside the range the database also enforces.
func clampCutoff(n int) int {
	if n < minCutoffSeconds {
		return minCutoffSeconds
	}
	if n > maxCutoffSeconds {
		return maxCutoffSeconds
	}
	return n
}

// submitPeriod is how often the plan is delivered: the cut-off period, but never beyond what netd's leases tolerate.
func submitPeriod(cutoff time.Duration) time.Duration {
	if max := time.Duration(maxTickSeconds) * time.Second; cutoff > max {
		return max
	}
	return cutoff
}

func isUndefinedTable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42P01"
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readCutoffSeconds reads the setting; found is false when the table or row does not exist (an older schema).
func readCutoffSeconds(ctx context.Context, db rowQuerier) (seconds int, found bool, err error) {
	err = db.QueryRow(ctx, `SELECT cutoff_seconds FROM public.enforcement_policy LIMIT 1`).Scan(&seconds)
	if err == nil {
		return clampCutoff(seconds), true, nil
	}
	if err == pgx.ErrNoRows || isUndefinedTable(err) {
		return 0, false, nil
	}
	return 0, false, err
}

// cutoffClock holds the period the three tickers re-arm with.
type cutoffClock struct{ ns atomic.Int64 }

func newCutoffClock(initial time.Duration) *cutoffClock {
	c := &cutoffClock{}
	c.ns.Store(int64(initial))
	return c
}

func (c *cutoffClock) period() time.Duration { return time.Duration(c.ns.Load()) }

func (c *cutoffClock) set(d time.Duration) { c.ns.Store(int64(d)) }

// watch keeps the clock in step with the stored setting.
func (c *cutoffClock) watch(ctx context.Context, db rowQuerier, every time.Duration) {
	read := func() {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		secs, found, err := readCutoffSeconds(rctx, db)
		if err != nil {
			slog.Warn("acctd: the cut-off period could not be read; the period in force is kept", "err", err)
			return
		}
		if !found {
			return
		}
		if d := time.Duration(secs) * time.Second; d != c.period() {
			slog.Info("acctd: cut-off period changed", "from_s", int(c.period()/time.Second), "to_s", secs)
			c.set(d)
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

// ticker runs fn once per period, re-reading the period each cycle. fn never overlaps itself (one goroutine), and a cycle
// that overruns its period is followed by the next at once, not by a backlog.
func ticker(ctx context.Context, period func() time.Duration, fn func(now time.Time)) {
	for {
		t := time.NewTimer(period())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case now := <-t.C:
			fn(now)
		}
	}
}

// submitter owns plan delivery. kick requests an immediate submission; many kicks while one runs coalesce into one more.
type submitter struct {
	kickCh chan struct{}
	run    func()
}

func newSubmitter(run func()) *submitter { return &submitter{kickCh: make(chan struct{}, 1), run: run} }

func (s *submitter) kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

func (s *submitter) loop(ctx context.Context, period func() time.Duration) {
	for {
		t := time.NewTimer(period())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.kickCh:
			t.Stop()
		case <-t.C:
		}
		s.run()
	}
}

// runLoops starts the three tickers and blocks until ctx ends.
func (a *acctd) runLoops(ctx context.Context, c cfg, clock *cutoffClock, netd planSubmitter, counters counterReader, epochs epochSource, aggOwner interface{ sweep(context.Context) }) {
	p3 := a.p3
	sub := newSubmitter(func() { p3.reconcileShaping(ctx, netd, c.LegacyBridge) })

	go clock.watch(ctx, a.db, cutoffRefreshEvery)
	go sub.loop(ctx, func() time.Duration { return submitPeriod(clock.period()) })

	// Expiry: independent of how long accounting takes.
	go ticker(ctx, clock.period, func(time.Time) {
		if p3.enforceExpiries(ctx) > 0 {
			sub.kick()
		}
	})

	slog.Info("acctd started", "cutoff_s", int(clock.period()/time.Second))
	// Accounting keeps the order that matters: measure first so this cycle's usage is attributed before anything is closed
	// out, then the session limits, the purge and the aggregate sweep.
	ticker(ctx, clock.period, func(now time.Time) {
		if n := p3.accountingPass(ctx, counters, epochs, c.LegacyBridge, now); n > 0 {
			slog.Debug("phase3: accounting samples ingested", "count", n)
		}
		p3.enforceSessionLimits(ctx, now)
		p3.purgeAccountingDetail(ctx, now)
		aggOwner.sweep(ctx)
		sub.kick() // a limit may have ended a session: tell netd now, not at the next period
		// The heartbeat proves the accounting loop is progressing; the report says why it is degraded, since a loop
		// whose every observation is refused is not healthy.
		livez.Touch("acctd")
		livez.Report("acctd", p3.degradedSummary())
	})
}
