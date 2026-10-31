package enforce

// The producer half of outage continuity: from the durable state acctd already reads to derive the plan, state for every
// entitled session the hard bounds and byte budget netd may rely on if plans stop (internal/shapeplan/continuity.go).
//
// Everything here is CONSERVATIVE by construction: a session whose bounds cannot be stated completely is left out of the
// block, and netd gives a session that is not in the block the strict 90 s lease, never a longer one.

import (
	"context"
	"fmt"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	// BudgetMarginSeconds is the traffic, at the session's rate ceiling, that may have flowed without being counted when the
	// budget was read: the accounting lag plus the spacing of netd's counter samples (30 s each).
	BudgetMarginSeconds = 60
	// UnratedAssumedKbps stands in for a session with no rate ceiling, so its margin is large rather than zero.
	UnratedAssumedKbps = 1_000_000
	// timeMarginSeconds is taken off an online-time allowance.
	timeMarginSeconds = 120
)

// ContinuityRow is what the database says about one entitled session.
type ContinuityRow struct {
	SessionID     string
	EntitlementID string
	Started       time.Time
	MaxSessionSec int64 // 0 = none
	// DataQuota is nil when the entitlement has no byte quota.
	DataQuota    *int64
	DataConsumed int64
	CountsTime   bool
	TimeQuota    *int64 // seconds; nil = no allowance
	TimeConsumed int64
	WindowEndsAt *time.Time
	DownKbps     int
	UpKbps       int
}

// BuildContinuity derives the block. window <= 0 returns nil (strict mode). Pure: no database, no clock.
func BuildContinuity(rows []ContinuityRow, window time.Duration, asOf time.Time) *shapeplan.Continuity {
	if window <= 0 {
		return nil
	}
	if window > time.Duration(shapeplan.MaxContinuityWindowSeconds)*time.Second {
		window = time.Duration(shapeplan.MaxContinuityWindowSeconds) * time.Second
	}
	live := map[string]int{}
	marginBytes := map[string]int64{}
	for _, r := range rows {
		live[r.EntitlementID]++
		down, up := r.DownKbps, r.UpKbps
		if down <= 0 {
			down = UnratedAssumedKbps
		}
		if up <= 0 {
			up = UnratedAssumedKbps
		}
		marginBytes[r.EntitlementID] += int64(down+up) * 125 * BudgetMarginSeconds
	}
	c := &shapeplan.Continuity{WindowSeconds: int(window / time.Second), AsOf: asOf.UTC()}
	budgeted := map[string]bool{}
	for _, r := range rows {
		end := r.WindowEndsAt
		tighten := func(t time.Time) {
			if end == nil || t.Before(*end) {
				tt := t.UTC()
				end = &tt
			}
		}
		if r.MaxSessionSec > 0 {
			tighten(r.Started.Add(time.Duration(r.MaxSessionSec) * time.Second))
		}
		if r.CountsTime && r.TimeQuota != nil {
			left := *r.TimeQuota - r.TimeConsumed - timeMarginSeconds
			// Device-seconds are spent by every live session at once, so the allowance lasts left/N wall-clock seconds.
			n := int64(live[r.EntitlementID])
			if n < 1 {
				n = 1
			}
			if left <= 0 {
				tighten(asOf) // nothing may be relied on
			} else {
				tighten(asOf.Add(time.Duration(left/n) * time.Second))
			}
		}
		if r.DataQuota != nil {
			rem := *r.DataQuota - r.DataConsumed - marginBytes[r.EntitlementID]
			if rem <= 0 {
				tighten(asOf) // budget gone before the outage began: no continuity for this session
			}
			if !budgeted[r.EntitlementID] {
				budgeted[r.EntitlementID] = true
				if rem < 0 {
					rem = 0
				}
				c.Budgets = append(c.Budgets, shapeplan.ContinuityBudget{EntitlementID: r.EntitlementID, RemainingBytes: rem})
			}
		}
		c.Sessions = append(c.Sessions, shapeplan.ContinuitySession{SessionID: r.SessionID, HardEnd: end})
	}
	c.ContentsHash = shapeplan.HashContinuity(*c)
	return c
}

// ContinuityRows reads the rows for the plan's entitled sessions. One query, indexed by session id.
func (e *Enforcer) ContinuityRows(ctx context.Context, tenant, site string, plan Plan) ([]ContinuityRow, error) {
	ids := make([]string, 0, len(plan.Shape))
	rate := map[string][2]int{}
	for _, s := range plan.Shape {
		ids = append(ids, s.SessionID)
		rate[s.SessionID] = [2]int{s.DownKbps, s.UpKbps}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rs, err := e.pool.Query(ctx, `SELECT s.id::text, s.entitlement_id::text, s.started,
			COALESCE(spr.max_continuous_session_seconds,0)::bigint,
			COALESCE(e.data_quota_bytes, spr.data_quota_bytes), COALESCE(e.consumed_data_bytes,0)::bigint,
			(e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
			   OR (e.time_accounting_mode = 'VALIDITY_WINDOW' AND COALESCE(spr.time_quota_seconds,0) > 0)),
			spr.time_quota_seconds, COALESCE(e.consumed_online_seconds,0)::bigint, e.window_ends_at
		FROM iam_v2.sessions s
		JOIN iam_v2.entitlements e ON e.id = s.entitlement_id AND e.tenant_id = s.tenant_id
		LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		WHERE s.tenant_id = $2 AND s.site_id = $3 AND s.id = ANY($1::uuid[])`, ids, tenant, site)
	if err != nil {
		return nil, fmt.Errorf("continuity rows: %w", err)
	}
	defer rs.Close()
	var out []ContinuityRow
	for rs.Next() {
		var r ContinuityRow
		if err := rs.Scan(&r.SessionID, &r.EntitlementID, &r.Started, &r.MaxSessionSec, &r.DataQuota, &r.DataConsumed,
			&r.CountsTime, &r.TimeQuota, &r.TimeConsumed, &r.WindowEndsAt); err != nil {
			return nil, err
		}
		if k, ok := rate[r.SessionID]; ok {
			r.DownKbps, r.UpKbps = k[0], k[1]
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// ContinuityFor reads and builds. AsOf is stamped BEFORE the read, so a change committed after it is newer than the block.
func (e *Enforcer) ContinuityFor(ctx context.Context, tenant, site string, plan Plan, window time.Duration) (*shapeplan.Continuity, error) {
	if window <= 0 {
		return nil, nil
	}
	asOf := time.Now()
	rows, err := e.ContinuityRows(ctx, tenant, site, plan)
	if err != nil {
		return nil, err
	}
	return BuildContinuity(rows, window, asOf), nil
}
