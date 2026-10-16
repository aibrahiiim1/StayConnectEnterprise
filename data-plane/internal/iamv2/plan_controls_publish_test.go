package iamv2

import (
	"encoding/json"
	"strings"
	"testing"
)

// D-C / O-8: ADMIN_APPROVAL has no approval workflow, so a plan cannot be published with it; the other two
// policies (REJECT_NEW_DEVICE, and DISCONNECT_OLDEST now that the database implements it) publish.
func TestPlanPublishRefusesAdminApprovalWithAReason(t *testing.T) {
	if msg := planSpecErr(t, func(s *PlanPublishSpec) { s.DeviceLimitPolicy = "ADMIN_APPROVAL" }); !strings.Contains(msg, "not available") ||
		!strings.Contains(msg, "DISCONNECT_OLDEST") {
		t.Fatalf("ADMIN_APPROVAL refusal = %q; it must say the policy is not available and what to choose instead", msg)
	}
	for _, p := range []string{"REJECT_NEW_DEVICE", "DISCONNECT_OLDEST", ""} {
		if msg := planSpecErr(t, func(s *PlanPublishSpec) { s.DeviceLimitPolicy = p }); msg != "" {
			t.Fatalf("policy %q refused: %s", p, msg)
		}
	}
}

// D-C / O-5: speed steps were shown and never applied. The editor may save one grant step with no overrides
// (an optional match condition is still allowed: it is enforced, as an eligibility condition).
func TestGrantTiersEnforceableRefusal(t *testing.T) {
	ok := [][]GrantTier{
		{{Order: 10, Value: map[string]any{}}},
		{{Order: 10, Value: nil}},
		{{Order: 10, Value: map[string]any{"match": map[string]any{"type": "AUTH_METHOD", "methods": []any{"voucher"}}}}},
	}
	for i, tiers := range ok {
		if msg := GrantTiersEnforceableRefusal(tiers); msg != "" {
			t.Fatalf("case %d refused: %s", i, msg)
		}
	}
	two := []GrantTier{{Order: 10, Value: map[string]any{}}, {Order: 20, Value: map[string]any{"down_kbps": json.Number("2000")}}}
	if msg := GrantTiersEnforceableRefusal(two); !strings.HasPrefix(msg, "speed_steps_not_supported") || !strings.Contains(msg, "service plan") {
		t.Fatalf("two steps: %q", msg)
	}
	over := []GrantTier{{Order: 10, Value: map[string]any{"up_kbps": json.Number("1000"), "down_kbps": json.Number("5000")}}}
	msg := GrantTiersEnforceableRefusal(over)
	if !strings.HasPrefix(msg, "speed_steps_not_supported") || !strings.Contains(msg, "down_kbps, up_kbps") {
		t.Fatalf("one step with speeds: %q (it must name the fields, in a stable order)", msg)
	}
}
