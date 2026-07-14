package api

import (
	"fmt"
	"strings"
)

// prohibitedTelemetryKeys are guest-PII / secret substrings that must NEVER
// appear in a Cloud telemetry payload. Cloud telemetry carries only sanitized
// aggregates (session counts, byte totals, health, versions, license status).
var prohibitedTelemetryKeys = []string{
	"guest_name", "guest_email", "room", "reservation", "email", "phone",
	"voucher_code", "mac", "pms_cache", "password", "secret", "token",
	"card", "credential", "first_name", "last_name",
}

// ValidateSanitizedTelemetry rejects a telemetry payload that contains any
// prohibited guest-PII or secret field (checked case-insensitively by key
// substring). Returns an error naming the offending field.
func ValidateSanitizedTelemetry(payload map[string]any) error {
	for k := range payload {
		lk := strings.ToLower(k)
		for _, bad := range prohibitedTelemetryKeys {
			if strings.Contains(lk, bad) {
				return fmt.Errorf("prohibited guest/secret field in Cloud telemetry: %q", k)
			}
		}
	}
	return nil
}
