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

// resolveLicenseRequired decides whether a real signed license is mandatory and
// detects any attempt to run permissively on a production appliance.
//
// Production is determined at COMPILE time (buildprofile.Production, the default
// build) OR by an explicit /etc/stayconnect/production marker. In production a
// license is ALWAYS required — SCD_LICENSE_REQUIRED=false and the dev-mode
// marker are ignored and reported as attempts. Only an explicit `-tags
// devlicense` build with no production marker may run permissively.
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
	// Development build (no production marker): permissive unless explicitly
	// required. This whole branch is compiled out of production binaries.
	return envRequired, ""
}

// reportPermissiveAttempt raises a local critical alert, writes an audit record
// and enqueues sanitized Central security telemetry when a production appliance
// rejects an attempt to enable permissive/dev licensing. Enforcement is NOT
// weakened — the attempt was already refused (license stays required).
func (s *server) reportPermissiveAttempt(ctx context.Context, reason string) {
	slog.Error("LICENSE SECURITY: rejected attempt to enable permissive/dev licensing on a production appliance",
		"reason", reason, "build_profile", buildprofile.Name, "action", "enforcement kept ON")

	// Audit (best-effort; never blocks startup).
	if s.db != nil {
		actx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, _ = s.db.Exec(actx, `INSERT INTO audit_log
		    (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
		    VALUES (NULLIF($1,'')::uuid, 'system', NULL, 'license.permissive_attempt_blocked', 'appliance', $2, $3)`,
			s.tenID, s.applID, map[string]any{"reason": reason, "build_profile": buildprofile.Name})
		cancel()
	}

	// Sanitized Central security telemetry (drains when connected; queues while
	// offline). No secrets/PII — just the fact + reason.
	if s.obx != nil {
		octx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = s.obx.Enqueue(octx, "security", map[string]any{
			"kind":          "license_permissive_attempt_blocked",
			"reason":        reason,
			"build_profile": buildprofile.Name,
			"severity":      "critical",
			"at":            time.Now().UTC(),
		})
		cancel()
	}
}
