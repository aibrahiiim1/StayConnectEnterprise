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
	var present bool
	if err := e.pool.QueryRow(ctx,
		`SELECT to_regprocedure('iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean)') IS NOT NULL`).
		Scan(&present); err != nil {
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
	var present bool
	if err := e.pool.QueryRow(ctx,
		`SELECT to_regprocedure('iam_v2.purge_accounting_detail(uuid,uuid,integer)') IS NOT NULL`).Scan(&present); err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}
	var n int
	err := e.pool.QueryRow(ctx, `SELECT iam_v2.purge_accounting_detail($1,$2,$3)`, tenant, site, batch).Scan(&n)
	return n, err
}
