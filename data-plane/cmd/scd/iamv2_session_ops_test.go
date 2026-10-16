package main

import "testing"

// end_session_enforcement takes a bounded upper-case machine code (^[A-Z][A-Z0-9_]{0,63}$).
func TestSessionEndCodeFitsTheControlledWriter(t *testing.T) {
	for in, want := range map[string]string{
		"admin":                  "ADMIN",
		"quota_bytes":            "QUOTA_BYTES",
		" dhcp_expired ":         "DHCP_EXPIRED",
		endReasonDeviceReplaced:  endReasonDeviceReplaced,
		endReasonAddressChanged:  endReasonAddressChanged,
		"":                       "ADMIN",
		"made-up reason!":        "ADMIN",
		"1starts_with_digit":     "ADMIN",
		string(make([]byte, 80)): "ADMIN",
	} {
		if got := sessionEndCode(in); got != want {
			t.Fatalf("sessionEndCode(%q) = %q, want %q", in, got, want)
		}
		if !sessionEndCodeRe.MatchString(sessionEndCode(in)) {
			t.Fatalf("sessionEndCode(%q) does not fit the writer's code form", in)
		}
	}
}
