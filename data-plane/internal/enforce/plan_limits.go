package enforce

// Plan session limits (idle_timeout_seconds, max_continuous_session_seconds) end only the session, not the
// entitlement, so the device can sign in again. The database decides via iam_v2.p6_end_sessions_over_plan_limits.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type SessionEnd struct {
	SessionID     string
	EntitlementID string
	Reason        string // IDLE_TIMEOUT | MAX_SESSION_DURATION
	At            time.Time
}

const (
	EndReasonIdleTimeout        = "IDLE_TIMEOUT"
	EndReasonMaxSessionDuration = "MAX_SESSION_DURATION"
)

// EnforceSessionLimits ends sessions past their plan's maximum duration or idle timeout.
//
// judgeIdle must be true only when this tick's accounting pass completed cleanly: a meter that could not read
// counters sees no traffic, and judging idleness then would disconnect active guests. Maximum duration is
// enforced either way.
//
// On a schema without the operation this is a no-op, not an error.
func (e *Enforcer) EnforceSessionLimits(ctx context.Context, tenant, site string, now time.Time, judgeIdle bool) ([]SessionEnd, error) {
	present, err := e.procPresent(ctx, "iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean)")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	rows, err := e.pool.Query(ctx,
		`SELECT session_id::text, entitlement_id::text, end_reason, ended_at
		   FROM iam_v2.p6_end_sessions_over_plan_limits($1,$2,$3,$4)`, tenant, site, now, judgeIdle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEnd
	for rows.Next() {
		var x SessionEnd
		if err := rows.Scan(&x.SessionID, &x.EntitlementID, &x.Reason, &x.At); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// PurgeAccountingDetail runs one bounded retention batch and returns the records removed. The database is a
// no-op while retention is unlimited, and removes only old records of ended sessions whose entitlements have all
// terminated.
func (e *Enforcer) PurgeAccountingDetail(ctx context.Context, tenant, site string, batch int) (int, error) {
	present, err := e.procPresent(ctx, "iam_v2.purge_accounting_detail(uuid,uuid,integer)")
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}
	var n int
	err = e.pool.QueryRow(ctx, `SELECT iam_v2.purge_accounting_detail($1,$2,$3)`, tenant, site, batch).Scan(&n)
	return n, err
}

// The roll-up reads one TIME window of raw samples per call, and the number of rows in a window is whatever the site
// produced in it. acctd's role cancels a statement after 30 s; a window that cannot be read in that time rolls back
// whole (the watermark moves in the same transaction), so the SAME window would be retried, and cancelled, forever
// and nothing behind it would ever be rolled up or purged. The window therefore adapts: it starts at two hours, is
// cut to a quarter after a cancelled statement (never below the function's one-minute minimum) and doubles back
// after a call that was quick.
const (
	rollupWindowMax = 2 * time.Hour
	rollupWindowMin = time.Minute
)

// rollupQuick: a window read faster than this is far from the 30 s cancellation (twice as wide takes under 10 s) and may
// be widened. A variable only so a test can scale it with a scaled-down statement_timeout.
var rollupQuick = 5 * time.Second

// nextRollupWindow decides the window for the call after one that took `took` and ended in `err`.
func nextRollupWindow(cur, took time.Duration, err error) time.Duration {
	switch {
	case err != nil && isStatementTimeout(err):
		cur /= 4
	case err == nil && took < rollupQuick:
		cur *= 2
	}
	if cur < rollupWindowMin {
		cur = rollupWindowMin
	}
	if cur > rollupWindowMax {
		cur = rollupWindowMax
	}
	return cur
}

// isStatementTimeout reports a statement cancelled by statement_timeout (SQLSTATE 57014, query_canceled).
func isStatementTimeout(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "57014"
}

type rollupWindows struct {
	mu sync.Mutex
	by map[string]time.Duration
}

func (w *rollupWindows) get(site string) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if d, ok := w.by[site]; ok {
		return d
	}
	return rollupWindowMax
}

func (w *rollupWindows) set(site string, d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.by == nil {
		w.by = map[string]time.Duration{}
	}
	w.by[site] = d
}

// RollupAccountingUsage adds the next window of raw accounting samples to the per-minute and per-hour usage tables
// (migration 0175, AD-48) and returns the samples read. A no-op on a database without the migration. The purge never
// removes a raw sample the rollup has not read, so this must keep running for detail to be reclaimed.
//
// The window is at most two hours and shrinks after a cancelled statement (see nextRollupWindow), so a busy site's
// window that cannot be read inside the role's statement_timeout is retried smaller instead of failing forever. The
// function is atomic, so a cancelled call leaves the watermark and the usage tables as they were.
func (e *Enforcer) RollupAccountingUsage(ctx context.Context, tenant, site string) (int64, error) {
	present, err := e.procPresent(ctx, "iam_v2.rollup_accounting_usage(uuid,uuid,interval)")
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}
	window := e.rollupWin.get(site)
	var n int64
	t0 := time.Now()
	err = e.pool.QueryRow(ctx, `SELECT iam_v2.rollup_accounting_usage($1,$2,make_interval(secs => $3::double precision))`,
		tenant, site, window.Seconds()).Scan(&n)
	e.rollupWin.set(site, nextRollupWindow(window, time.Since(t0), err))
	return n, err
}

// procPresent reports whether a database function exists, re-probing the catalog at most once a minute.
func (e *Enforcer) procPresent(ctx context.Context, signature string) (bool, error) {
	v, err := e.probes.get(signature, func() ([]bool, error) {
		var present bool
		err := e.pool.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, signature).Scan(&present)
		return []bool{present}, err
	})
	if err != nil {
		return false, err
	}
	return v[0], nil
}
