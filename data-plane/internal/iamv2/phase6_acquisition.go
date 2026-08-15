package iamv2

import "strings"

// THE ONE RULE FOR ACQUIRING A TIME ACCOUNTING MODE.
//
// Phase 6 has more than one way to acquire access -- the PMS/stay grant, and the Phase-2 free-commerce
// quote/confirm -- and they share no code. Two entry points each carrying their own copy of "may we create
// this?" is how they end up disagreeing: one gets a fix, the other does not, and the difference is invisible
// until an entitlement exists that nothing accounts for.
//
// So the rule lives here once, and every acquisition path calls it.
//
// WHAT THE RULE PROTECTS. An AGGREGATE_ONLINE_TIME entitlement is only meaningful if something is consuming
// its budget. That consumption is acctd's aggregate tick, which does not run while the Phase-6 aggregate
// flag is off. An entitlement created in that mode on such a build would never consume, never exhaust, and
// never end -- an unlimited package by accident, on a runtime that cannot account for it. Refusing to create
// it is the only honest answer, and refusing is safe: nothing is lost, the guest simply does not get access
// this build cannot measure.
//
// WHAT THE RULE DELIBERATELY DOES NOT DO. It says nothing about anything already durable. An immutable
// AGGREGATE_ONLINE_TIME plan revision keeps existing and keeps its meaning, entitlements granted earlier
// under it are not reinterpreted or re-moded, and no historical row is rewritten. Only NEW acquisition is
// closed, which is the difference between a capability gate and a destructive migration.

const (
	// TimeModeValidityWindow is the continuous wall-clock mode: the only mode published before Phase 6, and
	// the value an omitted mode defaults to.
	TimeModeValidityWindow = "VALIDITY_WINDOW"
	// TimeModeAggregateOnlineTime is the Phase-6 shared online-seconds budget.
	TimeModeAggregateOnlineTime = "AGGREGATE_ONLINE_TIME"
)

// AcquisitionReasonAggregateDisabled is the deny reason every acquisition path reports, so that a refusal
// looks the same wherever a guest happens to have started.
const AcquisitionReasonAggregateDisabled = "aggregate_online_time_not_enabled"

// TimeModeAcquirable reports whether a NEW quote, purchase or entitlement may be created in this mode.
//
// An empty mode is VALIDITY_WINDOW, matching the schema default and every revision published before Phase 6:
// callers must not have to normalise before asking.
//
// The empty return is "yes". A non-empty return is the deny reason, not an error type, because on the guest
// paths this is a refusal to serve rather than a fault -- and refusals there are uniform by design.
func TimeModeAcquirable(mode string, aggregateEnabled bool) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "", TimeModeValidityWindow:
		return ""
	case TimeModeAggregateOnlineTime:
		if aggregateEnabled {
			return ""
		}
		return AcquisitionReasonAggregateDisabled
	default:
		// An unknown mode is refused everywhere. A value the schema does not define cannot be accounted for
		// by definition, and guessing which existing mode was meant would be worse than refusing.
		return "unsupported_time_accounting_mode"
	}
}
