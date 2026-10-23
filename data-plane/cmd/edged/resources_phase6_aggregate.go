package main

// Read-only operator view of an online-time budget. It lives under the sessions resource so it inherits that
// role matrix, and it exposes no guest identity (no name, room, stay or PMS reference).

import (
	"net/http"
	"time"
)

type aggregateTimeRow struct {
	EntitlementID string `json:"entitlement_id"`
	Status        string `json:"status"`
	// Remaining is computed here, not in the browser, so the desk and the guest cannot disagree.
	BudgetSeconds    int64      `json:"budget_seconds"`
	ConsumedSeconds  int64      `json:"consumed_seconds"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	HardExpiry       *time.Time `json:"hard_expiry,omitempty"`
	TerminalCause    *string    `json:"terminal_cause,omitempty"`
	LiveDevices      int        `json:"live_devices"`
}

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
