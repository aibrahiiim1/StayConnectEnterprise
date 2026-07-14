package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

type activateReq struct {
	TenantID string `json:"tenant_id"`
	SiteID   string `json:"site_id"`
	// Simple license model: the activation form carries the license terms
	// directly — no plan or subscription selection.
	MaxConcurrentOnlineGuests int        `json:"max_concurrent_online_guests"`
	ValidFrom                 *time.Time `json:"valid_from"`
	ValidUntil                *time.Time `json:"valid_until"`
	ValidDays                 int        `json:"valid_days"` // fallback when valid_until absent
	GracePeriodDays           int        `json:"grace_period_days"`
	OfflineGraceDays          int        `json:"offline_grace_days"` // legacy alias
	Override                  bool       `json:"override_allocation"`
}

// activateAppliance is the ONE-CLICK activation. In a single operator action
// (one permission + one step-up) it runs the full lifecycle server-side:
//
//	claim -> assign -> signed assignment -> (wait for CSR) certificate -> hardware-bound license
//
// so the appliance can then converge on its own (fetch cert -> API mTLS ->
// fetch signed assignment -> NATS mTLS -> fetch license -> Active). It never
// leaves a Central-only assignment, a cert without assignment, or a partial
// license: assignment-sign failure rolls the assign back; if a CSR has not
// arrived yet the appliance is marked activated and SubmitCSR auto-issues the
// certificate the moment the CSR appears (see certificates.go).
func (b *Base) activateAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in activateReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.TenantID == "" || in.SiteID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant_id and site_id required")
		return
	}
	if in.ValidUntil == nil && in.ValidDays <= 0 {
		in.ValidDays = 365
	}
	if in.GracePeriodDays <= 0 {
		in.GracePeriodDays = in.OfflineGraceDays
	}
	if in.GracePeriodDays <= 0 {
		in.GracePeriodDays = 30
	}
	if b.Lic == nil {
		Fail(w, r, http.StatusServiceUnavailable, CodeInternal, "licensing not configured")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sess := auth.FromContext(r.Context())
	operatorID, actor := "", "operator"
	if sess != nil {
		operatorID = sess.OperatorID
		actor = emailOf(sess)
	}
	ip := clientIPFromReq(r)

	// Preconditions: site ∈ tenant, allocation.
	var ok int
	b.DB.QueryRow(ctx, `SELECT count(*) FROM sites WHERE id=$1 AND tenant_id=$2`, in.SiteID, in.TenantID).Scan(&ok)
	if ok == 0 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not in tenant")
		return
	}
	if !in.Override {
		if limit, _ := GetIntLimit(ctx, b.DB, in.TenantID, "max_appliances"); limit > 0 {
			var cnt int64
			b.DB.QueryRow(ctx, `SELECT count(*) FROM appliances WHERE tenant_id=$1 AND lifecycle_state IN ('assigned','licensed','online','offline','activated')`, in.TenantID).Scan(&cnt)
			if cnt >= limit {
				Fail(w, r, http.StatusConflict, "allocation_exceeded", "appliance allocation exceeded; upgrade or override")
				return
			}
		}
	}

	var prevT, prevS, prevState string
	if err := b.DB.QueryRow(ctx, `SELECT COALESCE(tenant_id::text,''),COALESCE(site_id::text,''),lifecycle_state FROM appliances WHERE id=$1`, id).Scan(&prevT, &prevS, &prevState); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}

	// (1) Claim.
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state='claimed', status='enrolled', updated_at=now()
	    WHERE id=$1 AND lifecycle_state IN ('pending_approval','installed_unenrolled','claimed')`, id)
	recordLifecycle(ctx, b.DB, id, prevState, "claimed", actor, ip, "one-click activate")

	// (2) Assign + (3) signed assignment (rolled back together on sign failure).
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET tenant_id=$2, site_id=$3, lifecycle_state='assigned', status='enrolled', updated_at=now() WHERE id=$1`, id, in.TenantID, in.SiteID)
	_, _ = b.DB.Exec(ctx, `
        INSERT INTO appliance_assignments (appliance_id, tenant_id, site_id, prev_tenant_id, prev_site_id, operator_id, source_ip, reason)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8)`,
		id, in.TenantID, in.SiteID, prevT, prevS, operatorID, ip, "one-click activate")
	recordLifecycle(ctx, b.DB, id, "claimed", "assigned", actor, ip, "one-click activate")
	if err := b.issueAssignment(ctx, id, "assigned"); err != nil {
		_, _ = b.DB.Exec(ctx, `UPDATE appliances SET tenant_id=NULLIF($2,'')::uuid, site_id=NULLIF($3,'')::uuid, lifecycle_state=$4, updated_at=now() WHERE id=$1`, id, prevT, prevS, prevState)
		audit.Op(ctx, b.DB, r, "appliance.activate_failed", "appliance", id, map[string]any{"step": "assignment", "error": err.Error()})
		Fail(w, r, http.StatusServiceUnavailable, "assignment_unsignable", "assignment could not be signed; activate rolled back: "+err.Error())
		return
	}

	// (4) Certificate: wait briefly for the appliance's CSR, then issue. If none
	// arrives in time, the appliance is still marked activated and the CSR will
	// be auto-issued on arrival (no manual step, no partial state).
	certIssued := false
	if b.CA != nil {
		cb := &CertBase{Base: b, CA: b.CA, ClientValid: b.ClientValid}
		for i := 0; i < 12; i++ {
			var n int
			b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='pending'`, id).Scan(&n)
			if n > 0 {
				if _, err := cb.issueCertForAppliance(ctx, r, id, actor); err == nil {
					certIssued = true
				}
				break
			}
			var active int
			b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1 AND status='active'`, id).Scan(&active)
			if active > 0 {
				certIssued = true
				break
			}
			time.Sleep(2 * time.Second)
		}
	}

	// (5) Hardware-bound license with the operator's explicit terms.
	lp := licensing.IssueParams{
		TenantID: in.TenantID, SiteID: in.SiteID, ApplianceID: id, CreatedBy: operatorID,
		MaxConcurrentOnlineGuests: in.MaxConcurrentOnlineGuests,
		GracePeriodDays:           in.GracePeriodDays,
		ValidFor:                  time.Duration(in.ValidDays) * 24 * time.Hour,
	}
	if in.ValidFrom != nil {
		lp.ValidFrom = *in.ValidFrom
	}
	if in.ValidUntil != nil {
		lp.ValidUntil = *in.ValidUntil
	}
	doc, _, err := b.Lic.Issue(ctx, lp)
	if err != nil {
		audit.Op(ctx, b.DB, r, "appliance.activate_failed", "appliance", id, map[string]any{"step": "license", "error": err.Error()})
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "license issue failed: "+err.Error())
		return
	}

	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state='activated', updated_at=now() WHERE id=$1`, id)
	recordLifecycle(ctx, b.DB, id, "assigned", "activated", actor, ip, "one-click activate")
	audit.Op(ctx, b.DB, r, "appliance.activated", "appliance", id, map[string]any{
		"_tenant_id": in.TenantID, "site_id": in.SiteID, "license_id": doc.LicenseID, "cert_issued": certIssued})

	// If this activation completes a replacement at the site, terminate the
	// outgoing appliance's authority now (bounded overlap ends the moment the
	// replacement is Active).
	b.completeReplacementIfPending(ctx, r, id, in.SiteID)

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":      "activated",
		"license_id":  doc.LicenseID,
		"cert_issued": certIssued,
		"note":        "assignment + license issued; appliance is converging (cert/mTLS/assignment adoption complete automatically)",
	})
}
