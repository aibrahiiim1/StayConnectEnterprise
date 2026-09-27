package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

// APPLIANCE LIFECYCLE ACTIONS (docs/CENTRAL_CONTROL_PLANE.md §4). All are platform-only and step-up gated at
// the router. Each checks the state it starts from; none trusts the console to have checked.

func (b *Base) writeAppliance(w http.ResponseWriter, r *http.Request, code int, id string) {
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	WriteJSON(w, code, row)
}

func requireReason(w http.ResponseWriter, r *http.Request, reason string) bool {
	if strings.TrimSpace(reason) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return false
	}
	return true
}

func invalidState(w http.ResponseWriter, r *http.Request, msg string, row *ApplianceRow) {
	Fail(w, r, http.StatusConflict, "invalid_state", msg, map[string]any{"activation": row.Activation})
}

// ---------------------------------------------------------------------------------------------------------
// Activate
// ---------------------------------------------------------------------------------------------------------

type activateReq struct {
	CustomerID  string `json:"customer_id"`
	NewCustomer *struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"new_customer"`
	SiteID  string `json:"site_id"`
	NewSite *struct {
		Name     string `json:"name"`
		Code     string `json:"code"`
		Timezone string `json:"timezone"`
		Country  string `json:"country"`
	} `json:"new_site"`
	License licenseTerms `json:"license"`
	Reason  string       `json:"reason"`
}

// activate: POST /cloud/v1/appliances/{id}/activate — WAITING appliances only.
//
// One transaction: create the customer and/or site when asked, bind the appliance to them, sign the
// assignment and issue the licence. If any step fails nothing is kept — there is never an assignment
// without a licence, a licence without an assignment, or a half-created customer. The certificate does
// not block: a CSR that is already waiting is signed right after, and one that arrives later is signed on
// arrival (certificates.go).
func (b *Base) activate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in activateReq
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body: "+err.Error())
		return
	}
	if (in.CustomerID == "") == (in.NewCustomer == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "give exactly one of customer_id or new_customer")
		return
	}
	if (in.SiteID == "") == (in.NewSite == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "give exactly one of site_id or new_site")
		return
	}
	if in.NewCustomer != nil && in.SiteID != "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "a new customer has no sites yet: use new_site")
		return
	}
	if b.Lic == nil || b.AssignKey == nil {
		Fail(w, r, http.StatusServiceUnavailable, "activation_unavailable",
			"activation needs the vendor signing key and the assignment signing key; one is not configured")
		return
	}
	operatorID, email := actorOf(r)
	lp, err := in.License.params(id, operatorID)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.Activation != ActivationWaiting {
		invalidState(w, r, "only an appliance that is waiting for activation can be activated", row)
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)

	// Lock the row and re-check the state inside the transaction: two operators clicking Activate at once
	// get one activation and one 409, never two assignments.
	var lifecycle string
	if err := tx.QueryRow(ctx, `SELECT lifecycle_state FROM appliances WHERE id=$1 FOR UPDATE`, id).Scan(&lifecycle); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if lifecycle != "pending_approval" {
		Fail(w, r, http.StatusConflict, "invalid_state", "the appliance is no longer waiting for activation")
		return
	}

	customerID, siteID := in.CustomerID, in.SiteID
	if in.NewCustomer != nil {
		if customerID, _, err = createCustomer(ctx, tx, in.NewCustomer.Name, in.NewCustomer.Slug); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
	} else {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM tenants WHERE id::text=$1`, customerID).Scan(&status); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "customer not found")
			return
		}
		if status == "archived" {
			Fail(w, r, http.StatusConflict, CodeConflict, "the customer is archived; restore it first")
			return
		}
	}
	if in.NewSite != nil {
		if siteID, err = createSite(ctx, tx, customerID, in.NewSite.Name, in.NewSite.Code, in.NewSite.Timezone, in.NewSite.Country); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
	} else {
		var siteStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM sites WHERE id::text=$1 AND tenant_id::text=$2`, siteID, customerID).Scan(&siteStatus); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not found for this customer")
			return
		}
		if siteStatus == "archived" {
			Fail(w, r, http.StatusConflict, CodeConflict, "the site is archived; restore it first")
			return
		}
	}

	if _, err := tx.Exec(ctx, `
        UPDATE appliances SET tenant_id=$2, site_id=$3, lifecycle_state='assigned', activated_at=now(), updated_at=now()
         WHERE id=$1`, id, customerID, siteID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "assign failed: "+err.Error())
		return
	}
	// A previous life's retirement record must not make the new activation look like a retirement.
	if _, err := tx.Exec(ctx, `DELETE FROM appliance_terminal_delivery WHERE appliance_id=$1`, id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "assign failed")
		return
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_assignments (appliance_id, tenant_id, site_id, operator_id, source_ip, reason)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6)`,
		id, customerID, siteID, operatorID, clientIPFromReq(r), strings.TrimSpace("activate "+in.Reason)); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "assign failed")
		return
	}
	recordLifecycle(ctx, tx, id, lifecycle, "assigned", email, clientIPFromReq(r), "activate "+in.Reason)

	ab := &AssignmentBase{Base: b, SignKey: b.AssignKey}
	asg, err := ab.IssueTx(ctx, tx, id, assignment.StateAssigned)
	if err != nil {
		Fail(w, r, http.StatusServiceUnavailable, "assignment_unsignable",
			"the assignment could not be signed, so nothing was activated: "+err.Error())
		return
	}
	lp.TenantID, lp.SiteID = customerID, siteID
	doc, _, err := b.Lic.IssueTx(ctx, tx, lp)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the licence could not be issued, so nothing was activated: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}

	// Committed. The rest is best effort and never undoes the activation.
	certIssued := false
	if b.CA != nil {
		var pending int
		_ = b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='pending'`, id).Scan(&pending)
		if pending > 0 {
			cb := &CertBase{Base: b, CA: b.CA, ClientValid: b.ClientValid}
			_, cerr := cb.issueCertForAppliance(ctx, r, id, email)
			certIssued = cerr == nil
		}
	}
	audit.Op(ctx, b.DB, r, "appliance.activated", "appliance", id, map[string]any{
		"_tenant_id": customerID, "site_id": siteID, "license_id": doc.LicenseID,
		"license_version": doc.LicenseVersion, "assignment_version": asg.Version, "cert_issued": certIssued,
		"new_customer": in.NewCustomer != nil, "new_site": in.NewSite != nil, "reason": in.Reason})
	b.completeReplacementIfPending(ctx, r, id, siteID)
	b.writeAppliance(w, r, http.StatusOK, id)
}

// ---------------------------------------------------------------------------------------------------------
// Move
// ---------------------------------------------------------------------------------------------------------

// move: POST /cloud/v1/appliances/{id}/move {customer_id, site_id, reason}
//
// Re-signs the assignment for the new site. Within the same customer the licence is re-issued, terms
// unchanged, bound to the new site. To another customer the licence is revoked: the appliance purges the
// previous customer's local data, and the new customer's licence is a new decision (Set licence).
func (b *Base) move(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		CustomerID string `json:"customer_id"`
		SiteID     string `json:"site_id"`
		Reason     string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.CustomerID == "" || in.SiteID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "customer_id and site_id are required")
		return
	}
	if !requireReason(w, r, in.Reason) {
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		invalidState(w, r, "only an activated appliance can be moved", row)
		return
	}
	if deref(row.SiteID) == in.SiteID {
		Fail(w, r, http.StatusConflict, CodeConflict, "the appliance is already at that site")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var siteStatus string
	if err := b.DB.QueryRow(ctx, `SELECT status FROM sites WHERE id::text=$1 AND tenant_id::text=$2`, in.SiteID, in.CustomerID).Scan(&siteStatus); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not found for this customer")
		return
	}
	if siteStatus == "archived" {
		Fail(w, r, http.StatusConflict, CodeConflict, "the site is archived; restore it first")
		return
	}
	operatorID, email := actorOf(r)
	prevCustomer, prevSite := deref(row.CustomerID), deref(row.SiteID)
	crossCustomer := prevCustomer != in.CustomerID

	var curLicense licensing.IssueParams
	var hadLicense bool
	if b.Lic != nil {
		if lid, err := b.Lic.CurrentLicenseID(ctx, id); err == nil {
			if curLicense, err = b.Lic.CurrentTerms(ctx, lid); err == nil {
				hadLicense = true
			}
		}
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE appliances SET tenant_id=$2, site_id=$3, updated_at=now() WHERE id=$1`,
		id, in.CustomerID, in.SiteID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "move failed: "+err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_assignments (appliance_id, tenant_id, site_id, prev_tenant_id, prev_site_id, operator_id, source_ip, reason)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8)`,
		id, in.CustomerID, in.SiteID, prevCustomer, prevSite, operatorID, clientIPFromReq(r), in.Reason); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "move failed")
		return
	}
	ab := &AssignmentBase{Base: b, SignKey: b.AssignKey}
	asg, err := ab.IssueTx(ctx, tx, id, assignment.StateAssigned)
	if err != nil {
		Fail(w, r, http.StatusServiceUnavailable, "assignment_unsignable", "the move could not be signed, so nothing changed: "+err.Error())
		return
	}
	// A licence already past valid_until cannot be re-signed for the new site (a licence is never issued
	// already expired); it is revoked like a cross-customer move and the operator sets new terms.
	revokeLicense := hadLicense && (crossCustomer || !curLicense.ValidUntil.After(time.Now()))
	var newLicenseID string
	if hadLicense && !revokeLicense {
		p := licensing.PreservedTerms(curLicense, operatorID)
		p.TenantID, p.SiteID = in.CustomerID, in.SiteID
		doc, _, err := b.Lic.IssueTx(ctx, tx, p)
		if err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the licence could not be re-issued for the new site, so nothing changed: "+err.Error())
			return
		}
		newLicenseID = doc.LicenseID
	}
	if revokeLicense {
		if _, err := tx.Exec(ctx, `UPDATE licenses SET status='revoked', revoked_at=now()
            WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')`, id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "move failed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	recordLifecycle(ctx, b.DB, id, "assigned", "assigned", email, clientIPFromReq(r), "moved: "+in.Reason)
	audit.Op(ctx, b.DB, r, "appliance.moved", "appliance", id, map[string]any{
		"_tenant_id": in.CustomerID, "site_id": in.SiteID, "prev_customer_id": prevCustomer, "prev_site_id": prevSite,
		"cross_customer": crossCustomer, "license_revoked": revokeLicense,
		"new_license_id": newLicenseID, "assignment_version": asg.Version, "reason": in.Reason})
	b.writeAppliance(w, r, http.StatusOK, id)
}

