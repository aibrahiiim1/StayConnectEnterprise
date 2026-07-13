package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	pki "github.com/stayconnect/enterprise/control-plane/internal/pki"
)

// terminalTimeout is how long Phase 1 waits for the appliance's signed ack before
// raising a security alert (and NOT falsely reporting the box decommissioned).
func terminalTimeout() time.Duration { return 10 * time.Minute }

// beginTerminalDelivery starts retirement of an appliance.
//
// NORMAL (emergency=false) — two phases:
//
//	Phase 1: mint a higher-version signed TERMINAL assignment (revoked/unassigned/
//	         decommissioned) and record delivery as pending. Credentials are NOT
//	         touched, so the still-trusted appliance can fetch the document, clear
//	         its authority, and return a signed acknowledgment. Phase 2 (cert +
//	         NATS shutdown) runs only when that ack arrives (see AckHandler).
//
// EMERGENCY (emergency=true) — do NOT depend on a compromised box cooperating:
//
//	revoke the certificate, deny NATS + all API access immediately, and mark the
//	terminal delivery UNCONFIRMED. A signed terminal document is still minted (so a
//	recovered box would stand down), but nothing waits for it.
func (b *Base) beginTerminalDelivery(ctx context.Context, r *http.Request, id, terminalState, reason string, emergency bool) (int64, error) {
	if terminalState != assignment.StateRevoked && terminalState != assignment.StateUnassigned &&
		terminalState != assignment.StateDecommissioned {
		return 0, fmt.Errorf("invalid terminal state %q", terminalState)
	}
	// Mint + persist the signed terminal assignment (higher version). This uses the
	// signing guard (active key only) inside issueAssignment. The minted terminal
	// document carries NO tenant/site — issueAssignment only stamps ownership for a
	// Grants() state, and a terminal state Clears(). It is the APPLIANCE that
	// atomically clears its own tenant/site when it verifies and adopts the document;
	// Central's row keeps its last tenant_id (NOT NULL) with lifecycle_state marking
	// the retirement, and the signed document remains authoritative.
	if err := b.issueAssignment(ctx, id, terminalState); err != nil {
		return 0, err
	}
	var ver int64
	_ = b.DB.QueryRow(ctx, `SELECT version FROM appliance_signed_assignments WHERE appliance_id=$1`, id).Scan(&ver)

	deliveryState := "terminal_delivery_pending"
	var timeoutAt any = time.Now().Add(terminalTimeout())
	if emergency {
		// Credentials pulled now; the ack (if it ever comes) is irrelevant.
		deliveryState = "credential_revoked"
		timeoutAt = nil
	}
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO appliance_terminal_delivery
            (appliance_id, terminal_state, assignment_version, delivery_state, reason, emergency, issued_at, timeout_at)
        VALUES ($1,$2,$3,$4,$5,$6,now(),$7)
        ON CONFLICT (appliance_id) DO UPDATE SET
            terminal_state=EXCLUDED.terminal_state, assignment_version=EXCLUDED.assignment_version,
            delivery_state=EXCLUDED.delivery_state, reason=EXCLUDED.reason, emergency=EXCLUDED.emergency,
            issued_at=now(), timeout_at=EXCLUDED.timeout_at, acked_at=NULL, credential_revoked_at=NULL`,
		id, terminalState, ver, deliveryState, reason, emergency, timeoutAt); err != nil {
		return 0, err
	}

	if emergency {
		// Immediate Phase 2 — the guardrail is the caller (confirmation + step-up).
		if err := b.phase2ShutCredentials(ctx, r, id, "emergency_compromise"); err != nil {
			return ver, err
		}
		// Lifecycle goes terminal now; delivery stays UNCONFIRMED (acked_at NULL).
		_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state=$2, status='retired', updated_at=now() WHERE id=$1`, id, terminalState)
	}
	return ver, nil
}

