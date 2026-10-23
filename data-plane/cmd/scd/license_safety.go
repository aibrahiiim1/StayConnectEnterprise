package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

// productionMarker, when present, forces production licensing even on a
// development binary (defence in depth for a field-imaged appliance).
const productionMarker = "/etc/stayconnect/production"

// devModeMarker is the file an operator might drop to try to enable permissive
// mode. On a production appliance its presence is a REJECTED attempt.
const devModeMarker = "/etc/stayconnect/dev-mode"

// resolveLicenseRequired decides whether a signed licence is mandatory. In production (compile-time buildprofile.Production, or the production
// marker) it is ALWAYS required: SCD_LICENSE_REQUIRED=false and the dev-mode marker are ignored and reported as attempts.
// Only a devlicense build without the marker may run permissively.
func resolveLicenseRequired(envRequired bool) (required bool, devAttempt string) {
	_, markerErr := os.Stat(productionMarker)
	isProduction := buildprofile.Production || markerErr == nil

	if isProduction {
		// No environment-only bypass, no config-error fallback: always required.
		if os.Getenv("SCD_LICENSE_REQUIRED") == "false" {
			devAttempt = "SCD_LICENSE_REQUIRED=false ignored (production build)"
		}
		if _, err := os.Stat(devModeMarker); err == nil {
			if devAttempt != "" {
				devAttempt += "; "
			}
			devAttempt += devModeMarker + " present but ignored (production build)"
		}
		return true, devAttempt
	}
	// Development build: permissive unless explicitly required; compiled out of production binaries.
	return envRequired, ""
}

// reportPermissiveAttempt raises a critical alert and audit record when production rejects a permissive-licensing attempt;
// enforcement is not weakened. The alert surfaces in Hotel Admin through licence status (permissiveBlocked).
func (s *server) reportPermissiveAttempt(ctx context.Context, reason string) {
	slog.Error("LICENSE SECURITY: rejected attempt to enable permissive/dev licensing on a production appliance",
		"reason", reason, "build_profile", buildprofile.Name, "action", "enforcement kept ON")

	if s.db != nil {
		actx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, _ = s.db.Exec(actx, `INSERT INTO audit_log
		    (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
		    VALUES (NULLIF($1,'')::uuid, 'system', NULL, 'license.permissive_attempt_blocked', 'appliance', $2, $3)`,
			s.tenID, s.applID, map[string]any{"reason": reason, "build_profile": buildprofile.Name})
		cancel()
	}
}
