package iamv2

// WHICH PLAN FIELD WAS OUT OF RANGE.
//
// A live edit of the OneDay service plan was refused with "plan integer out of range" and nothing else. Six
// different fields produced that identical message, so the operator could not tell which number to correct,
// and OneDay is the plan where that matters most: its speed sits exactly ON the 10 Gbps ceiling, so the
// nearest wrong keystroke fails while the screen names no field.
//
// The refusals themselves are unchanged -- the same values are accepted and rejected as before. What is
// guarded here is that a rejection says what to fix.

import (
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func planSpecErr(t *testing.T, mutate func(*PlanPublishSpec)) string {
	t.Helper()
	// A minimal spec that passes on its own, so any failure below belongs to the field under test.
	spec := &PlanPublishSpec{MaxConcurrentDevices: 1}
	mutate(spec)
	err := validatePlanSpec(spec, false)
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestPlanBoundRejectionNamesTheField(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PlanPublishSpec)
		field   string
		limit   string
		offered string
	}{
		{"download speed", func(s *PlanPublishSpec) { s.DownKbps = ptr(maxKbps + 1) },
			"down_kbps", "10000000", "10000001"},
		{"upload speed", func(s *PlanPublishSpec) { s.UpKbps = ptr(maxKbps + 1) },
			"up_kbps", "10000000", "10000001"},
		{"idle timeout", func(s *PlanPublishSpec) { s.IdleTimeoutSeconds = ptr(maxIdleSeconds + 1) },
			"idle_timeout_seconds", "2592000", "2592001"},
		{"max session", func(s *PlanPublishSpec) { s.MaxContinuousSessionSeconds = ptr(maxSessionSeconds + 1) },
			"max_continuous_session_seconds", "31536000", "31536001"},
		// The live trap: an operator re-entering a one-day allowance as 86400 against the DAYS unit.
		{"time quota", func(s *PlanPublishSpec) { s.TimeQuotaSeconds = ptr(int64(86400) * 86400) },
			"time_quota_seconds", "315360000", "7464960000"},
		{"data quota", func(s *PlanPublishSpec) { s.DataQuotaBytes = ptr(maxDataQuotaBytes + 1) },
			"data_quota_bytes", "1125899906842624", "1125899906842625"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := planSpecErr(t, tc.mutate)
			if msg == "" {
				t.Fatalf("%s past its bound was accepted", tc.field)
			}
			// The field, the limit and what was actually sent -- everything needed to fix it without guessing.
			for _, want := range []string{tc.field, tc.limit, tc.offered} {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q does not contain %q", msg, want)
				}
			}
			// ...and never the old message that named none of them.
			if strings.Contains(msg, "plan integer out of range") {
				t.Errorf("message %q is still the unhelpful one", msg)
			}
		})
	}
}

func TestPlanBoundsStillAcceptWhatTheyAlwaysDid(t *testing.T) {
	// The OneDay plan as it exists live: 10 Gbps up and down is exactly the ceiling, not past it. Tightening
	// the message must not tighten the rule.
	if msg := planSpecErr(t, func(s *PlanPublishSpec) {
		s.DownKbps = ptr(maxKbps)
		s.UpKbps = ptr(maxKbps)
		s.MaxConcurrentDevices = 5
		s.TimeQuotaSeconds = ptr(int64(86400))
	}); msg != "" {
		t.Fatalf("the live OneDay plan was refused: %s", msg)
	}
	// Negative values remain refused, and now say which field.
	if msg := planSpecErr(t, func(s *PlanPublishSpec) { s.DownKbps = ptr(-1) }); !strings.Contains(msg, "down_kbps") {
		t.Fatalf("a negative speed was not refused by name: %q", msg)
	}
	// An unset field is not a zero and is not bounds-checked.
	if msg := planSpecErr(t, func(s *PlanPublishSpec) {}); msg != "" {
		t.Fatalf("a spec that sets no optional field was refused: %s", msg)
	}
}