// terminalAction is the Platform handler for /revoke and /decommission. Normal
// path = Phase 1 (deliver, wait for ack). emergency_compromise=true = immediate
// credential shutdown, which additionally requires a typed confirmation.
func (b *Base) terminalAction(terminalState string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var in struct {
			Reason       string `json:"reason"`
			Emergency    bool   `json:"emergency_compromise"`
			Confirmation string `json:"confirmation"`
		}
		_ = DecodeJSON(r, &in)
		if in.Reason == "" {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
			return
		}
		ctx, cancel := DBCtx(r)
		defer cancel()
		var prev, serial string
		if err := b.DB.QueryRow(ctx, `SELECT lifecycle_state, COALESCE(serial,'') FROM appliances WHERE id=$1`, id).Scan(&prev, &serial); err != nil {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
			return
		}
		if in.Emergency && in.Confirmation != serial && in.Confirmation != id {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest,
				"emergency compromise requires confirmation=<serial> (typed) to proceed with immediate credential revocation")
			return
		}
		ver, err := b.beginTerminalDelivery(ctx, r, id, terminalState, in.Reason, in.Emergency)
		if err != nil {
			Fail(w, r, http.StatusServiceUnavailable, "terminal_delivery_failed",
				"could not start terminal delivery: "+err.Error())
			return
		}
		// Commercial authority ends the moment the operator retires the box — it does
		// NOT wait for the appliance's credential ack (that is only for cert/NATS).
		// Centralized so revoke/decommission/emergency can never leave the license active.
		licRevoked, _ := b.revokeApplianceBoundLicenses(ctx, id)
		sess := auth.FromContext(r.Context())
		recordLifecycle(ctx, b.DB, id, prev, terminalState, emailOf(sess), clientIPFromReq(r), in.Reason)
		action := "appliance." + terminalState
		phase := "phase1_delivery_pending"
		if in.Emergency {
			action += "_emergency"
			phase = "emergency_credentials_revoked"
		}
		audit.Op(ctx, b.DB, r, action, "appliance", id, map[string]any{
			"reason": in.Reason, "prev_state": prev, "emergency_compromise": in.Emergency,
			"assignment_version": ver, "licenses_revoked": len(licRevoked),
		})
		var deliveryState string
		_ = b.DB.QueryRow(ctx, `SELECT delivery_state FROM appliance_terminal_delivery WHERE appliance_id=$1`, id).Scan(&deliveryState)
		WriteJSON(w, http.StatusOK, map[string]any{
			"status": terminalState, "phase": phase, "emergency_compromise": in.Emergency,
			"assignment_version": ver, "delivery_state": deliveryState,
			"note": ternary(in.Emergency,
				"credentials revoked immediately; terminal delivery is UNCONFIRMED — local factory reset / controlled recovery required",
				"terminal assignment delivered; awaiting the appliance's signed acknowledgment before revoking credentials"),
		})
	}
}

func ternary(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// terminalDeliveryStatus exposes the two-phase progress to the Platform console.
func (b *Base) terminalDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var terminalState, deliveryState, reason string
	var version int64
	var emergency bool
	var issuedAt time.Time
	var timeoutAt, ackedAt, credRevokedAt *time.Time
	var ackVersion *int64
	var ackFpr *string
	err := b.DB.QueryRow(ctx, `
        SELECT terminal_state, delivery_state, COALESCE(reason,''), assignment_version, emergency,
               issued_at, timeout_at, acked_at, credential_revoked_at, ack_version, ack_fingerprint
          FROM appliance_terminal_delivery WHERE appliance_id=$1`, id).
		Scan(&terminalState, &deliveryState, &reason, &version, &emergency,
			&issuedAt, &timeoutAt, &ackedAt, &credRevokedAt, &ackVersion, &ackFpr)
	if err != nil {
		WriteJSON(w, http.StatusOK, map[string]any{"terminal": false})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"terminal": true, "terminal_state": terminalState, "delivery_state": deliveryState,
		"assignment_version": version, "emergency_compromise": emergency, "reason": reason,
		"issued_at": issuedAt, "timeout_at": timeoutAt, "acked_at": ackedAt,
		"credential_revoked_at": credRevokedAt, "ack_version": ackVersion, "ack_fingerprint": ackFpr,
	})
}

