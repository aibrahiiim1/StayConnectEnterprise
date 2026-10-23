package iamv2

import "strings"

// The single rule every acquisition path (PMS/stay grant and free-commerce quote/confirm) asks, so the two
// entry points cannot disagree. It gates only NEW acquisition in a mode the deployment has not enabled;
// acctd's accrual is data-driven and keeps accounting existing entitlements whatever this flag says (so a
// disabled capability never turns a finite budget unlimited), and no historical row is rewritten.

const (
	// TimeModeValidityWindow is the continuous wall-clock mode; an omitted mode defaults to it.
	TimeModeValidityWindow = "VALIDITY_WINDOW"
	// TimeModeAggregateOnlineTime is the shared online-seconds budget.
	TimeModeAggregateOnlineTime = "AGGREGATE_ONLINE_TIME"
)

// AcquisitionReasonAggregateDisabled is the one deny reason every path reports, so refusals look alike.
const AcquisitionReasonAggregateDisabled = "aggregate_online_time_not_enabled"

// TimeModeAcquirable returns "" when a new quote, purchase or entitlement may be created in this mode, else
// the deny reason (a refusal to serve, not a fault). An empty mode is VALIDITY_WINDOW.
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
		// An unknown mode cannot be accounted for; refuse rather than guess.
		return "unsupported_time_accounting_mode"
	}
}
