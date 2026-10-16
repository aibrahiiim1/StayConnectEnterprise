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
	// HoldsCustomerID is the customer whose data the appliance's local site database still holds; absent for
	// a factory-clean appliance. It is inside the signed body. Stored on every registration (NULL when
	// absent); activation for any other customer is refused (holdsOtherCustomerRefusal).
	HoldsCustomerID string `json:"holds_customer_id"`
}

// RegisterHandler is PUBLIC and TOKEN-LESS. A factory-clean appliance generates
// its Ed25519 identity locally and calls this endpoint with a request SIGNED BY
// THAT KEY (trust-on-first-use): the signature proves possession of the private
// key for the enclosed public key. On success the appliance appears as Pending
// Activation with its hardware inventory; no token of any kind is involved.
//
// Clone protection: the identity key is the trust anchor. If a request presents
// a KNOWN identity key from DIFFERENT hardware (different serial / hardware
// fingerprint), that is a copied-identity clone — we raise a security alert and
// reject, and (because the license binds to the original hardware) the clone can
// never install an active license anyway.
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

	// Verify the self-signed registration JWT against the ENCLOSED key (TOFU).
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
	// A captured registration is refused when replayed, like every other signed appliance request.
	if b.ReplayCache != nil {
		if err := b.ReplayCache.Use(claims.Jti, time.Now()); err != nil {
			Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "replay detected")
			return
		}
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	ip := clientIP(r)

	// (1) Known identity key → same appliance re-registering, OR a clone.
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
	// A RETIRED identity key never comes back as a waiting appliance, even after its record was deleted: the
	// box still holds whatever its previous customer left on it. Factory-reset it (a new key) first.
	if retired, err := identityRetired(ctx, b.DB, req.PublicKey); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	} else if retired {
		Fail(w, r, http.StatusForbidden, "identity_retired",
			"registration rejected: this appliance identity was retired; factory-reset the appliance to register it again")
		return
	}

	// (2) New identity key for hardware that has a LIVE row (same serial, not retired).
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
			// Activated hardware already has a live identity: a new key there is a clone or a hijack of a known
			// serial (serial and fingerprint are not secret). A legitimate factory reset of an activated box
			// requires the operator to retire it first -- a deliberate, audited action.
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

	// (3) No live row: a brand-new appliance, or a RETIRED box coming back after a factory reset. Either way it
	// gets a NEW row: whatever the retired row holds -- its licence history, audit trail, alerts, the previous
	// customer -- stays with that row and is never shown to whoever activates this one.
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
		// Anything a previous identity of this hardware held is dead: its key is remembered as retired (so it can
		// never register again) and any certificate still marked active is revoked.
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
			// A concurrent registration of the same serial won the race; this one is refused, never merged.
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

// replaceWaitingIdentity adopts a NEW identity key on a WAITING appliance row -- the box was factory-reset
// before anyone activated it. Serial and fingerprint are not secret, so the serial alone proves nothing: the
// replacement is accepted only when the hardware fingerprint Central recorded for this row is present and is
// exactly the one presented. Anything else is refused and raised as a security alert; the legitimate way
// out is to delete the waiting appliance in the console, after which the box registers as new. Every
// replacement is audited, accepted or refused.
func (b *IdentityBase) replaceWaitingIdentity(w http.ResponseWriter, r *http.Request, ctx context.Context,
	id, storedHWF string, req registerReq, held, ip string) {
	if storedHWF == "" || req.HardwareFingerprint == "" || storedHWF != req.HardwareFingerprint {
		reason := "the presented hardware fingerprint does not match the one recorded for this waiting appliance"
		if storedHWF == "" {
			reason = "Central has no hardware fingerprint for this waiting appliance, so a new identity cannot be bound to it"
		} else if req.HardwareFingerprint == "" {
			reason = "the registration carried no hardware fingerprint"
		}
		b.alertAndAudit(ctx, r, id, req.Serial, "identity_replacement_refused", map[string]any{
			"reason":          reason + " — someone who knows this serial may be trying to take over the waiting appliance",
			"serial":          req.Serial,
			"appliance_id":    id,
			"operator_action": "If this is the same box after a factory reset, delete the waiting appliance in Central; the box then registers as new.",
		}, ip)
		audit.Op(r.Context(), b.DB, r, "appliance.identity_replacement_refused", "appliance", id, map[string]any{
			"serial": req.Serial, "source_ip": ip, "new_identity_key_fingerprint": identityFprFromPubB64(req.PublicKey),
			"reason": reason})
		Fail(w, r, http.StatusForbidden, "identity_replacement_refused",
			"registration rejected: a waiting appliance with this serial is registered under another identity from different hardware")
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "registration failed")
		return
	}
	defer tx.Rollback(ctx)
	var oldPub, state string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(public_key,''), lifecycle_state FROM appliances WHERE id=$1 FOR UPDATE`, id).
		Scan(&oldPub, &state); err != nil || state != "pending_approval" {
		Fail(w, r, http.StatusConflict, "invalid_state", "the appliance changed state; retry")
		return
	}
	// The previous key is dead: remembered as retired so it can never register again, and any certificate it
	// held is revoked. A waiting appliance holds no licence and no assignment.
	if oldPub != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO retired_appliance_identities (public_key, serial, appliance_id, reason)
            VALUES ($1,$2,$3,'replaced by a new identity while waiting (factory reset)') ON CONFLICT (public_key) DO NOTHING`,
			oldPub, req.Serial, id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "registration failed")
			return
		}
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason)
        SELECT c.id, c.appliance_id, c.fingerprint_sha256, 'identity replaced'
          FROM appliance_certificates c WHERE c.appliance_id=$1 AND c.status='active'
        ON CONFLICT DO NOTHING`, id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "registration failed")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(),
        revocation_reason='identity replaced' WHERE appliance_id=$1 AND status='active'`, id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "registration failed")
		return
	}
	if _, err := tx.Exec(ctx, `
        UPDATE appliances SET
            public_key = $2, current_cert_fingerprint = NULL,
            wan_mac = NULLIF($3,''), lan_mac = NULLIF($4,''),
            hostname = NULLIF($5,''), model = NULLIF($6,''),
            held_customer_id = NULLIF($8,'')::uuid,
            registered_at = now(), last_seen_at = now(), last_public_ip = $7, updated_at = now()
         WHERE id = $1`, id, req.PublicKey, req.WANMAC, req.LANMAC, req.Hostname, req.Model, ip, held); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "registration failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "registration failed")
		return
	}
	details := map[string]any{"serial": req.Serial, "source_ip": ip,
		"previous_identity_key_fingerprint": identityFprFromPubB64(oldPub),
		"new_identity_key_fingerprint":      identityFprFromPubB64(req.PublicKey)}
	if held != "" {
		details["holds_customer_id"] = held
	}
	audit.Op(r.Context(), b.DB, r, "appliance.identity_replaced", "appliance", id, details)
	WriteJSON(w, http.StatusOK, map[string]any{"appliance_id": id, "status": "pending_approval"})
}

// alertAndAudit raises (or folds into an open) security alert and audits only when a NEW alert was raised, so
// repeated unauthenticated attempts cost one alert and one audit row per window.
func (b *IdentityBase) alertAndAudit(ctx context.Context, r *http.Request, applianceID, serial, kind string, detail map[string]any, ip string) {
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

// identityRetired reports whether this identity key belonged to a retired appliance.
func identityRetired(ctx context.Context, db *pgxpool.Pool, publicKey string) (bool, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM retired_appliance_identities WHERE public_key=$1`, publicKey).Scan(&n)
	return n > 0, err
}
