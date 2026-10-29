package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
	// HoldsCustomerID is inside the signed body; activation for any other customer is refused (holdsOtherCustomerRefusal).
	HoldsCustomerID string `json:"holds_customer_id"`
}

// RegisterHandler is public and token-less: the request is signed by the appliance's own new identity key
// (trust on first use), proving possession of the enclosed public key. The appliance then waits for activation.
//
// Clone protection: a known identity key presented from different hardware is a copied identity, so it raises a
// security alert and is rejected.
func (b *IdentityBase) RegisterHandler(w http.ResponseWriter, r *http.Request) {
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
	facts, err := applianceFacts{Serial: req.Serial, Hostname: req.Hostname, Model: req.Model,
		WANMAC: req.WANMAC, LANMAC: req.LANMAC, Fingerprint: req.HardwareFingerprint}.normalize()
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	req.Serial, req.Hostname, req.Model = facts.Serial, facts.Hostname, facts.Model
	req.WANMAC, req.LANMAC, req.HardwareFingerprint = facts.WANMAC, facts.LANMAC, facts.Fingerprint
	pubRaw, err := base64.RawStdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "public_key must be base64-raw Ed25519 (32 bytes)")
		return
	}
	pub := ed25519.PublicKey(pubRaw)
	held, ok := normalizeHeldCustomer(req.HoldsCustomerID)
	if !ok {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "holds_customer_id must be a customer id (uuid)")
		return
	}

	// Verified against the enclosed key (trust on first use).
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "signed registration required")
		return
	}
	claims, err := applianceauth.VerifyRequest(token, pub, time.Now(), applianceauth.RequestParams{
		Audience: "stayconnect-cloud-api",
		Method:   http.MethodPost,
		Path:     "/v1/appliances/register",
		Body:     raw,
		KeyID:    applianceauth.KeyID(pub),
	})
	if err != nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "invalid registration signature")
		return
	}
	// A captured registration is refused when replayed.
	if b.ReplayCache != nil {
		if err := b.ReplayCache.Use(claims.Jti, time.Now()); errors.Is(err, applianceauth.ErrReplayCacheFull) {
			Fail(w, r, http.StatusServiceUnavailable, "busy", "Central is busy; try again shortly")
			return
		} else if err != nil {
			Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "replay detected")
			return
		}
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	ip := clientIP(r)

	// A known identity key is the same appliance re-registering, or a clone.
	var appID, exSerial, exHWF, exLifecycle string
	err = b.DB.QueryRow(ctx,
		`SELECT id::text, serial, COALESCE(hardware_fingerprint,''), COALESCE(lifecycle_state,'')
           FROM appliances WHERE public_key = $1 ORDER BY created_at DESC LIMIT 1`, req.PublicKey).
		Scan(&appID, &exSerial, &exHWF, &exLifecycle)
	if err == nil {
		hwMismatch := (exSerial != "" && exSerial != req.Serial) ||
			(exHWF != "" && req.HardwareFingerprint != "" && exHWF != req.HardwareFingerprint)
		if hwMismatch {
			b.alertAndAudit(ctx, r, appID, req.Serial, "identity_hardware_mismatch", map[string]any{
				"reason":             "a KNOWN identity key was presented from DIFFERENT hardware (serial/fingerprint) — possible cloned/copied identity",
				"presented_serial":   req.Serial,
				"known_serial":       exSerial,
				"known_appliance_id": appID,
				"operator_action":    "possible clone; the license binds to the original hardware so a clone cannot activate. Investigate.",
			}, ip)
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
                held_customer_id = NULLIF($8,'')::uuid,
                last_seen_at = now(), last_public_ip = $7, updated_at = now()
             WHERE id = $1`,
			appID, req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model, ip, held)
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
	// A retired identity key never returns as a waiting appliance, even after deletion: the box may still hold
	// the previous customer's data. It must be factory-reset (new key) first.
	if retired, err := identityRetired(ctx, b.DB, req.PublicKey); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	} else if retired {
		Fail(w, r, http.StatusForbidden, "identity_retired",
			"registration rejected: this appliance identity was retired; factory-reset the appliance to register it again")
		return
	}

	// A new identity key for hardware that has a live row (same serial, not retired).
	var liveID, liveState, liveHWF string
	err = b.DB.QueryRow(ctx, `
        SELECT id::text, COALESCE(lifecycle_state,''), COALESCE(hardware_fingerprint,'')
          FROM appliances WHERE serial = $1 AND lifecycle_state NOT IN ('revoked','decommissioned')
         LIMIT 1`, req.Serial).Scan(&liveID, &liveState, &liveHWF)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}
	if liveID != "" {
		if liveState != "pending_approval" && liveState != "" {
			// Serial and fingerprint are not secret, so a new key on activated hardware is a clone or hijack; a
			// legitimate factory reset requires the operator to retire it first.
			b.alertAndAudit(ctx, r, liveID, req.Serial, "hardware_reused", map[string]any{
				"reason":                "same physical hardware presented a NEW identity key while the previous identity still exists — most likely a local factory reset of this box",
				"same_hardware_serial":  req.Serial,
				"previous_appliance_id": liveID,
				"previous_state":        liveState,
				"factory_reset":         true,
				"operator_action":       "local-only reset; Central was NOT changed. To let this hardware re-register as a fresh Pending appliance, retire the previous appliance first (an explicit, audited decision).",
			}, ip)
			Fail(w, r, http.StatusForbidden, CodeForbidden,
				"registration rejected: this hardware (serial "+req.Serial+") already has an active appliance under another identity — retire it in Central first (likely a factory reset)")
			return
		}
		b.replaceWaitingIdentity(w, r, ctx, liveID, liveHWF, req, held, ip)
		return
	}

	// No live row: a new appliance, or a retired box back after a factory reset. Either gets a new row, so the
	// retired row's history and previous customer are never shown to whoever activates this one.
	if ref := constantAnchorRefusal(req.Serial, req.HardwareFingerprint); ref != nil { // AD-19
		Fail(w, r, ref.Status, ref.Code, ref.Message)
		return
	}
	if ref := b.registrationCapRefusal(ctx, r, req.Serial, ip); ref != nil { // AD-20
		Fail(w, r, ref.Status, ref.Code, ref.Message)
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
		return
	}
	defer tx.Rollback(ctx)
	var previous []string
	rows, err := tx.Query(ctx, `SELECT id::text FROM appliances WHERE serial=$1 ORDER BY created_at`, req.Serial)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			previous = append(previous, id)
		}
	}
	rows.Close()
	if len(previous) > 0 {
		// The previous identity is dead: its key is remembered as retired and any active certificate revoked.
		if _, err := tx.Exec(ctx, `
            INSERT INTO retired_appliance_identities (public_key, serial, appliance_id, reason)
            SELECT public_key, serial, id, 'hardware re-registered with a new identity (factory reset)'
              FROM appliances WHERE serial=$1 AND lifecycle_state IN ('revoked','decommissioned') AND COALESCE(public_key,'') <> ''
            ON CONFLICT (public_key) DO NOTHING`, req.Serial); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
			return
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason)
            SELECT c.id, c.appliance_id, c.fingerprint_sha256, 'hardware re-registered'
              FROM appliance_certificates c JOIN appliances a ON a.id = c.appliance_id
             WHERE a.serial=$1 AND c.status='active'
            ON CONFLICT DO NOTHING`, req.Serial); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
			return
		}
		if _, err := tx.Exec(ctx, `
            UPDATE appliance_certificates c SET status='revoked', revoked_at=now(), revocation_reason='hardware re-registered'
              FROM appliances a WHERE a.id = c.appliance_id AND a.serial=$1 AND c.status='active'`, req.Serial); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
			return
		}
	}
	err = tx.QueryRow(ctx, `
        INSERT INTO appliances(serial, lifecycle_state, public_key,
                               wan_mac, lan_mac, hardware_fingerprint, hostname, model,
                               registered_at, first_seen_at, last_seen_at, last_public_ip, held_customer_id)
        VALUES ($1, 'pending_approval', $2,
                NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''),
                now(), now(), now(), $8, NULLIF($9,'')::uuid)
        RETURNING id::text
    `, req.Serial, req.PublicKey, req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model, ip, held).Scan(&appID)
	if err != nil {
		if isUniqueViolation(err) {
			// A concurrent registration of the same serial won; this one is refused, never merged.
			Fail(w, r, http.StatusConflict, CodeConflict, "another registration of this serial is in progress; retry")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
		return
	}
	details := map[string]any{"serial": req.Serial, "wan_mac": req.WANMAC, "source_ip": ip}
	if held != "" {
		details["holds_customer_id"] = held
	}
	action := "appliance.registered"
	if len(previous) > 0 {
		action = "appliance.registered_after_retirement"
		details["previous_appliance_ids"] = previous
	}
	audit.Op(r.Context(), b.DB, r, action, "appliance", appID, details)
	WriteJSON(w, http.StatusOK, map[string]any{"appliance_id": appID, "status": "pending_approval"})
}

