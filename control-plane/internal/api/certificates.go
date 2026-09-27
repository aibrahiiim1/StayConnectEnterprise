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

// CertBase serves both the appliance-facing CSR/cert-fetch endpoints and the
// platform-facing certificate lifecycle (issue/revoke/list). The CA private
// key lives only inside b.CA (a file on Central); it is never exposed.
type CertBase struct {
	*Base
	CA          *pki.CA
	ClientValid time.Duration // issued client-cert lifetime
}

// ----- Appliance-facing (signed-auth) -----

// SubmitCSR stores a certificate-signing request from the appliance. The
// appliance generates its key locally and sends only the CSR (public). If the
// appliance ALREADY holds an active, non-revoked certificate and is in a good
// lifecycle state, the CSR is auto-signed immediately (autonomous ROTATION);
// otherwise it is queued pending a Platform operator's first issuance.
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
	var reqID string
	err := b.DB.QueryRow(ctx, `
        INSERT INTO appliance_certificate_requests (appliance_id, csr_pem, status, source_ip)
        VALUES ($1,$2,'pending',$3) RETURNING id`,
		ident.ApplianceID, in.CSRPem, clientIP(r)).Scan(&reqID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "csr store failed")
		return
	}
	b.certEvent(ctx, ident.ApplianceID, "", "csr_submitted", "appliance", clientIP(r))

	// Autonomous rotation: an already-certified, healthy appliance rotating its
	// key/cert does not need a human in the loop — its existing valid cert is
	// the trust proof. First issuance still requires Platform approval.
	var activeCount int
	var lifecycle string
	b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1 AND status='active'`, ident.ApplianceID).Scan(&activeCount)
	b.DB.QueryRow(ctx, `SELECT COALESCE(lifecycle_state,'') FROM appliances WHERE id=$1`, ident.ApplianceID).Scan(&lifecycle)
	rotatable := activeCount > 0 && lifecycle == "assigned"
	// Once an operator has activated the appliance, its FIRST certificate is auto-issued the moment the CSR
	// arrives — activation already authorized it, so there is no second button and the appliance converges on
	// its own. A waiting appliance's CSR is queued and signed by the activation itself.
	firstAfterActivate := activeCount == 0 && lifecycle == "assigned"
	if rotatable || firstAfterActivate {
		actor := "appliance-rotation"
		if firstAfterActivate {
			actor = "auto-activate"
		}
		res, err := b.issueCertForAppliance(ctx, r, ident.ApplianceID, actor)
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

// FetchCertificate returns the appliance's current active client cert + the CA
// chain once a Platform operator has issued it; otherwise reports pending/none.
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
		// Is there a pending CSR?
		var pend int
		b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='pending'`, ident.ApplianceID).Scan(&pend)
		status := "none"
		if pend > 0 {
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

// issueCertForAppliance signs the appliance's latest pending CSR, supersedes
// any prior active cert (rotation overlap is handled appliance-side), records
// the issuance, and drives current_cert_fingerprint. actor labels the event.
func (b *CertBase) issueCertForAppliance(ctx context.Context, r *http.Request, appID, actor string) (*pki.SignedClient, error) {
	var csrPEM, tenantID, siteID, serial string
	if err := b.DB.QueryRow(ctx, `
        SELECT rq.csr_pem, COALESCE(a.tenant_id::text,''), COALESCE(a.site_id::text,''), a.serial
          FROM appliance_certificate_requests rq JOIN appliances a ON a.id=rq.appliance_id
         WHERE rq.appliance_id=$1 AND rq.status='pending'
         ORDER BY rq.requested_at DESC LIMIT 1`, appID).Scan(&csrPEM, &tenantID, &siteID, &serial); err != nil {
		return nil, err
	}
	sc, err := b.CA.SignApplianceCSR([]byte(csrPEM), appID, tenantID, siteID, b.ClientValid)
	if err != nil {
		return nil, err
	}
	operatorID := ""
	if s := auth.FromContext(r.Context()); s != nil {
		operatorID = s.OperatorID
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `UPDATE appliance_certificates SET status='superseded' WHERE appliance_id=$1 AND status='active'`, appID)
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
	_, _ = tx.Exec(ctx, `UPDATE appliance_certificate_requests SET status='signed', decided_at=now(), decided_by=NULLIF($2,'')::uuid WHERE appliance_id=$1 AND status='pending'`, appID, operatorID)
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
