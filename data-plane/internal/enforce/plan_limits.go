package enforce

// Plan session limits (idle_timeout_seconds, max_continuous_session_seconds) end only the session, not the
// entitlement, so the device can sign in again. The database decides via iam_v2.p6_end_sessions_over_plan_limits.

import (
	"context"
	"time"
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

// RollupAccountingUsage adds the next window of raw accounting samples to the per-minute and per-hour usage tables
// (migration 0175, AD-48) and returns the samples read. A no-op on a database without the migration. The purge never
// removes a raw sample the rollup has not read, so this must keep running for detail to be reclaimed.
func (e *Enforcer) RollupAccountingUsage(ctx context.Context, tenant, site string) (int64, error) {
	present, err := e.procPresent(ctx, "iam_v2.rollup_accounting_usage(uuid,uuid,interval)")
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}
	var n int64
	// A window, not the function's one-day default: acctd's role cancels a statement after 30 s, and a day of samples at
	// the design load (2,000 devices) is several million rows. Measured on a 1 vCPU appliance: about 85,000 rows a second,
	// so two hours (about 480,000 rows) take some six seconds; the caller repeats until a window is empty.
	err = e.pool.QueryRow(ctx, `SELECT iam_v2.rollup_accounting_usage($1,$2,interval '2 hours')`, tenant, site).Scan(&n)
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