// replaceWaitingIdentity refuses a new identity key on a WAITING row (AD-1). The serial and the hardware fingerprint
// are not secret, so neither proves that the new key belongs to the box the row describes; accepting it on a
// fingerprint match let anyone who knew both take over a waiting row and receive its assignment, licence and
// certificates. If the real box was factory-reset before activation, the operator deletes the waiting row and the
// box registers as new; the activation code then proves which box the operator is looking at. Every refusal is
// alerted and audited.
func (b *IdentityBase) replaceWaitingIdentity(w http.ResponseWriter, r *http.Request, ctx context.Context,
	id, storedHWF string, req registerReq, held, ip string) {
	reason := "a waiting appliance with this serial is already registered under another identity key"
	b.alertAndAudit(ctx, r, id, req.Serial, "identity_replacement_refused", map[string]any{
		"reason":          reason + " - a waiting appliance's identity key is never replaced; someone who knows this serial may be trying to take over the row",
		"serial":          req.Serial,
		"appliance_id":    id,
		"operator_action": "If this is the same box after a factory reset, delete the waiting appliance in Central; the box then registers as new. Compare the activation code on the appliance with the one Central shows before activating.",
	}, ip)
	audit.Op(r.Context(), b.DB, r, "appliance.identity_replacement_refused", "appliance", id, map[string]any{
		"serial": req.Serial, "source_ip": ip, "new_identity_key_fingerprint": identityFprFromPubB64(req.PublicKey),
		"reason": reason})
	Fail(w, r, http.StatusForbidden, "identity_replacement_refused",
		"registration rejected: a waiting appliance with this serial is registered under another identity")
}

// alertAndAudit audits only when a new alert was raised, so repeated unauthenticated attempts cost one row per window.
func (b *Base) alertAndAudit(ctx context.Context, r *http.Request, applianceID, serial, kind string, detail map[string]any, ip string) {
	isNew, err := raiseAlertOnce(ctx, b.DB, applianceID, serial, kind, detail, ip)
	if err != nil {
		slog.Warn("security alert not recorded", "kind", kind, "err", err)
		return
	}
	if isNew {
		audit.Op(r.Context(), b.DB, r, "security_alert."+kind, "appliance", applianceID,
			map[string]any{"serial": serial, "source_ip": ip})
	}
}

func identityRetired(ctx context.Context, db *pgxpool.Pool, publicKey string) (bool, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM retired_appliance_identities WHERE public_key=$1`, publicKey).Scan(&n)
	return n > 0, err
}
