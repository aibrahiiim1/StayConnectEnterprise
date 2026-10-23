package main

import (
	"context"
	"time"
)

// aggregateStatus is what the status endpoint adds for online-time entitlements. A guest has two clocks:
// remaining online time (counts down only while connected) and hard expiry (an immutable calendar instant).
// Both are computed as the accrual does (budget minus consumption, clamped at zero); every field is guest-facing.
type aggregateStatus struct {
	TimeMode string `json:"time_mode"`
	// RemainingOnlineSeconds is clamped at zero; a guest is never told they owe time.
	RemainingOnlineSeconds int64 `json:"remaining_online_seconds"`
	// HardExpiry is the immutable outer window (RFC3339), empty when the package has none.
	HardExpiry string `json:"hard_expiry,omitempty"`
	// OnlineTimeAllowance is always true when present: remaining_online_seconds is an allowance to show.
	OnlineTimeAllowance bool `json:"online_time_allowance"`
}

// countsOnlineTime mirrors iam_v2.p6_counts_online_time (0122): the aggregate mode always, and a validity window
// when its plan carries a positive time allowance.
func countsOnlineTime(mode string, budget int64) bool {
	return mode == "AGGREGATE_ONLINE_TIME" || (mode == "VALIDITY_WINDOW" && budget > 0)
}

// aggregateStatusFor returns the online-time view of a session, or nil when it is not accounted in online time
// (AGGREGATE_ONLINE_TIME, or VALIDITY_WINDOW with a time allowance). A read failure returns nil so the guest
// still gets the online answer.
func (s *server) aggregateStatusFor(ctx context.Context, sessionID string) *aggregateStatus {
	if sessionID == "" || s.db == nil {
		return nil
	}
	var mode string
	var budget, consumed int64
	var window *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(e.time_accounting_mode, 'VALIDITY_WINDOW'),
		       COALESCE(spr.time_quota_seconds, 0),
		       COALESCE(e.consumed_online_seconds, 0),
		       e.window_ends_at
		  FROM iam_v2.sessions se
		  JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		 WHERE se.id = $1::uuid`, sessionID).Scan(&mode, &budget, &consumed, &window)
	if err != nil || !countsOnlineTime(mode, budget) {
		return nil
	}
	remaining := budget - consumed
	if remaining < 0 {
		remaining = 0
	}
	out := &aggregateStatus{TimeMode: mode, RemainingOnlineSeconds: remaining, OnlineTimeAllowance: true}
	if window != nil {
		out.HardExpiry = window.UTC().Format(time.RFC3339)
	}
	return out
}