// ---------------------------------------------------------------------------------------------------------
// Retire
// ---------------------------------------------------------------------------------------------------------

// retire: POST /cloud/v1/appliances/{id}/retire {reason, emergency?, confirm_serial?}
//
// Normal: two-phase terminal delivery — a signed decommissioned assignment, the appliance acknowledges, then
// its credentials are revoked. Emergency (a compromised or lost box): credentials are revoked now and the
// identity is marked revoked; nothing waits for the box. The licence is revoked at once either way.
func (b *Base) retire(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason        string `json:"reason"`
		Emergency     bool   `json:"emergency"`
		ConfirmSerial string `json:"confirm_serial"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if !requireReason(w, r, in.Reason) {
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" {
		invalidState(w, r, "only an activated appliance can be retired (a waiting one can simply be deleted)", row)
		return
	}
	if row.Activation == ActivationRetiring && !in.Emergency {
		invalidState(w, r, "retirement is already in progress; retire again as an emergency to stop waiting for the appliance", row)
		return
	}
	if in.Emergency && in.ConfirmSerial != row.Serial {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "an emergency retirement needs confirm_serial set to the appliance's serial",
			map[string]any{"expected": row.Serial})
		return
	}
	terminal := assignment.StateDecommissioned
	if in.Emergency {
		terminal = assignment.StateRevoked
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	ver, err := b.beginTerminalDelivery(ctx, r, id, terminal, in.Reason, in.Emergency)
	if err != nil {
		Fail(w, r, http.StatusServiceUnavailable, "retire_failed", "retirement could not start: "+err.Error())
		return
	}
	revoked, _ := b.revokeApplianceBoundLicenses(ctx, id)
	_, email := actorOf(r)
	to := "retiring"
	if in.Emergency {
		to = lifecycleForTerminal(terminal)
	}
	recordLifecycle(ctx, b.DB, id, row.lifecycle, to, email, clientIPFromReq(r), in.Reason)
	action := "appliance.retire_started"
	if in.Emergency {
		action = "appliance.retired_emergency"
	}
	audit.Op(ctx, b.DB, r, action, "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "reason": in.Reason, "emergency": in.Emergency,
		"assignment_version": ver, "licenses_revoked": len(revoked)})
	b.writeAppliance(w, r, http.StatusOK, id)
}

// ---------------------------------------------------------------------------------------------------------
// Replace
// ---------------------------------------------------------------------------------------------------------

// replace: POST /cloud/v1/appliances/{id}/replace {reason}
//
// Marks the appliance for replacement. The new hardware registers itself; activating it at the SAME site
// retires this one. Until then this appliance keeps working, bounded by replacementWindow.
func (b *Base) replace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if !requireReason(w, r, in.Reason) {
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		invalidState(w, r, "only an activated appliance can be replaced", row)
		return
	}
	if row.replacementPending {
		Fail(w, r, http.StatusConflict, CodeConflict, "the appliance is already marked for replacement")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	if _, err := b.DB.Exec(ctx, `UPDATE appliances SET replacement_pending=true,
        replacement_deadline = now() + make_interval(hours => $2), updated_at=now() WHERE id=$1`,
		id, int(replacementWindow.Hours())); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	_, email := actorOf(r)
	recordLifecycle(ctx, b.DB, id, "assigned", "assigned", email, clientIPFromReq(r), "marked for replacement: "+in.Reason)
	audit.Op(ctx, b.DB, r, "appliance.replacement_started", "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "site_id": deref(row.SiteID), "reason": in.Reason,
		"window_hours": int(replacementWindow.Hours())})
	b.writeAppliance(w, r, http.StatusOK, id)
}

// ---------------------------------------------------------------------------------------------------------
// Rebind WAN MAC (F8)
// ---------------------------------------------------------------------------------------------------------

// rebindWANMAC: POST /cloud/v1/appliances/{id}/rebind-wan-mac {reason, new_mac?}
//
// After a genuine NIC replacement or VM move. The appliance reports its WAN MAC every time it registers, so
// by default the licence is re-bound to the MAC Central last heard; new_mac overrides that. The licence is
// re-signed with EXACTLY its current terms (cap, validity, grace, status) and the next version — it used to
// come back unlimited and valid for a fresh year.
func (b *Base) rebindWANMAC(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
		NewMAC string `json:"new_mac"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if !requireReason(w, r, in.Reason) {
		return
	}
	if b.Lic == nil {
		Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", "the vendor signing key is not configured")
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		invalidState(w, r, "only an activated appliance can be rebound", row)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var curMAC string
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(wan_mac,'') FROM appliances WHERE id=$1`, id).Scan(&curMAC)
	newMAC := normalizeMAC(in.NewMAC)
	if newMAC == "" {
		newMAC = normalizeMAC(curMAC)
	}
	if newMAC == "" {
		Fail(w, r, http.StatusConflict, CodeConflict, "Central has no WAN MAC for this appliance; pass new_mac")
		return
	}
	operatorID, _ := actorOf(r)
	lid, err := b.Lic.CurrentLicenseID(ctx, id)
	if err != nil {
		Fail(w, r, http.StatusConflict, "no_license", "the appliance has no current licence to rebind; set a licence instead")
		return
	}
	if _, err := b.DB.Exec(ctx, `UPDATE appliances SET wan_mac=$2, updated_at=now() WHERE id=$1`, id, newMAC); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	doc, err := b.Lic.Reissue(ctx, lid, operatorID)
	if err != nil {
		_, _ = b.DB.Exec(ctx, `UPDATE appliances SET wan_mac=NULLIF($2,''), updated_at=now() WHERE id=$1`, id, curMAC)
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the licence could not be re-issued, so nothing changed: "+err.Error())
		return
	}
	_, _ = b.DB.Exec(ctx, `UPDATE appliance_security_alerts SET status='resolved', resolved=true,
        acknowledged_by=NULLIF($2,'')::uuid, acknowledged_at=now()
         WHERE appliance_id=$1 AND kind IN ('wan_mac_mismatch','hardware_mismatch') AND NOT resolved`, id, operatorID)
	audit.Op(ctx, b.DB, r, "appliance.wan_mac_rebound", "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "old_mac": curMAC, "new_mac": newMAC, "reason": in.Reason,
		"previous_license_id": lid, "license_id": doc.LicenseID, "license_version": doc.LicenseVersion})
	b.writeAppliance(w, r, http.StatusOK, id)
}

func normalizeMAC(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", ":")
}

// ---------------------------------------------------------------------------------------------------------
// Reissue certificate
// ---------------------------------------------------------------------------------------------------------

// reissueCertificate: POST /cloud/v1/appliances/{id}/reissue-certificate {reason}
//
// Central never holds an appliance's private key, so it cannot mint a certificate on its own. If a CSR is
// waiting it is signed now (superseding the current certificate). Otherwise the current certificate is
// revoked, and the appliance's next CSR — which it sends when it finds its certificate refused — is signed on
// arrival because the appliance is activated.
func (b *Base) reissueCertificate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if !requireReason(w, r, in.Reason) {
		return
	}
	if b.CA == nil {
		Fail(w, r, http.StatusServiceUnavailable, "pki_unavailable", "the appliance CA is not configured")
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		invalidState(w, r, "only an activated appliance gets certificates", row)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	operatorID, email := actorOf(r)
	var pending int
	_ = b.DB.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='pending'`, id).Scan(&pending)
	outcome := "signed_pending_csr"
	if pending > 0 {
		cb := &CertBase{Base: b, CA: b.CA, ClientValid: b.ClientValid}
		if _, err := cb.issueCertForAppliance(ctx, r, id, email); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the waiting CSR could not be signed: "+err.Error())
			return
		}
	} else {
		outcome = "revoked_awaiting_csr"
		if err := b.revokeActiveCertificates(ctx, id, operatorID, "reissue: "+in.Reason); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "revoke failed")
			return
		}
	}
	audit.Op(ctx, b.DB, r, "appliance.certificate_reissued", "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "outcome": outcome, "reason": in.Reason})
	b.writeAppliance(w, r, http.StatusOK, id)
}

