package iamv2

import (
	"strings"
	"time"
)

// Phase-2 duration/end-policy resolution. Only policies whose required inputs are authoritative WITHOUT
// PMS/Stay resolution are supported; every Stay-dependent or checkout-grace policy is capability-
// disabled and fails closed. The policy is resolved ONCE at quote time and frozen into the snapshot;
// historical purchases/entitlements are never recomputed when a package revision later changes.
//
// Supported end_mode values (non-PMS):
//   - MANUAL_END                          : no automatic window.
//   - VALIDITY_WINDOW {duration_seconds}  : window_ends_at = now + duration_seconds.
//   - FIXED_AT        {ends_at RFC3339 UTC}: window_ends_at = ends_at (must be in the future).
//
// Capability-disabled (Phase 3): AT_CHECKOUT, GRACE_AFTER_CHECKOUT, EARLIEST_OF_FIXED_AND_CHECKOUT,
// REST_OF_STAY, and any local-time-boundary policy (needs authoritative site timezone semantics).

var stayDependentEndModes = map[string]bool{
	"AT_CHECKOUT": true, "GRACE_AFTER_CHECKOUT": true, "EARLIEST_OF_FIXED_AND_CHECKOUT": true, "REST_OF_STAY": true,
}

const maxDurationSeconds = int64(10) * 365 * 24 * 3600

// ResolveEndPolicy computes (end_mode, window) from the immutable package-revision duration_policy. A
// nil/empty policy is malformed (deny). Returns a deterministic error for unsupported/malformed input.
func ResolveEndPolicy(dp map[string]any, now time.Time) (endMode string, window *time.Time, err error) {
	if len(dp) == 0 {
		return "", nil, &Error{Code: ErrInvalidInput, Msg: "duration_policy required"}
	}
	rawMode, _ := dp["end_mode"].(string)
	mode := strings.ToUpper(strings.TrimSpace(rawMode))
	if mode == "" {
		return "", nil, &Error{Code: ErrInvalidInput, Msg: "duration_policy.end_mode required"}
	}
	if stayDependentEndModes[mode] {
		return "", nil, &Error{Code: ErrInvalidInput, Msg: "end_mode " + mode + " is capability-disabled in Phase 2 (needs PMS Stay resolution)"}
	}
	// a local-time boundary is capability-disabled in Phase 2 (no authoritative site tz in this path)
	if _, hasLocal := dp["local_end_time"]; hasLocal {
		return "", nil, &Error{Code: ErrInvalidInput, Msg: "local-time-boundary duration is capability-disabled in Phase 2"}
	}
	switch mode {
	case "MANUAL_END":
		return "MANUAL_END", nil, nil
	case "VALIDITY_WINDOW":
		secs, ok := asInt64(dp["duration_seconds"])
		if !ok || secs <= 0 || secs > maxDurationSeconds {
			return "", nil, &Error{Code: ErrInvalidInput, Msg: "VALIDITY_WINDOW needs a positive bounded duration_seconds"}
		}
		w := now.Add(time.Duration(secs) * time.Second).UTC()
		return "VALIDITY_WINDOW", &w, nil
	case "FIXED_AT":
		sv, ok := dp["ends_at"].(string)
		if !ok || strings.TrimSpace(sv) == "" {
			return "", nil, &Error{Code: ErrInvalidInput, Msg: "FIXED_AT needs ends_at (RFC3339 UTC)"}
		}
		t, perr := time.Parse(time.RFC3339, strings.TrimSpace(sv))
		if perr != nil {
			return "", nil, &Error{Code: ErrInvalidInput, Msg: "FIXED_AT ends_at malformed"}
		}
		if !t.After(now) {
			return "", nil, &Error{Code: ErrInvalidInput, Msg: "FIXED_AT ends_at must be in the future"}
		}
		w := t.UTC()
		return "FIXED_AT", &w, nil
	default:
		return "", nil, &Error{Code: ErrInvalidInput, Msg: "unsupported end_mode " + mode}
	}
}
