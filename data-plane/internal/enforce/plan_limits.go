package enforce

// SESSION-LEVEL PLAN LIMITS AND ACCOUNTING RETENTION (migration 0122).
//
// A plan states two limits on a SESSION rather than on the entitlement:
//
//	idle_timeout_seconds            no traffic for N seconds ends the session (IDLE_TIMEOUT)
//	max_continuous_session_seconds  a session ends N seconds after it started (MAX_SESSION_DURATION)
//
// Both end only the session, at the instant the limit was reached. The entitlement keeps its window, its time
// allowance and its usage, and the device keeps its place on it -- so the device signs in again and goes on.
// That is the difference from everything EnforceExpiries does, which ends the entitlement itself.
//
// The database decides, through iam_v2.p6_end_sessions_over_plan_limits: the plan is the pinned immutable
// revision, idle is read from the session's own accounting checkpoint (which moves only when its counters
// moved), and the session row is written by the definer, not by this process.

import (
	"context"
	"time"
)

// SessionEnd is one session a plan limit ended.
type SessionEnd struct {
	SessionID     string
	EntitlementID string
	Reason        string // IDLE_TIMEOUT | MAX_SESSION_DURATION
	At            time.Time
}

// End reasons recorded on a session a plan limit ended.
const (
	EndReasonIdleTimeout        = "IDLE_TIMEOUT"
	EndReasonMaxSessionDuration = "MAX_SESSION_DURATION"
)

// EnforceSessionLimits ends every live session of the site that has reached its plan's maximum continuous
// session or idle timeout, at the true instant.
//
// judgeIdle must be true only when THIS tick's accounting pass completed cleanly. Idle means "the meter saw no
// traffic", and a meter that could not read the counters sees none either: judging idleness then would
// disconnect guests who are using the network. The maximum session duration needs no evidence beyond the
// session's start, so it is enforced either way.
//
// On a schema without the operation (before 0122, or rolled back) this is a no-op, never an error: the limits
// were not enforced there before, and the rest of the tick must not fail because of it.
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

// PurgeAccountingDetail runs ONE bounded batch of the accounting-detail retention purge and reports how many
// records it removed. The database does nothing while the site's retention is unlimited -- the default -- and
// otherwise removes only records older than the retention, of ended sessions whose every entitlement has
// terminated; every run that removed anything is recorded in iam_v2.accounting_purge_runs.
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