// revokeActiveCertificates revokes every active client certificate of the appliance and records each
// revocation where the mTLS listener checks it.
func (b *Base) revokeActiveCertificates(ctx context.Context, id, operatorID, reason string) error {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason, revoked_by)
        SELECT c.id, c.appliance_id, c.fingerprint_sha256, $2, NULLIF($3,'')::uuid
          FROM appliance_certificates c WHERE c.appliance_id=$1 AND c.status='active'
        ON CONFLICT DO NOTHING`, id, reason, operatorID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(), revocation_reason=$2
        WHERE appliance_id=$1 AND status='active'`, id, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE appliances SET current_cert_fingerprint=NULL, updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------------------------------------

// deleteAppliance: DELETE /cloud/v1/appliances/{id} {confirm_serial, reason} — waiting or retired only.
// The record goes; the audit history stays (audit_log has no foreign key).
func (b *Base) deleteAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ConfirmSerial string `json:"confirm_serial"`
		Reason        string `json:"reason"`
	}
	_ = DecodeJSON(r, &in)
	if !requireReason(w, r, in.Reason) {
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.Activation != ActivationWaiting && row.Activation != ActivationRetired {
		invalidState(w, r, "only a waiting or retired appliance can be deleted; retire it first", row)
		return
	}
	if in.ConfirmSerial != row.Serial {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "type the exact serial to confirm", map[string]any{"expected": row.Serial})
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	operatorID, _ := actorOf(r)
	// Defence in depth: nothing this appliance held may outlive it.
	_ = b.revokeActiveCertificates(ctx, id, operatorID, "appliance deleted")
	revoked, _ := b.revokeApplianceBoundLicenses(ctx, id)
	audit.Op(ctx, b.DB, r, "appliance.deleted", "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "serial": row.Serial, "activation": row.Activation,
		"reason": in.Reason, "licenses_revoked": len(revoked)})
	tag, err := b.DB.Exec(ctx, `DELETE FROM appliances WHERE id=$1 AND lifecycle_state IN ('pending_approval','revoked','decommissioned')`, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusConflict, "invalid_state", "the appliance changed state; reload and try again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------------------------------------
// Housekeeping
// ---------------------------------------------------------------------------------------------------------

// FetchLogRetention is how long assignment-fetch log rows are kept. The log records every 30-second poll of
// every appliance; it is a diagnostic trail, not history anybody decides anything from.
const FetchLogRetention = 30 * 24 * time.Hour

// PruneAssignmentFetchLog deletes assignment-fetch log rows older than FetchLogRetention. Nothing else.
func PruneAssignmentFetchLog(ctx context.Context, b *Base) (int64, error) {
	tag, err := b.DB.Exec(ctx, `DELETE FROM appliance_assignment_fetch_log WHERE at < now() - make_interval(secs => $1)`,
		FetchLogRetention.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
