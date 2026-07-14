package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// health-supervisor alert kinds (appliance_security_alerts.kind is free-form).
// These are the ONLY kinds this reconciler owns; it never touches other alerts.
var healthAlertKinds = []string{
	"service_crash_loop",
	"service_unavailable",
	"service_active_but_unhealthy",
	"dependency_unavailable",
	"appliance_boot_not_converged",
}

// condition is one currently-active alert condition derived from a health report.
type condition struct {
	kind    string
	service string // the affected service (or "appliance"/dependency name)
	detail  map[string]any
}

func (c condition) key() string { return c.kind + "|" + c.service }

// reconcileHealthAlerts raises deduplicated alerts for the bad conditions in a
// service-health report and auto-resolves any previously-open health alert whose
// condition has cleared. Alerts are appliance-scoped, idempotent and lifecycle-
// aware; nothing carries guest PII or secrets (payload is pre-sanitized).
func (c *Consumer) reconcileHealthAlerts(ctx context.Context, applianceID, serial string, payload map[string]any) {
	active := deriveConditions(payload)

	// Raise: insert an alert for each active condition that has no open alert of
	// the same (appliance, kind, service).
	for _, cond := range active {
		det := cond.detail
		if det == nil {
			det = map[string]any{}
		}
		det["service"] = cond.service
		detJSON, _ := json.Marshal(det)
		_, _ = c.DB.Exec(ctx, `
			INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail, status, resolved)
			SELECT $1, $2, $3, $4::jsonb, 'open', false
			WHERE NOT EXISTS (
			  SELECT 1 FROM appliance_security_alerts s
			   WHERE s.appliance_id = $1 AND s.kind = $3
			     AND COALESCE(s.detail->>'service','') = $5 AND s.resolved = false)
		`, applianceID, serial, cond.kind, string(detJSON), cond.service)
	}

	// Resolve: any open health alert for this appliance whose condition is no
	// longer active is auto-resolved (recovery). Flag prolonged degradation so
	// the "recovery after prolonged degradation" signal is visible.
	rows, err := c.DB.Query(ctx, `
		SELECT id, kind, COALESCE(detail->>'service',''), created_at
		  FROM appliance_security_alerts
		 WHERE appliance_id = $1 AND resolved = false AND kind = ANY($2)
	`, applianceID, healthAlertKinds)
	if err != nil {
		return
	}
	type openAlert struct {
		id      string
		kind    string
		service string
		created time.Time
	}
	var open []openAlert
	for rows.Next() {
		var a openAlert
		if rows.Scan(&a.id, &a.kind, &a.service, &a.created) == nil {
			open = append(open, a)
		}
	}
	rows.Close()

	activeSet := map[string]bool{}
	for _, cond := range active {
		activeSet[cond.key()] = true
	}
	for _, a := range open {
		if activeSet[a.kind+"|"+a.service] {
			continue // still bad
		}
		prolonged := time.Since(a.created) > 5*time.Minute
		note := map[string]any{
			"resolution": "auto-resolved: appliance reported recovery",
			"prolonged":  prolonged,
			"open_for_s": int64(time.Since(a.created).Seconds()),
		}
		noteJSON, _ := json.Marshal(note)
		_, _ = c.DB.Exec(ctx, `
			UPDATE appliance_security_alerts
			   SET status='resolved', resolved=true, acknowledged_at=now(),
			       detail = COALESCE(detail,'{}'::jsonb) || $2::jsonb
			 WHERE id = $1 AND resolved = false
		`, a.id, string(noteJSON))
	}
}

// raiseSecurityAlert raises a deduplicated appliance security alert from a
// sanitized "security" telemetry event (e.g. a blocked attempt to enable
// permissive/dev licensing on a production appliance). These are point-in-time
// integrity events: they stay open until an operator triages them (no
// auto-resolve). Payload carries no secrets/PII (sanitized on the appliance).
func (c *Consumer) raiseSecurityAlert(ctx context.Context, applianceID, serial string, payload map[string]any) {
	kind, _ := payload["kind"].(string)
	if kind == "" {
		kind = "appliance_security_event"
	}
	det, _ := json.Marshal(payload)
	// Dedupe: one open alert per (appliance, kind) — repeated reports (e.g. on
	// every reboot while the misconfiguration persists) do not pile up.
	_, _ = c.DB.Exec(ctx, `
		INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail, status, resolved)
		SELECT $1, $2, $3, $4::jsonb, 'open', false
		WHERE NOT EXISTS (
		  SELECT 1 FROM appliance_security_alerts s
		   WHERE s.appliance_id = $1 AND s.kind = $3 AND s.resolved = false)
	`, applianceID, serial, kind, string(det))
}

// deriveConditions extracts the active alert conditions from a health payload.
func deriveConditions(payload map[string]any) []condition {
	var out []condition
	if svcs, ok := payload["services"].([]any); ok {
		for _, raw := range svcs {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["service"].(string)
			state, _ := m["state"].(string)
			dep, _ := m["degraded_dependency"].(string)
			reason, _ := m["last_failure_reason"].(string)
			base := map[string]any{"state": state}
			if reason != "" {
				base["reason"] = reason
			}
			if bl, ok := m["backoff_level"].(float64); ok {
				base["backoff_level"] = int(bl)
			}
			if rc, ok := m["restart_count"].(float64); ok {
				base["restart_count"] = int(rc)
			}
			switch state {
			case "crash_loop":
				out = append(out, condition{"service_crash_loop", name, base})
			case "failed":
				out = append(out, condition{"service_unavailable", name, base})
			case "degraded":
				out = append(out, condition{"service_active_but_unhealthy", name, base})
			}
			if dep != "" {
				d := map[string]any{"dependency": dep, "reason": reason}
				out = append(out, condition{"dependency_unavailable", name + "->" + dep, d})
			}
		}
	}
	if boot, ok := payload["boot"].(map[string]any); ok {
		alertOpen, _ := boot["alert_open"].(bool)
		if alertOpen {
			pend := fmt.Sprintf("%v", boot["pending"])
			out = append(out, condition{"appliance_boot_not_converged", "appliance",
				map[string]any{"pending_services": pend}})
		}
	}
	return out
}
