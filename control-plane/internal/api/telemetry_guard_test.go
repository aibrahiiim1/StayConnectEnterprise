package api

import "testing"

func TestSanitizedTelemetryAcceptsAggregates(t *testing.T) {
	ok := map[string]any{"active_sessions": 5, "sessions_today": 5, "bytes_up_today": 100, "bytes_down_today": 200, "version": "1.0", "license_state": "Active"}
	if err := ValidateSanitizedTelemetry(ok); err != nil {
		t.Fatalf("sanitized aggregates must be accepted: %v", err)
	}
}

func TestSanitizedTelemetryRejectsPII(t *testing.T) {
	for _, bad := range []map[string]any{
		{"guest_name": "Jane"}, {"room_number": "204"}, {"email": "j@x.com"},
		{"reservation_number": "R1"}, {"voucher_code": "ABC"}, {"phone": "555"},
		{"mac_address": "aa:bb"}, {"pms_cache": []any{}}, {"stripe_secret": "sk_x"},
	} {
		if err := ValidateSanitizedTelemetry(bad); err == nil {
			t.Errorf("prohibited payload must be rejected: %v", bad)
		}
	}
}