// phase2ShutCredentials revokes the appliance's client certificate (which the
// NATS Auth-Callout also honours, denying reconnect and killing the live session
// within the JWT TTL) and records the shutdown. It is called after a verified ack
// (normal) or immediately (emergency).
func (b *Base) phase2ShutCredentials(ctx context.Context, r *http.Request, id, reason string) error {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason)
        SELECT c.id, c.appliance_id, c.fingerprint_sha256, $2
          FROM appliance_certificates c WHERE c.appliance_id=$1 AND c.status='active'
        ON CONFLICT DO NOTHING`, id, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(), revocation_reason=$2 WHERE appliance_id=$1 AND status='active'`, id, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliances SET current_cert_fingerprint=NULL, updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_terminal_delivery SET credential_revoked_at=now() WHERE appliance_id=$1`, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if r != nil {
		audit.Op(ctx, b.DB, r, "appliance.credentials_revoked", "appliance", id,
			map[string]any{"reason": reason})
	} else {
		audit.System(ctx, b.DB, "appliance.credentials_revoked", "appliance", id, map[string]any{"reason": reason})
	}
	return nil
}

// AckHandler receives the appliance's SIGNED terminal-adoption acknowledgment over
// the strict mTLS channel, and — only if it verifies — runs Phase 2.
func (b *Base) AckHandler(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "appliance identity required")
		return
	}
	if !strictMTLSSelf(w, r, b.DB, ident.ApplianceID) {
		return
	}
	var ack assignment.Ack
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&ack); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad ack")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Ack must be self-scoped and signed by THIS appliance's identity key.
	if ack.ApplianceID != ident.ApplianceID {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "ack is for a different appliance")
		return
	}
	var pubB64 string
	if err := b.DB.QueryRow(ctx, `SELECT COALESCE(public_key,'') FROM appliances WHERE id=$1`, ident.ApplianceID).Scan(&pubB64); err != nil || pubB64 == "" {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	pub, ok := decodeApplPub(pubB64)
	if !ok || !assignment.VerifyAck(pub, &ack) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "ack signature invalid")
		return
	}
	if !assignment.Clears(ack.TerminalState) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "ack terminal_state is not a terminal state")
		return
	}

	// The ack must name the CURRENT terminal document (version + fingerprint).
	var version int64
	var signedDoc []byte
	var deliveryState string
	err := b.DB.QueryRow(ctx, `
        SELECT sa.version, sa.signed_doc, COALESCE(td.delivery_state,'')
          FROM appliance_signed_assignments sa
          LEFT JOIN appliance_terminal_delivery td ON td.appliance_id = sa.appliance_id
         WHERE sa.appliance_id=$1`, ident.ApplianceID).Scan(&version, &signedDoc, &deliveryState)
	if err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "no assignment to acknowledge")
		return
	}
	var doc assignment.Document
	_ = json.Unmarshal(signedDoc, &doc)
	if ack.Version != version || ack.Fingerprint != assignment.DocFingerprint(&doc) {
		Fail(w, r, http.StatusConflict, "ack_mismatch", "ack does not match the current terminal assignment")
		return
	}

	// Record the ack, mark adopted, then run Phase 2.
	raw, _ := json.Marshal(ack)
	_, _ = b.DB.Exec(ctx, `
        UPDATE appliance_terminal_delivery
           SET delivery_state='terminal_adopted', acked_at=now(), ack_version=$2,
               ack_fingerprint=$3, ack_adopted_at=$4, ack_signed=$5
         WHERE appliance_id=$1`, ident.ApplianceID, ack.Version, ack.Fingerprint, ack.AdoptedAt, raw)
	_, _ = b.DB.Exec(ctx, `UPDATE appliance_signed_assignments SET last_acked_version=$2 WHERE appliance_id=$1`, ident.ApplianceID, ack.Version)
	audit.Op(ctx, b.DB, r, "appliance.terminal_adopted", "appliance", ident.ApplianceID,
		map[string]any{"version": ack.Version, "terminal_state": ack.TerminalState, "fingerprint": ack.Fingerprint})

	if err := b.phase2ShutCredentials(ctx, r, ident.ApplianceID, "terminal_ack"); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "phase2 failed")
		return
	}
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state=$2, status='retired', updated_at=now() WHERE id=$1`,
		ident.ApplianceID, ack.TerminalState)
	_, _ = b.DB.Exec(ctx, `UPDATE appliance_terminal_delivery SET delivery_state='credential_revoked' WHERE appliance_id=$1`, ident.ApplianceID)
	WriteJSON(w, http.StatusOK, map[string]any{"status": "credential_revoked", "version": ack.Version})
}

