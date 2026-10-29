package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	pki "github.com/stayconnect/enterprise/control-plane/internal/pki"
)

// beginTerminalDelivery starts retirement inside the caller's transaction, so the caller's licence revocation and
// the signed terminal assignment commit together (a revoked licence with no terminal document would leave the
// appliance on its cached assignment, never told).
//
// Normal, two phases:
//
//	Phase 1: mint a higher-version signed terminal assignment and record delivery as pending. Credentials are
//	         untouched so the appliance can fetch it, clear its authority and acknowledge. Phase 2 (cert
//	         revocation) runs only when that ack arrives (AckHandler).
//
// Emergency does not depend on a compromised box cooperating: the certificate is revoked and API access denied
// at once and delivery is marked unconfirmed. The terminal document is still minted so a recovered box stands down.
func (b *Base) beginTerminalDelivery(ctx context.Context, tx pgx.Tx, id, terminalState, reason string, emergency bool) (int64, error) {
	if terminalState != assignment.StateRevoked && terminalState != assignment.StateUnassigned &&
		terminalState != assignment.StateDecommissioned {
		return 0, fmt.Errorf("invalid terminal state %q", terminalState)
	}
	if b.AssignKey == nil {
		return 0, errors.New("assignment signing key not configured")
	}
	// The terminal document carries no tenant/site; the appliance clears its own when it adopts it, and Central's
	// row keeps its last tenant_id with lifecycle_state marking the retirement.
	ab := &AssignmentBase{Base: b, SignKey: b.AssignKey}
	doc, err := ab.IssueTx(ctx, tx, id, terminalState)
	if err != nil {
		return 0, err
	}
	ver := doc.Version

	deliveryState := "terminal_delivery_pending"
	var timeoutAt any = time.Now().Add(b.retirementAckTimeout(ctx, tx))
	if emergency {
		deliveryState = "credential_revoked"
		timeoutAt = nil
	}
	if _, err := tx.Exec(ctx, `
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
		// The guardrail is the caller (confirmation and step-up).
		if err := b.phase2ShutCredentials(ctx, tx, id, "emergency_compromise"); err != nil {
			return ver, err
		}
		if _, err := tx.Exec(ctx, `UPDATE appliances SET lifecycle_state=$2, replacement_pending=false, updated_at=now() WHERE id=$1`,
			id, lifecycleForTerminal(terminalState)); err != nil {
			return ver, err
		}
	}
	return ver, nil
}

func lifecycleForTerminal(state string) string {
	if state == assignment.StateRevoked {
		return "revoked"
	}
	return "decommissioned"
}

// phase2ShutCredentials revokes the client certificate and records the shutdown in the caller's transaction, after a
// verified ack (normal) or immediately (emergency). The caller audits appliance.credentials_revoked after commit.
func (b *Base) phase2ShutCredentials(ctx context.Context, tx pgx.Tx, id, reason string) error {
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason)
        SELECT c.id, c.appliance_id, c.fingerprint_sha256, $2
          FROM appliance_certificates c WHERE c.appliance_id=$1 AND c.status IN ('active','superseded')
        ON CONFLICT DO NOTHING`, id, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(), revocation_reason=$2
        WHERE appliance_id=$1 AND status IN ('active','superseded')`, id, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliances SET current_cert_fingerprint=NULL, updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE appliance_terminal_delivery SET credential_revoked_at=now() WHERE appliance_id=$1`, id)
	return err
}

