package main

// THE OPERATOR'S VIEW OF AN ONLINE-TIME BUDGET (Phase 6, DARK).
//
// It answers the question a front desk is actually asked -- "how much internet time do I have left?" -- with
// the same two numbers the guest sees, read from the same durable state the accrual writes. If the desk and
// the guest's phone could disagree about the remaining minutes, the desk would be the one telling the guest
// something untrue, in person.
//
// IT LIVES UNDER THE SESSIONS RESOURCE, deliberately. This is live access state, which is what that resource
// already means, so it inherits the role matrix exactly: the desk roles that may see and disconnect sessions
// may see this, and nobody else gains anything. Adding a resource key would have meant a new row in the
// matrix for a view that is not a new kind of authority.
//
// IT IS READ-ONLY AND IT EXPOSES NO GUEST IDENTITY. No name, no room, no stay, no PMS reference: an operator
// looking at time budgets does not need to know whose they are, and the screen that does need that is the
// one that already has its own authorization.

import (
	"net/http"
	"time"
)

type aggregateTimeRow struct {
	EntitlementID string `json:"entitlement_id"`
	Status        string `json:"status"`
	// BudgetSeconds and ConsumedSeconds are the raw pair; Remaining is what an operator reads out loud, and
	// it is computed here rather than in the browser so the two surfaces cannot drift.
	BudgetSeconds    int64      `json:"budget_seconds"`
	ConsumedSeconds  int64      `json:"consumed_seconds"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	HardExpiry       *time.Time `json:"hard_expiry,omitempty"`
	// TerminalCause is present only once the entitlement has ended, and says WHICH clock ran out --
	// the minutes, the calendar, or something else entirely.
	TerminalCause *string `json:"terminal_cause,omitempty"`
	LiveDevices   int     `json:"live_devices"`
}

// listAggregateTime serves GET /edge/v1/sessions/aggregate-time.
func (s *server) listAggregateTime(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT e.id::text,
		       e.status,
		       COALESCE(spr.time_quota_seconds, 0),
		       COALESCE(e.consumed_online_seconds, 0),
		       GREATEST(0, COALESCE(spr.time_quota_seconds, 0) - COALESCE(e.consumed_online_seconds, 0)),
		       e.window_ends_at,
		       ev.cause_detail,
		       (SELECT count(*) FROM iam_v2.entitlement_devices ed
		         WHERE ed.entitlement_id = e.id AND ed.status = 'AUTHORIZED')
		  FROM iam_v2.entitlements e
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		  LEFT JOIN LATERAL (
		      SELECT cause_detail FROM iam_v2.entitlement_termination_evidence t
		       WHERE t.entitlement_id = e.id ORDER BY t.recorded_at DESC LIMIT 1) ev ON true
		 WHERE e.tenant_id = $1 AND e.site_id = $2
		   AND e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
		 ORDER BY (e.status = 'TERMINATED'), e.window_ends_at NULLS LAST
		 LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()

	out := []aggregateTimeRow{}
	for rows.Next() {
		var a aggregateTimeRow
		if err := rows.Scan(&a.EntitlementID, &a.Status, &a.BudgetSeconds, &a.ConsumedSeconds,
			&a.RemainingSeconds, &a.HardExpiry, &a.TerminalCause, &a.LiveDevices); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "read failed")
		return
	}
	writeList(w, out)
}
