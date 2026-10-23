package iamv2

import (
	"strings"
	"time"
)

// Only non-PMS end modes are supported (MANUAL_END, VALIDITY_WINDOW, FIXED_AT); stay-dependent and
// local-time policies are capability-disabled and fail closed. The policy is resolved once at quote time and
// frozen into the snapshot, so later revisions never recompute historical purchases.
var stayDependentEndModes = map[string]bool{
	"AT_CHECKOUT": true, "GRACE_AFTER_CHECKOUT": true, "EARLIEST_OF_FIXED_AND_CHECKOUT": true, "REST_OF_STAY": true,
}

const maxDurationSeconds = int64(10) * 365 * 24 * 3600

// ResolveEndPolicy resolves the revision's duration_policy; a nil or empty policy is malformed (deny).
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
