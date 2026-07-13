package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

type registerReq struct {
	Serial              string `json:"serial"`
	WANMAC              string `json:"wan_mac"`
	LANMAC              string `json:"lan_mac"`
	HardwareFingerprint string `json:"hardware_fingerprint"`
	Hostname            string `json:"hostname"`
	Model               string `json:"model"`
	PublicKey           string `json:"public_key"` // base64-raw Ed25519 (32 bytes)
}

// RegisterHandler is PUBLIC and TOKEN-LESS. A factory-clean appliance generates
// its Ed25519 identity locally and calls this endpoint with a request SIGNED BY
// THAT KEY (trust-on-first-use): the signature proves possession of the private
// key for the enclosed public key. On success the appliance appears as Pending
// Activation with its hardware inventory; no bootstrap token is required for the
// normal online flow.
//
// Clone protection: the identity key is the trust anchor. If a request presents
// a KNOWN identity key from DIFFERENT hardware (different serial / hardware
// fingerprint), that is a copied-identity clone — we raise a security alert and
// reject, and (because the license binds to the original hardware) the clone can
// never install an active license anyway.
func (b *EnrollmentBase) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	var req registerReq
	if err := json.Unmarshal(raw, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	req.Serial = strings.TrimSpace(req.Serial)
	req.PublicKey = strings.TrimSpace(req.PublicKey)
	if req.Serial == "" || req.PublicKey == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "serial and public_key required")
		return
	}
	pubRaw, err := base64.RawStdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "public_key must be base64-raw Ed25519 (32 bytes)")
		return
	}
	pub := ed25519.PublicKey(pubRaw)

	// Verify the self-signed registration JWT against the ENCLOSED key (TOFU).
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "signed registration required")
		return
	}
	if _, err := applianceauth.VerifyRequest(token, pub, time.Now(), applianceauth.RequestParams{
		Audience: "stayconnect-cloud-api",
		Method:   http.MethodPost,
		Path:     "/v1/appliances/register",
		Body:     raw,
		KeyID:    applianceauth.KeyID(pub),
	}); err != nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "invalid registration signature")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	ip := clientIP(r)

	// (1) Known identity key → same appliance re-registering, OR a clone.
	var appID, exSerial, exHWF, exLifecycle string
	err = b.DB.QueryRow(ctx,
		`SELECT id::text, serial, COALESCE(hardware_fingerprint,''), COALESCE(lifecycle_state,'')
           FROM appliances WHERE public_key = $1 LIMIT 1`, req.PublicKey).
		Scan(&appID, &exSerial, &exHWF, &exLifecycle)
	if err == nil {
		hwMismatch := (exSerial != "" && exSerial != req.Serial) ||
			(exHWF != "" && req.HardwareFingerprint != "" && exHWF != req.HardwareFingerprint)
		if hwMismatch {
			_, _ = b.DB.Exec(ctx, `
                INSERT INTO appliance_security_alerts(appliance_id, serial, kind, detail, source_ip)
                VALUES ($1, $2, 'identity_hardware_mismatch', $3::jsonb, $4)`,
				appID, req.Serial,
				`{"reason":"known identity key presented from different hardware (possible clone)"}`, ip)
			Fail(w, r, http.StatusForbidden, CodeForbidden, "registration rejected: identity/hardware mismatch (possible clone)")
			return
		}
		// Same physical box → refresh inventory + last seen.
		_, _ = b.DB.Exec(ctx, `
            UPDATE appliances SET
                wan_mac = COALESCE(NULLIF($2,''), wan_mac),
                lan_mac = COALESCE(NULLIF($3,''), lan_mac),
                hardware_fingerprint = COALESCE(NULLIF($4,''), hardware_fingerprint),
                hostname = COALESCE(NULLIF($5,''), hostname),
                model = COALESCE(NULLIF($6,''), model),
                last_seen_at = now(), last_public_ip = $7, updated_at = now()
             WHERE id = $1`,
			appID, req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model, ip)
		state := exLifecycle
		if state == "" {
			state = "pending_approval"
		}
		WriteJSON(w, http.StatusOK, map[string]any{"appliance_id": appID, "status": state})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}

	// (2) New identity key. If this HARDWARE already has a row (same serial), it
	// is a factory-reset re-registration (new key, same box). Reuse the row when
	// it is not actively assigned; alert when it is.
	var reuseID, reuseState string
	_ = b.DB.QueryRow(ctx,
		`SELECT id::text, COALESCE(lifecycle_state,'') FROM appliances WHERE serial = $1 LIMIT 1`, req.Serial).
		Scan(&reuseID, &reuseState)
	// A new identity key may only reuse a serial that is NOT in active use. Any
	// claimed/assigned/activated/online state means the hardware already has a
	// live identity — a new key there is a clone or a hijack of a known serial
	// (serial + hardware fingerprint are not secret), so we alert and reject.
	// A legit factory-reset of an active box requires the operator to decommission
	// it first (or use a bootstrap token) — a deliberate, audited action.
	reusable := map[string]bool{"pending_approval": true, "installed_unenrolled": true, "": true,
		"revoked": true, "decommissioned": true, "retired": true}
	if reuseID != "" {
		if !reusable[reuseState] {
			_, _ = b.DB.Exec(ctx, `
                INSERT INTO appliance_security_alerts(appliance_id, serial, kind, detail, source_ip)
                VALUES ($1, $2, 'hardware_reused', $3::jsonb, $4)`,
				reuseID, req.Serial,
				`{"reason":"new identity key registered on hardware that already has an active appliance"}`, ip)
			Fail(w, r, http.StatusForbidden, CodeForbidden, "registration rejected: hardware already activated under another identity")
			return
		}
		// Re-register after factory reset: adopt the new identity on the existing
		// row, keep it Pending. No duplicate appliance is created.
		_, _ = b.DB.Exec(ctx, `
            UPDATE appliances SET
                public_key = $2, status = 'pending', lifecycle_state = 'pending_approval',
                tenant_id = NULL, site_id = NULL,
                wan_mac = NULLIF($3,''), lan_mac = NULLIF($4,''), hardware_fingerprint = NULLIF($5,''),
                hostname = NULLIF($6,''), model = NULLIF($7,''),
                enrolled_at = now(), first_seen_at = COALESCE(first_seen_at, now()),
                last_seen_at = now(), last_public_ip = $8, updated_at = now()
             WHERE id = $1`,
			reuseID, req.PublicKey, req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model, ip)
		WriteJSON(w, http.StatusOK, map[string]any{"appliance_id": reuseID, "status": "pending_approval"})
		return
	}

	// (3) Brand-new appliance → Pending Activation (unassigned).
	err = b.DB.QueryRow(ctx, `
        INSERT INTO appliances(serial, name, status, lifecycle_state, public_key,
                               wan_mac, lan_mac, hardware_fingerprint, hostname, model,
                               enrolled_at, first_seen_at, last_seen_at, last_public_ip)
        VALUES ($1, $1, 'pending', 'pending_approval', $2,
                NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''),
                now(), now(), now(), $8)
        RETURNING id::text
    `, req.Serial, req.PublicKey, req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model, ip).Scan(&appID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "appliance.registered", "appliance", appID, map[string]any{
		"serial": req.Serial, "wan_mac": req.WANMAC, "source_ip": ip,
	})
	WriteJSON(w, http.StatusOK, map[string]any{"appliance_id": appID, "status": "pending_approval"})
}
