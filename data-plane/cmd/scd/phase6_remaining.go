package main

import (
	"context"
	"time"
)

// WHAT A GUEST ON AN AGGREGATE PACKAGE IS TOLD ABOUT THEIR TIME.
//
// A guest with an online-time budget has two clocks and needs both, because either can end their access and
// they behave nothing alike:
//
//   REMAINING ONLINE TIME  counts down only while they are connected. Ten minutes left means ten minutes of
//                          use, whenever they use them.
//   HARD EXPIRY            is a calendar instant, immutable, and arrives whether or not they were ever
//                          online. Minutes left after it are worth nothing.
//
// Showing only the first would be the more comfortable half-truth: a guest with 90 minutes left and a window
// closing in ten would plan their evening around a number that is about to stop mattering. Showing only the
// second is the ordinary validity-window display, which is what every other package already gets.
//
// The numbers come from durable state and are computed the same way the accrual does -- budget minus
// consumption, clamped at zero -- so the screen and the enforcement can never disagree about whether a guest
// still has time.

// aggregateStatus is what the status endpoint adds for a guest whose entitlement is in AGGREGATE_ONLINE_TIME
// mode. Every field is guest-facing: no entitlement id, no plan revision, no internal identity of any kind.
type aggregateStatus struct {
	TimeMode string `json:"time_mode"`
	// RemainingOnlineSeconds is what is left of the budget. Clamped at zero: a guest is never told they owe
	// time, and a negative number would in any case mean the accounting had failed.
	RemainingOnlineSeconds int64 `json:"remaining_online_seconds"`
	// HardExpiry is the outer window, RFC3339, empty when the package has none. It is the immutable instant
	// stamped at grant and never moved.
	HardExpiry string `json:"hard_expiry,omitempty"`
}

// aggregateStatusFor returns the aggregate view of a session, or nil when the session's entitlement is not in
// that mode -- which is every entitlement today, and which is why the caller emits nothing extra by default.
//
// A failure to read returns nil rather than an error: the status endpoint's job is to tell the guest whether
// they are online, and losing the extra detail must not cost them that answer.
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
	if err != nil || mode != "AGGREGATE_ONLINE_TIME" {
		return nil
	}
	remaining := budget - consumed
	if remaining < 0 {
		remaining = 0
	}
	out := &aggregateStatus{TimeMode: mode, RemainingOnlineSeconds: remaining}
	if window != nil {
		out.HardExpiry = window.UTC().Format(time.RFC3339)
	}
	return out
}