// AckHandler receives the appliance's signed terminal acknowledgment over the strict mTLS channel and, only if it verifies, runs Phase 2.
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

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "ack failed")
		return
	}
	defer tx.Rollback(ctx)
	var prevLifecycle string
	if err := tx.QueryRow(ctx, `SELECT lifecycle_state FROM appliances WHERE id=$1 FOR UPDATE`, ident.ApplianceID).Scan(&prevLifecycle); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	// The ack must name the current terminal document of a retirement in progress; otherwise it could retire an
	// appliance nobody retired.
	var version int64
	var signedDoc []byte
	var deliveryState string
	err = tx.QueryRow(ctx, `
        SELECT sa.version, sa.signed_doc, COALESCE(td.delivery_state,'')
          FROM appliance_signed_assignments sa
          LEFT JOIN appliance_terminal_delivery td ON td.appliance_id = sa.appliance_id
         WHERE sa.appliance_id=$1`, ident.ApplianceID).Scan(&version, &signedDoc, &deliveryState)
	if err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "no assignment to acknowledge")
		return
	}
	if deliveryState != "terminal_delivery_pending" && deliveryState != "terminal_delivery_failed" {
		Fail(w, r, http.StatusConflict, "no_retirement_pending", "no retirement is waiting for this acknowledgment")
		return
	}
	var doc assignment.Document
	_ = json.Unmarshal(signedDoc, &doc)
	if !assignment.Clears(doc.State) || ack.Version != version || ack.Fingerprint != assignment.DocFingerprint(&doc) ||
		ack.TerminalState != doc.State {
		Fail(w, r, http.StatusConflict, "ack_mismatch", "ack does not match the current terminal assignment")
		return
	}

	raw, _ := json.Marshal(ack)
	terminal := lifecycleForTerminal(ack.TerminalState)
	steps := []struct {
		sql  string
		args []any
	}{
		{`UPDATE appliance_terminal_delivery
             SET delivery_state='terminal_adopted', acked_at=now(), ack_version=$2,
                 ack_fingerprint=$3, ack_adopted_at=$4, ack_signed=$5
           WHERE appliance_id=$1`, []any{ident.ApplianceID, ack.Version, ack.Fingerprint, ack.AdoptedAt, raw}},
		{`UPDATE appliance_signed_assignments SET last_acked_version=$2 WHERE appliance_id=$1`, []any{ident.ApplianceID, ack.Version}},
	}
	for _, s := range steps {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "ack failed")
			return
		}
	}
	if err := b.phase2ShutCredentials(ctx, tx, ident.ApplianceID, "terminal_ack"); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "phase2 failed")
		return
	}
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE appliances SET lifecycle_state=$2, replacement_pending=false, updated_at=now() WHERE id=$1`, []any{ident.ApplianceID, terminal}},
		{`UPDATE appliance_terminal_delivery SET delivery_state='credential_revoked' WHERE appliance_id=$1`, []any{ident.ApplianceID}},
		// A late ack answers the earlier "did not confirm" alert.
		{`UPDATE appliance_security_alerts SET status='resolved', resolved=true, acknowledged_at=now()
           WHERE appliance_id=$1 AND kind='terminal_delivery_failed' AND NOT resolved`, []any{ident.ApplianceID}},
	} {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "ack failed")
			return
		}
	}
	recordLifecycle(ctx, tx, ident.ApplianceID, prevLifecycle, terminal, "appliance",
		clientIP(r), "retirement acknowledged by the appliance")
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "ack failed")
		return
	}
	audit.Op(ctx, b.DB, r, "appliance.terminal_adopted", "appliance", ident.ApplianceID,
		map[string]any{"version": ack.Version, "terminal_state": ack.TerminalState, "fingerprint": ack.Fingerprint})
	audit.Op(ctx, b.DB, r, "appliance.credentials_revoked", "appliance", ident.ApplianceID, map[string]any{"reason": "terminal_ack"})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "credential_revoked", "version": ack.Version})
}

// ReconcileTerminalTimeouts flips overdue pending deliveries to terminal_delivery_failed and raises an alert; the
// appliance is not reported decommissioned because the ack never came.
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
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var n int64
	for _, id := range ids {
		// The state change and its alert commit together, so a failed alert never leaves a silent failure
		// (audit Phase 1 L6). The state is re-checked under the update.
		raised, err := failTerminalDelivery(ctx, b, id)
		if err != nil {
			return n, err
		}
		if !raised {
			continue
		}
		audit.System(ctx, b.DB, "appliance.terminal_delivery_failed", "appliance", id,
			map[string]any{"note": "no ack within policy; not reported as decommissioned"})
		n++
	}
	return n, nil
}

func failTerminalDelivery(ctx context.Context, b *Base, id string) (bool, error) {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE appliance_terminal_delivery SET delivery_state='terminal_delivery_failed'
         WHERE appliance_id=$1 AND delivery_state='terminal_delivery_pending'`, id)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	var serial string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(serial,'') FROM appliances WHERE id=$1`, id).Scan(&serial); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	// detail is jsonb; a bare string is rejected by Postgres.
	detail, _ := json.Marshal(map[string]string{
		"reason": "the appliance did not acknowledge its retirement within policy; retirement is UNCONFIRMED and its credentials were NOT revoked. Investigate, or retire it again as an emergency.",
	})
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail, status)
        VALUES ($1,$2,'terminal_delivery_failed',$3::jsonb,'open')`, id, serial, string(detail)); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// StrictApplianceAssignmentHandler serves GET /v1/appliance/assignment under the full mTLS trust rules; it is the
// only delivery channel for assignment documents (no JWT fallback, no :443 mount).
func (b *AssignmentBase) StrictApplianceAssignmentHandler(w http.ResponseWriter, r *http.Request) {
	// Identity comes only from the verified client certificate; any Authorization header is ignored.
	// strictMTLSSelf enforces the exact fingerprint and serial match to the active Central record.
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
	pubB64 := ""
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(public_key,'') FROM appliances WHERE id=$1`, appID).Scan(&pubB64)
	if doc.ApplianceID != appID ||
		(doc.IdentityKeyFpr != "" && doc.IdentityKeyFpr != identityFprFromPubB64(pubB64)) {
		logFetch(ctx, b.DB, appID, "", "denied:not-self")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Only a currently trusted (active or verify_only) signer, never an unknown or revoked one.
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

// strictMTLSSelf requires mTLS, a URI-SAN equal to self, and an exact fingerprint and serial match to an unrevoked
// certificate issued to this appliance.
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
	// A failed lookup is Central's outage (503), not "certificate not active" (audit Phase 1 L7).
	if err := db.QueryRow(r.Context(), `
        SELECT count(*) FROM appliance_certificates
         WHERE appliance_id=$1 AND fingerprint_sha256=$2 AND cert_serial=$3 AND status='active'`,
		applianceID, fpr, certSerial).Scan(&n); err != nil {
		failDB(w, r, "Central could not check the certificate. Try again later.", err)
		return false
	}
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

// logFetch records a fetch. Served and denied fetches are one row each; "not-modified" fetches (one per 30-second
// poll) share one row per appliance, outcome and hour, counted in hits (AD-54).
func logFetch(ctx context.Context, db *pgxpool.Pool, applianceID, fpr, outcome string) {
	if strings.HasPrefix(outcome, "not-modified") && applianceID != "" {
		tag, err := db.Exec(ctx, `UPDATE appliance_assignment_fetch_log SET hits = hits + 1
            WHERE id = (SELECT id FROM appliance_assignment_fetch_log
                         WHERE appliance_id = $1::uuid AND outcome = $2 AND at >= date_trunc('hour', now())
                         ORDER BY at DESC LIMIT 1)`, applianceID, outcome)
		if err == nil && tag.RowsAffected() > 0 {
			return
		}
	}
	_, _ = db.Exec(ctx, `INSERT INTO appliance_assignment_fetch_log (appliance_id, fingerprint, outcome)
        VALUES (NULLIF($1,'')::uuid,NULLIF($2,''),$3)`, applianceID, fpr, outcome)
}
