package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

// SYSTEM: security alerts, trust & keys, the audit log.

// listSecurityAlerts: GET /cloud/v1/security-alerts?status=&appliance_id=
func (b *Base) listSecurityAlerts(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT x.id::text, COALESCE(x.appliance_id::text,''), COALESCE(x.serial, a.serial, ''), x.kind,
               COALESCE(x.detail::text,'{}'), COALESCE(x.source_ip,''), x.resolved, x.status, x.created_at,
               x.acknowledged_at, COALESCE(a.tenant_id::text,''), COALESCE(t.name,'')
          FROM appliance_security_alerts x
          LEFT JOIN appliances a ON a.id = x.appliance_id
          LEFT JOIN tenants t ON t.id = a.tenant_id
         WHERE ($1 = '' OR a.tenant_id::text = $1)
           AND ($2 = '' OR x.status = $2)
           AND ($3 = '' OR x.appliance_id::text = $3)
         ORDER BY x.created_at DESC LIMIT 500`, scope.Filter(""), q.Get("status"), q.Get("appliance_id"))
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, appID, serial, kind, detail, ip, status, custID, custName string
		var resolved bool
		var at time.Time
		var ackAt *time.Time
		if rows.Scan(&id, &appID, &serial, &kind, &detail, &ip, &resolved, &status, &at, &ackAt, &custID, &custName) != nil {
			continue
		}
		if !json.Valid([]byte(detail)) {
			b, _ := json.Marshal(map[string]string{"reason": detail})
			detail = string(b)
		}
		out = append(out, map[string]any{"id": id, "appliance_id": strPtr(appID), "serial": strPtr(serial),
			"kind": kind, "detail": json.RawMessage(detail), "source_ip": strPtr(ip), "resolved": resolved,
			"status": status, "created_at": at, "acknowledged_at": ackAt,
			"customer_id": strPtr(custID), "customer_name": strPtr(custName)})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// patchSecurityAlert: PATCH /cloud/v1/security-alerts/{id} {status, reason} — security.manage.
func (b *Base) patchSecurityAlert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	switch in.Status {
	case "open", "investigating", "acknowledged", "resolved", "false_positive":
	default:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "status must be open, investigating, acknowledged, resolved or false_positive")
		return
	}
	operatorID, _ := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var prev, appID string
	if err := b.DB.QueryRow(ctx, `SELECT status, COALESCE(appliance_id::text,'') FROM appliance_security_alerts WHERE id=$1`, id).
		Scan(&prev, &appID); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "alert not found")
		return
	}
	resolved := in.Status == "resolved" || in.Status == "false_positive"
	if _, err := b.DB.Exec(ctx, `
        UPDATE appliance_security_alerts
           SET status=$2, resolved=$3, acknowledged_by=NULLIF($4,'')::uuid, acknowledged_at=now()
         WHERE id=$1`, id, in.Status, resolved, operatorID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(ctx, b.DB, r, "security_alert.status_changed", "security_alert", id, map[string]any{
		"from": prev, "to": in.Status, "reason": in.Reason, "appliance_id": appID})
	WriteJSON(w, http.StatusOK, map[string]any{"id": id, "status": in.Status, "from": prev})
}

// trust: GET /cloud/v1/trust — CA versions, appliance certificates, assignment signing keys, registry.
// Public material and fingerprints only; no private key and no certificate PEM is ever returned.
func (b *Base) trust(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()

	cas := []map[string]any{}
	if rows, err := b.DB.Query(ctx, `
        SELECT version, subject, COALESCE(key_fingerprint,''), active, cert_pem FROM appliance_ca_versions ORDER BY version`); err == nil {
		for rows.Next() {
			var ver int
			var subject, keyFpr, pemText string
			var active bool
			if rows.Scan(&ver, &subject, &keyFpr, &active, &pemText) != nil {
				continue
			}
			kind := "intermediate"
			if ver == 0 {
				kind = "root"
			}
			var notAfter *time.Time
			var fpr *string
			if block, _ := pem.Decode([]byte(pemText)); block != nil {
				sum := sha256.Sum256(block.Bytes)
				f := hex.EncodeToString(sum[:])
				fpr = &f
				if c, err := x509.ParseCertificate(block.Bytes); err == nil {
					na := c.NotAfter
					notAfter = &na
				}
			}
			cas = append(cas, map[string]any{"version": ver, "kind": kind, "subject": subject,
				"fingerprint_sha256": fpr, "key_fingerprint": keyFpr, "active": active, "not_after": notAfter})
		}
		rows.Close()
	}

	certs := []map[string]any{}
	var active, revoked, expiring int
	if rows, err := b.DB.Query(ctx, `
        SELECT c.id::text, c.appliance_id::text, a.serial, COALESCE(t.name,''), c.fingerprint_sha256, c.cert_serial,
               c.ca_version, c.not_before, c.not_after, c.status, c.created_at, c.revoked_at, COALESCE(c.revocation_reason,'')
          FROM appliance_certificates c
          JOIN appliances a ON a.id = c.appliance_id
          LEFT JOIN tenants t ON t.id = a.tenant_id
         ORDER BY c.created_at DESC LIMIT 500`); err == nil {
		now := time.Now()
		for rows.Next() {
			var id, appID, serial, cust, fpr, cser, status, reason string
			var caVer int
			var nb, na, created time.Time
			var revokedAt *time.Time
			if rows.Scan(&id, &appID, &serial, &cust, &fpr, &cser, &caVer, &nb, &na, &status, &created, &revokedAt, &reason) != nil {
				continue
			}
			switch status {
			case "active":
				active++
				if na.Sub(now) <= 30*24*time.Hour {
					expiring++
				}
			case "revoked":
				revoked++
			}
			certs = append(certs, map[string]any{"id": id, "appliance_id": appID, "serial": serial,
				"customer_name": strPtr(cust), "fingerprint_sha256": fpr, "cert_serial": cser, "ca_version": caVer,
				"not_before": nb, "not_after": na, "status": status, "created_at": created,
				"revoked_at": revokedAt, "revocation_reason": strPtr(reason)})
		}
		rows.Close()
	}

	keys := []map[string]any{}
	if rows, err := b.DB.Query(ctx, `
        SELECT k.key_id, k.public_key, k.state, k.activated_at, k.verify_only_at, k.revoked_at,
               COALESCE(k.reason,''), COALESCE(u.current_assignments, 0)
          FROM assignment_signing_keys k
          LEFT JOIN assignment_signer_usage u ON u.key_id = k.key_id
         ORDER BY k.activated_at DESC`); err == nil {
		for rows.Next() {
			var keyID, state, reason string
			var pub []byte
			var activated time.Time
			var verifyOnlyAt, revokedAt *time.Time
			var deps int64
			if rows.Scan(&keyID, &pub, &state, &activated, &verifyOnlyAt, &revokedAt, &reason, &deps) != nil {
				continue
			}
			sum := sha256.Sum256(pub)
			keys = append(keys, map[string]any{"key_id": keyID, "fingerprint": hex.EncodeToString(sum[:16]),
				"state": state, "can_sign": assignment.CanSign(state), "can_verify": assignment.CanVerify(state),
				"current_assignments": deps, "created_at": activated, "activated_at": activated, "verify_only_at": verifyOnlyAt,
				"revoked_at": revokedAt, "reason": strPtr(reason)})
		}
		rows.Close()
	}

	var registry any
	var regVersion int64
	var regIssued time.Time
	if err := b.DB.QueryRow(ctx, `SELECT registry_version, issued_at FROM assignment_registry WHERE is_current LIMIT 1`).
		Scan(&regVersion, &regIssued); err == nil {
		registry = map[string]any{"version": regVersion, "issued_at": regIssued}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"ca":              cas,
		"certificates":    map[string]any{"active": active, "revoked": revoked, "expiring_30d": expiring, "items": certs},
		"assignment_keys": keys,
		"registry":        registry,
	})
}

// listAudit: GET /cloud/v1/audit?customer_id=&appliance_id=&action=&since=&limit=&cursor=
//
// Platform roles read everything, including platform events that belong to no customer. A customer-scoped
// caller reads only its own customer's records. The cursor is the ts of the last row returned.
func (b *Base) listAudit(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var since, before *time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "since must be RFC3339")
			return
		}
		since = &t
	}
	if v := q.Get("cursor"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad cursor")
			return
		}
		before = &t
	}
	limit := ParseLimit(r, 100, 500)
	var actions []string
	for _, a := range strings.Split(q.Get("action"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			actions = append(actions, a)
		}
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT ts, tenant_id::text, actor_type, actor_id, action, target_type, target_id, host(ip), payload
          FROM audit_log
         WHERE ($1 = '' OR tenant_id::text = $1)
           AND ($2 = '' OR (target_type = 'appliance' AND target_id = $2) OR payload->>'appliance_id' = $2)
           AND (COALESCE(cardinality($3::text[]), 0) = 0 OR action = ANY($3) OR action LIKE ANY(
                   SELECT a || '.%' FROM unnest($3::text[]) a))
           AND ($4::timestamptz IS NULL OR ts >= $4)
           AND ($5::timestamptz IS NULL OR ts < $5)
         ORDER BY ts DESC
         LIMIT $6`, scope.Filter(q.Get("customer_id")), q.Get("appliance_id"), actions, since, before, limit)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	var last time.Time
	for rows.Next() {
		var ts time.Time
		var tenant, actorID, targetType, targetID, ip *string
		var actorType, action string
		var payload json.RawMessage
		if rows.Scan(&ts, &tenant, &actorType, &actorID, &action, &targetType, &targetID, &ip, &payload) != nil {
			continue
		}
		last = ts
		out = append(out, map[string]any{"ts": ts, "customer_id": tenant, "actor_type": actorType,
			"actor_id": actorID, "action": action, "target_type": targetType, "target_id": targetID,
			"ip": ip, "payload": payload})
	}
	var next any
	if len(out) == limit {
		next = last.UTC().Format(time.RFC3339Nano)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}