// ReconcileTerminalTimeouts flips pending deliveries past their timeout to
// terminal_delivery_failed and raises a security alert. It does NOT report the
// appliance as decommissioned — the ack never came, so completion is unconfirmed.
func ReconcileTerminalTimeouts(ctx context.Context, b *Base) (int64, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT appliance_id::text FROM appliance_terminal_delivery
         WHERE delivery_state='terminal_delivery_pending' AND timeout_at IS NOT NULL AND timeout_at < now()`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var n int64
	for _, id := range ids {
		_, _ = b.DB.Exec(ctx, `UPDATE appliance_terminal_delivery SET delivery_state='terminal_delivery_failed' WHERE appliance_id=$1`, id)
		var serial string
		_ = b.DB.QueryRow(ctx, `SELECT COALESCE(serial,'') FROM appliances WHERE id=$1`, id).Scan(&serial)
		_, _ = b.DB.Exec(ctx, `
            INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail, status)
            VALUES ($1,$2,'terminal_delivery_failed',
                    'appliance did not acknowledge a terminal assignment within policy; retirement is UNCONFIRMED — credentials NOT revoked. Investigate or use emergency compromise.','open')`,
			id, serial)
		audit.System(ctx, b.DB, "appliance.terminal_delivery_failed", "appliance", id,
			map[string]any{"note": "no ack within policy; not reported as decommissioned"})
		n++
	}
	return n, nil
}

// -------- strict terminal endpoint --------

// StrictApplianceAssignmentHandler serves GET /v1/appliance/assignment under the
// full mTLS trust rules. It is the ONLY delivery channel for assignment documents
// (no JWT/bootstrap fallback; that mount is removed from the :443 router).
func (b *AssignmentBase) StrictApplianceAssignmentHandler(w http.ResponseWriter, r *http.Request) {
	// IDENTITY COMES ONLY FROM THE VERIFIED CLIENT CERTIFICATE. This endpoint
	// consults no appliance JWT, bearer, bootstrap or enrollment token — any
	// Authorization header on the request is ignored. The mTLS listener has
	// already verified the certificate chain against the CA; here we bind the
	// cert's URI-SAN appliance_id, and strictMTLSSelf enforces the exact
	// fingerprint + serial match to this appliance's active Central record.
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		logFetch(r.Context(), b.DB, "", "", "denied:no-cert")
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "client certificate required (mTLS only)")
		return
	}
	appID := pki.ApplianceIDFromCert(r.TLS.PeerCertificates[0])
	if appID == "" {
		logFetch(r.Context(), b.DB, "", "", "denied:no-uri-san")
		Fail(w, r, http.StatusForbidden, CodeForbidden, "certificate carries no appliance identity (URI-SAN)")
		return
	}
	if !strictMTLSSelf(w, r, b.DB, appID) {
		logFetch(r.Context(), b.DB, appID, "", "denied:mtls")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	var version, lastAcked int64
	var signedDoc []byte
	err := b.DB.QueryRow(ctx,
		`SELECT version, last_acked_version, signed_doc FROM appliance_signed_assignments WHERE appliance_id=$1`,
		appID).Scan(&version, &lastAcked, &signedDoc)
	if errors.Is(err, pgx.ErrNoRows) {
		logFetch(ctx, b.DB, appID, "", "not-modified:none")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "assignment lookup failed")
		return
	}

	// Nothing newer than what the appliance already acknowledged.
	if version <= lastAcked {
		logFetch(ctx, b.DB, appID, "", "not-modified:acked")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var doc assignment.Document
	if json.Unmarshal(signedDoc, &doc) != nil {
		logFetch(ctx, b.DB, appID, "", "denied:corrupt")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Self-scoped + identity-bound: never another appliance's document.
	idFpr := ""
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(public_key,'') FROM appliances WHERE id=$1`, appID).Scan(&idFpr)
	if doc.ApplianceID != appID ||
		(doc.IdentityKeyFpr != "" && doc.IdentityKeyFpr != identityFprFromPubB64(idFpr)) {
		logFetch(ctx, b.DB, appID, "", "denied:not-self")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Signed by a currently-trusted (active|verify_only) assignment key — never an
	// unknown or revoked signer.
	var signerState string
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(state,'') FROM assignment_signing_keys WHERE key_id=$1`, doc.SignerKeyID).Scan(&signerState)
	if !assignment.CanVerify(signerState) {
		logFetch(ctx, b.DB, appID, doc.SignerKeyID, "denied:untrusted-signer")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	logFetch(ctx, b.DB, appID, assignment.DocFingerprint(&doc), "served")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(signedDoc)
}

// strictMTLSSelf enforces the certificate rules for the assignment channel:
// mTLS present, cert URI-SAN == self, and an exact fingerprint+serial match to a
// certificate that was actually issued to this appliance and is not revoked.
func strictMTLSSelf(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool, applianceID string) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "client certificate required (mTLS only)")
		return false
	}
	cert := r.TLS.PeerCertificates[0]
	if pki.ApplianceIDFromCert(cert) != applianceID {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "certificate not bound to this appliance")
		return false
	}
	fpr := pki.FingerprintHex(cert)
	// cert_serial is stored as lowercase hex at issuance (pki.SerialHex = "%x"),
	// NOT decimal — match that exact form or the serial check never passes.
	certSerial := fmt.Sprintf("%x", cert.SerialNumber)
	var n int
	_ = db.QueryRow(r.Context(), `
        SELECT count(*) FROM appliance_certificates
         WHERE appliance_id=$1 AND fingerprint_sha256=$2 AND cert_serial=$3 AND status='active'`,
		applianceID, fpr, certSerial).Scan(&n)
	if n == 0 {
		Fail(w, r, http.StatusForbidden, CodeForbidden,
			"certificate fingerprint/serial not an active credential issued to this appliance")
		return false
	}
	return true
}

func decodeApplPub(b64 string) (ed25519.PublicKey, bool) {
	raw, err := base64.RawStdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		if r2, e2 := base64.StdEncoding.DecodeString(b64); e2 == nil && len(r2) == ed25519.PublicKeySize {
			return ed25519.PublicKey(r2), true
		}
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

func logFetch(ctx context.Context, db *pgxpool.Pool, applianceID, fpr, outcome string) {
	_, _ = db.Exec(ctx, `INSERT INTO appliance_assignment_fetch_log (appliance_id, fingerprint, outcome)
        VALUES (NULLIF($1,'')::uuid,NULLIF($2,''),$3)`, applianceID, fpr, outcome)
}
