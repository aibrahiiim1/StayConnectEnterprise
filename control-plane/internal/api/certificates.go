package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	pki "github.com/stayconnect/enterprise/control-plane/internal/pki"
)

// CertBase serves the appliance CSR/cert-fetch endpoints and the platform certificate lifecycle.
// The CA private key lives only inside b.CA and is never exposed.
type CertBase struct {
	*Base
	CA          *pki.CA
	ClientValid time.Duration // issued client-cert lifetime
}

// currentPendingCSR selects (as rq, joined to appliances a) the pending certificate requests filed under the appliance's
// CURRENT identity. A CSR is submitted on a request signed by the identity key, and registered_at is when Central adopted
// that key: the first registration, or replaceWaitingIdentity after a reinstall before activation. A request filed earlier
// carries the mTLS key of an installation that no longer exists; signing it issues a certificate nobody can use, and while
// it reads as "pending" the new installation never files its own (finding F1). Such a request is never signed or reported.
const currentPendingCSR = `rq.status='pending' AND rq.requested_at >= COALESCE(a.registered_at, '-infinity'::timestamptz)`

// countCurrentPendingCSRs counts the requests Activate or Reissue could sign, and that FetchCertificate reports as pending.
func (b *Base) countCurrentPendingCSRs(ctx context.Context, appID string) int {
	var n int
	_ = b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_requests rq JOIN appliances a ON a.id=rq.appliance_id
         WHERE rq.appliance_id=$1 AND `+currentPendingCSR, appID).Scan(&n)
	return n
}

// SubmitCSR stores the appliance's CSR (its key stays local). An appliance already holding an active
// certificate in a good lifecycle state is auto-signed (rotation); otherwise the CSR is queued for first issuance.
func (b *CertBase) SubmitCSR(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	var in struct {
		CSRPem string `json:"csr_pem"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.CSRPem == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "csr_pem required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	// One pending request per appliance: a new one closes the older ones (only the newest is ever signed), so
	// repeated submissions cannot pile up rows (audit Phase 1 L8).
	var reqID string
	err := pgx.BeginFunc(ctx, b.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE appliance_certificate_requests SET status='rejected', decided_at=now()
            WHERE appliance_id=$1 AND status='pending'`, ident.ApplianceID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
            INSERT INTO appliance_certificate_requests (appliance_id, csr_pem, status, source_ip)
            VALUES ($1,$2,'pending',$3) RETURNING id`,
			ident.ApplianceID, in.CSRPem, clientIP(r)).Scan(&reqID)
	})
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "csr store failed")
		return
	}
	b.certEvent(ctx, ident.ApplianceID, "", "csr_submitted", "appliance", clientIP(r))

	// Rotation needs no human: the existing valid certificate is the trust proof.
	var activeCount int
	var lifecycle string
	var retiring bool
	b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1 AND status='active'`, ident.ApplianceID).Scan(&activeCount)
	b.DB.QueryRow(ctx, `SELECT COALESCE(lifecycle_state,''),
            EXISTS (SELECT 1 FROM appliance_terminal_delivery t WHERE t.appliance_id=a.id)
          FROM appliances a WHERE a.id=$1`, ident.ApplianceID).Scan(&lifecycle, &retiring)
	// A retiring appliance gets no new certificate on its own (audit Phase 1 L8).
	rotatable := activeCount > 0 && lifecycle == "assigned" && !retiring
	// Activation already authorized the first certificate, so it is issued on arrival; a waiting appliance's CSR
	// is queued and signed by the activation itself.
	firstAfterActivate := activeCount == 0 && lifecycle == "assigned"
	if rotatable || (firstAfterActivate && !retiring) {
		actor := "appliance-rotation"
		if firstAfterActivate {
			actor = "auto-activate"
		}
		res, err := b.issueCertForAppliance(ctx, r, ident.ApplianceID, actor, false)
		if err == nil {
			WriteJSON(w, http.StatusCreated, map[string]any{
				"status": "rotated", "fingerprint_sha256": res.FingerprintHex,
				"not_after": res.NotAfter, "ca_version": b.CA.Version,
			})
			return
		}
	}
	WriteJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "request_id": reqID})
}

// FetchCertificate returns the current client cert and CA chain, or pending/none.
func (b *CertBase) FetchCertificate(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var certPEM, fpr string
	var notAfter time.Time
	err := b.DB.QueryRow(ctx, `
        SELECT cert_pem, fingerprint_sha256, not_after FROM appliance_certificates
         WHERE appliance_id=$1 AND status='active' ORDER BY created_at DESC LIMIT 1`,
		ident.ApplianceID).Scan(&certPEM, &fpr, &notAfter)
	if err == pgx.ErrNoRows {
		status := "none"
		if b.countCurrentPendingCSRs(ctx, ident.ApplianceID) > 0 {
			status = "pending"
		}
		WriteJSON(w, http.StatusOK, map[string]any{"status": status, "ca_chain": string(b.CA.CertPEM())})
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "cert lookup failed")
		return
	}
	b.certEvent(ctx, ident.ApplianceID, "", "delivered", "appliance", clientIP(r))
	WriteJSON(w, http.StatusOK, map[string]any{
		"status": "issued", "certificate_pem": certPEM, "ca_chain": string(b.CA.CertPEM()),
		"fingerprint_sha256": fpr, "not_after": notAfter,
	})
}

// issueCertForAppliance signs the latest pending CSR of the current identity, supersedes any prior active cert, and records
// the issuance. Every other pending request is closed as rejected: none of them may be signed later.
//
// The appliance row is locked for the whole issuance, so two concurrent issuances cannot both leave an active
// certificate (audit Phase 1 L8). revokeOthers (an operator's reissue) revokes every earlier certificate at once
// instead of superseding it with the 24 h overlap that automatic rotation keeps (audit Phase 1 M2).
func (b *CertBase) issueCertForAppliance(ctx context.Context, r *http.Request, appID, actor string, revokeOthers bool) (*pki.SignedClient, error) {
	operatorID := ""
	if s := auth.FromContext(r.Context()); s != nil {
		operatorID = s.OperatorID
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM appliances WHERE id=$1 FOR UPDATE`, appID); err != nil {
		return nil, err
	}
	var reqID, csrPEM, tenantID, siteID, serial string
	if err := tx.QueryRow(ctx, `
        SELECT rq.id::text, rq.csr_pem, COALESCE(a.tenant_id::text,''), COALESCE(a.site_id::text,''), a.serial
          FROM appliance_certificate_requests rq JOIN appliances a ON a.id=rq.appliance_id
         WHERE rq.appliance_id=$1 AND `+currentPendingCSR+`
         ORDER BY rq.requested_at DESC LIMIT 1`, appID).Scan(&reqID, &csrPEM, &tenantID, &siteID, &serial); err != nil {
		return nil, err
	}
	sc, err := b.CA.SignApplianceCSR([]byte(csrPEM), appID, tenantID, siteID, b.ClientValid)
	if err != nil {
		return nil, err
	}
	if revokeOthers {
		reason := "operator reissue"
		if _, err := tx.Exec(ctx, `
            INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason, revoked_by)
            SELECT c.id, c.appliance_id, c.fingerprint_sha256, $2, NULLIF($3,'')::uuid
              FROM appliance_certificates c WHERE c.appliance_id=$1 AND c.status IN ('active','superseded')
            ON CONFLICT DO NOTHING`, appID, reason, operatorID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(), revocation_reason=$2
            WHERE appliance_id=$1 AND status IN ('active','superseded')`, appID, reason); err != nil {
			return nil, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='superseded', superseded_at=now()
        WHERE appliance_id=$1 AND status='active'`, appID); err != nil {
		return nil, err
	}
	var certID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO appliance_certificates
          (appliance_id, tenant_id, site_id, serial, cert_serial, fingerprint_sha256,
           ca_version, not_before, not_after, status, cert_pem, issued_by)
        VALUES ($1,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,'active',$10,NULLIF($11,'')::uuid)
        RETURNING id`,
		appID, tenantID, siteID, serial, sc.SerialHex, sc.FingerprintHex, b.CA.Version,
		sc.NotBefore, sc.NotAfter, string(sc.CertPEM), operatorID).Scan(&certID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificate_requests SET status='signed', decided_at=now(), decided_by=NULLIF($2,'')::uuid WHERE id=$1`, reqID, operatorID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificate_requests SET status='rejected', decided_at=now(), decided_by=NULLIF($2,'')::uuid
        WHERE appliance_id=$1 AND status='pending'`, appID, operatorID); err != nil {
		return nil, err
	}
	_, _ = tx.Exec(ctx, `UPDATE appliances SET current_cert_fingerprint=$2, cert_not_after=$3, updated_at=now() WHERE id=$1`, appID, sc.FingerprintHex, sc.NotAfter)
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	b.certEvent(ctx, appID, certID, "issued", actor, clientIP(r))
	audit.Op(ctx, b.DB, r, "certificate.issued", "certificate", certID, map[string]any{
		"_tenant_id": tenantID, "appliance_id": appID, "fingerprint": sc.FingerprintHex, "not_after": sc.NotAfter, "actor": actor,
	})
	return sc, nil
}

// SupersededOverlap is how long a rotated-out certificate stays usable on the mTLS listener, long enough to install
// its replacement. The assignment channel never accepts a superseded certificate (strictMTLSSelf).
const SupersededOverlap = 24 * time.Hour

// RevokeSupersededCertificates records as revoked every certificate superseded more than SupersededOverlap ago (or
// at an unknown time) where the mTLS listener checks. Idempotent; run by the housekeeping loop.
func RevokeSupersededCertificates(ctx context.Context, b *Base) (int64, error) {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
        INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason)
        SELECT c.id, c.appliance_id, c.fingerprint_sha256, 'superseded by rotation'
          FROM appliance_certificates c
         WHERE c.status='superseded' AND (c.superseded_at IS NULL OR c.superseded_at < now() - make_interval(secs => $1))
        ON CONFLICT DO NOTHING`, SupersededOverlap.Seconds())
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(),
            revocation_reason='superseded by rotation'
         WHERE status='superseded' AND (superseded_at IS NULL OR superseded_at < now() - make_interval(secs => $1))`,
		SupersededOverlap.Seconds()); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

func (b *CertBase) certEvent(ctx context.Context, appID, certID, event, actor, ip string) {
	var appArg, certArg any
	if appID != "" {
		appArg = appID
	}
	if certID != "" {
		certArg = certID
	}
	_, _ = b.DB.Exec(ctx, `INSERT INTO appliance_certificate_events (appliance_id, certificate_id, event, actor, source_ip) VALUES ($1,$2,$3,$4,$5)`,
		appArg, certArg, event, actor, ip)
}
