package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

// Lifecycle actions are platform-only and step-up gated at the router; each re-checks its starting state.

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
		SiteType string `json:"site_type"`
	} `json:"new_site"`
	License licenseTerms `json:"license"`
	Reason  string       `json:"reason"`
}

// activate: POST /cloud/v1/appliances/{id}/activate — WAITING appliances only.
//
// One transaction: create the customer/site, bind, sign the assignment and issue the licence, so there is never
// an assignment without a licence or a half-created customer. A waiting CSR is signed right after.
func (b *Base) activate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in activateReq
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
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

	// Lock and re-check the state: two concurrent activations yield one 409, never two assignments.
	var lifecycle, held string
	if err := tx.QueryRow(ctx, `SELECT lifecycle_state, COALESCE(held_customer_id::text,'') FROM appliances WHERE id=$1 FOR UPDATE`, id).
		Scan(&lifecycle, &held); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if lifecycle != "pending_approval" {
		Fail(w, r, http.StatusConflict, "invalid_state", "the appliance is no longer waiting for activation")
		return
	}
	// An appliance still holding a customer's data activates only for that customer (held_customer.go); checked before anything is created.
	target := in.CustomerID
	if in.NewCustomer != nil {
		target = ""
	}
	if ref := holdsOtherCustomerRefusal(held, target); ref != nil {
		Fail(w, r, ref.Status, ref.Code, ref.Message, map[string]any{"holds_customer_id": held})
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
		if siteID, err = createSite(ctx, tx, customerID, in.NewSite.Name, in.NewSite.Code, in.NewSite.Timezone, in.NewSite.Country, in.NewSite.SiteType); err != nil {
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
		failInternal(w, r, "assign failed", err)
		return
	}
	// A previous retirement record must not make the new activation look like a retirement.
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
			"the assignment could not be signed, so nothing was activated: "+publicError(r, err))
		return
	}
	lp.TenantID, lp.SiteID = customerID, siteID
	doc, _, err := b.Lic.IssueTx(ctx, tx, lp)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the licence could not be issued, so nothing was activated: "+publicError(r, err))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}

	// Committed. The rest is best effort and never undoes the activation.
	certIssued := false
	if b.CA != nil {
		if b.countCurrentPendingCSRs(ctx, id) > 0 {
			cb := &CertBase{Base: b, CA: b.CA, ClientValid: b.ClientValid}
			_, cerr := cb.issueCertForAppliance(ctx, r, id, email, false)
			certIssued = cerr == nil
		}
	}
	audit.Op(ctx, b.DB, r, "appliance.activated", "appliance", id, map[string]any{
		"_tenant_id": customerID, "site_id": siteID, "license_id": doc.LicenseID,
		"license_version": doc.LicenseVersion, "license_modules": doc.Modules.ModuleIDs(),
		"assignment_version": asg.Version, "site_type": asg.SiteType, "cert_issued": certIssued,
		"new_customer": in.NewCustomer != nil, "new_site": in.NewSite != nil, "reason": in.Reason})
	b.completeReplacementIfPending(ctx, r, id, siteID)
	b.writeAppliance(w, r, http.StatusOK, id)
}

// Operator-facing refusals of a move. The console shows `message` verbatim.
const (
	msgMoveCrossCustomer = "An appliance cannot move to another customer. Retire it, factory-reset it and " +
		"activate it for the new customer."
	msgMoveLicensingUnavailable = "Licensing is unavailable on Central right now, so the appliance was not moved; " +
		"its licence would not follow it. Try again later."
	msgMoveLicenceLookupFailed = "Central could not read the appliance's licence, so the appliance was not moved; " +
		"its licence would not follow it. Try again later."
	msgMoveLicenceExpired = "The appliance's licence is past its end date and cannot be re-issued for another " +
		"site, so the appliance was not moved. Renew the licence first, then move the appliance."
)

// moveRefusal is why a move is refused before anything is written; nil means go ahead.
type moveRefusal struct {
	Status  int
	Code    string
	Message string
}

// checkMoveCustomer refuses a move to another customer: local data cannot be guaranteed cleared in place, so
// changing customer is Retire -> factory-reset -> register -> Activate.
func checkMoveCustomer(currentCustomer, requestedCustomer string) *moveRefusal {
	if requestedCustomer != currentCustomer {
		return &moveRefusal{http.StatusConflict, "cross_customer_move", msgMoveCrossCustomer}
	}
	return nil
}

// decideMoveLicence decides what the licence does on a same-customer move. It fails closed: a move proceeds
// only when Central can prove what the licence is.
//
//	licensing unavailable (no vendor signer)      -> refuse (503): nothing could re-sign the licence
//	lookup failed for any reason but ErrNoLicense -> refuse (503): "no licence" was not verified
//	verified ErrNoLicense                          -> move, nothing to carry
//	current licence already past valid_until      -> refuse (409): it cannot be re-signed; renew first
//	otherwise                                      -> move, re-sign with the SAME terms for the new site
func decideMoveLicence(licensingAvailable bool, terms licensing.IssueParams, lookupErr error, now time.Time) (carry bool, refusal *moveRefusal) {
	if !licensingAvailable {
		return false, &moveRefusal{http.StatusServiceUnavailable, "licensing_unavailable", msgMoveLicensingUnavailable}
	}
	if errors.Is(lookupErr, licensing.ErrNoLicense) {
		return false, nil
	}
	if lookupErr != nil {
		return false, &moveRefusal{http.StatusServiceUnavailable, "licensing_unavailable", msgMoveLicenceLookupFailed}
	}
	if !terms.ValidUntil.After(now) {
		return false, &moveRefusal{http.StatusConflict, "license_expired", msgMoveLicenceExpired}
	}
	return true, nil
}

// move: POST /cloud/v1/appliances/{id}/move {customer_id, site_id, reason}
//
// Moves an activated appliance to another site of the same customer: assignment re-signed and licence re-issued
// with the same terms for the new site, in one transaction.
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
		invalidState(w, r, "Only an activated appliance can be moved.", row)
		return
	}
	prevCustomer, prevSite := deref(row.CustomerID), deref(row.SiteID)
	if ref := checkMoveCustomer(prevCustomer, in.CustomerID); ref != nil {
		Fail(w, r, ref.Status, ref.Code, ref.Message)
		return
	}
	if prevSite == in.SiteID {
		Fail(w, r, http.StatusConflict, CodeConflict, "The appliance is already at that site.")
		return
	}
	if b.Lic == nil || b.AssignKey == nil {
		// Without the vendor key the licence cannot follow; without the assignment key nothing can be signed.
		Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", msgMoveLicensingUnavailable)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var siteStatus string
	if err := b.DB.QueryRow(ctx, `SELECT status FROM sites WHERE id::text=$1 AND tenant_id::text=$2`, in.SiteID, in.CustomerID).Scan(&siteStatus); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "That site does not belong to the appliance's customer.")
		return
	}
	if siteStatus == "archived" {
		Fail(w, r, http.StatusConflict, CodeConflict, "The site is archived; restore it first.")
		return
	}
	operatorID, email := actorOf(r)

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)
	// Lock and re-check: a retirement, move or delete committed since the check above wins and this move is refused.
	var lockedLifecycle, lockedSite string
	var lockedRetiring bool
	if err := tx.QueryRow(ctx, `SELECT a.lifecycle_state, COALESCE(a.site_id::text,''),
                                       EXISTS (SELECT 1 FROM appliance_terminal_delivery td WHERE td.appliance_id=a.id)
                                  FROM appliances a WHERE a.id=$1 FOR UPDATE`, id).
		Scan(&lockedLifecycle, &lockedSite, &lockedRetiring); err != nil ||
		lockedLifecycle != "assigned" || lockedRetiring || lockedSite != prevSite {
		Fail(w, r, http.StatusConflict, "invalid_state", "The appliance changed state; reload and try again.")
		return
	}
	// Read under the per-appliance licence lock so a racing suspend or renew is not undone by re-signing stale terms.
	curLicense, lookupErr := b.Lic.CurrentTermsForApplianceTx(ctx, tx, id)
	carry, ref := decideMoveLicence(true, curLicense, lookupErr, time.Now())
	if ref != nil {
		Fail(w, r, ref.Status, ref.Code, ref.Message)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE appliances SET site_id=$2, updated_at=now() WHERE id=$1 AND tenant_id::text=$3`,
		id, in.SiteID, in.CustomerID); err != nil {
		failInternal(w, r, "move failed", err)
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
		Fail(w, r, http.StatusServiceUnavailable, "assignment_unsignable", "The move could not be signed, so nothing changed: "+publicError(r, err))
		return
	}
	var newLicenseID string
	var newLicenseVersion int64
	if carry {
		p := licensing.PreservedTerms(curLicense, operatorID)
		p.TenantID, p.SiteID = in.CustomerID, in.SiteID
		doc, _, err := b.Lic.IssueTx(ctx, tx, p)
		if err != nil {
			Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable",
				"The licence could not be re-issued for the new site, so the appliance was not moved: "+publicError(r, err))
			return
		}
		newLicenseID, newLicenseVersion = doc.LicenseID, doc.LicenseVersion
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	recordLifecycle(ctx, b.DB, id, "assigned", "assigned", email, clientIPFromReq(r), "moved: "+in.Reason)
	audit.Op(ctx, b.DB, r, "appliance.moved", "appliance", id, map[string]any{
		"_tenant_id": in.CustomerID, "site_id": in.SiteID, "prev_site_id": prevSite,
		"license_carried": carry, "new_license_id": newLicenseID, "new_license_version": newLicenseVersion,
		"assignment_version": asg.Version, "reason": in.Reason})
	b.writeAppliance(w, r, http.StatusOK, id)
}

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
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
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
	// One transaction: licence revocation, hostname release, terminal assignment and (emergency) credential shutdown
	// commit together, so there is never a revoked licence on an appliance that was not told to stand down.
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		failInternal(w, r, "Retirement could not start. Try again.", err)
		return
	}
	defer tx.Rollback(ctx)
	// Lock the row and re-check the state: a concurrent retire, move or delete gets one winner.
	var lifecycle string
	var delivery *string
	if err := tx.QueryRow(ctx, `SELECT a.lifecycle_state, td.delivery_state FROM appliances a
            LEFT JOIN appliance_terminal_delivery td ON td.appliance_id = a.id WHERE a.id=$1 FOR UPDATE OF a`, id).
		Scan(&lifecycle, &delivery); err != nil || lifecycle != "assigned" || (delivery != nil && !in.Emergency) {
		Fail(w, r, http.StatusConflict, "invalid_state", "The appliance changed state; reload and try again.")
		return
	}
	revoked, err := b.revokeApplianceBoundLicensesTx(ctx, tx, id)
	if err != nil {
		failInternal(w, r, "The licence could not be revoked, so retirement did not start. Try again.", err)
		return
	}
	ver, err := b.beginTerminalDelivery(ctx, tx, id, terminal, in.Reason, in.Emergency)
	if err != nil {
		Fail(w, r, http.StatusServiceUnavailable, "retire_failed", "Retirement could not start, so nothing changed: "+publicError(r, err))
		return
	}
	_, email := actorOf(r)
	to := "retiring"
	if in.Emergency {
		to = lifecycleForTerminal(terminal)
	}
	recordLifecycle(ctx, tx, id, row.lifecycle, to, email, clientIPFromReq(r), in.Reason)
	if err := tx.Commit(ctx); err != nil {
		failInternal(w, r, "Retirement could not start. Try again.", err)
		return
	}
	b.PortalTLS.Nudge()
	action := "appliance.retire_started"
	if in.Emergency {
		action = "appliance.retired_emergency"
		audit.Op(ctx, b.DB, r, "appliance.credentials_revoked", "appliance", id, map[string]any{"reason": "emergency_compromise"})
	}
	audit.Op(ctx, b.DB, r, action, "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "reason": in.Reason, "emergency": in.Emergency,
		"assignment_version": ver, "licenses_revoked": len(revoked)})
	b.writeAppliance(w, r, http.StatusOK, id)
}

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
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
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

// rebindWANMAC: POST /cloud/v1/appliances/{id}/rebind-wan-mac {reason, new_mac?}
//
// After a NIC replacement or VM move. The licence is re-bound to the MAC Central last heard (new_mac overrides)
// and re-signed with exactly its current terms and the next version.
func (b *Base) rebindWANMAC(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
		NewMAC string `json:"new_mac"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
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
	if errors.Is(err, licensing.ErrNoLicense) {
		Fail(w, r, http.StatusConflict, "no_license", "The appliance has no current licence to rebind; set a licence instead.")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", "Central could not read the appliance's licence, so nothing changed. Try again later.")
		return
	}
	if _, err := b.DB.Exec(ctx, `UPDATE appliances SET wan_mac=$2, updated_at=now() WHERE id=$1`, id, newMAC); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	doc, err := b.Lic.Reissue(ctx, lid, operatorID)
	if err != nil {
		_, _ = b.DB.Exec(ctx, `UPDATE appliances SET wan_mac=NULLIF($2,''), updated_at=now() WHERE id=$1`, id, curMAC)
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the licence could not be re-issued, so nothing changed: "+publicError(r, err))
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

// reissueCertificate: POST /cloud/v1/appliances/{id}/reissue-certificate {reason}
//
// Central never holds the appliance's private key. A waiting CSR is signed now; otherwise the current
// certificate is revoked and the appliance's next CSR is signed on arrival.
func (b *Base) reissueCertificate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
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
	outcome := "signed_pending_csr"
	if b.countCurrentPendingCSRs(ctx, id) > 0 {
		cb := &CertBase{Base: b, CA: b.CA, ClientValid: b.ClientValid}
		if _, err := cb.issueCertForAppliance(ctx, r, id, email, true); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the waiting CSR could not be signed: "+publicError(r, err))
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

// revokeActiveCertificates revokes every active client certificate and records it where the mTLS listener checks.
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

// deleteAppliance: DELETE /cloud/v1/appliances/{id} {confirm_serial, reason} — waiting or retired only.
// The record goes; the audit history stays (audit_log has no foreign key).
func (b *Base) deleteAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ConfirmSerial string `json:"confirm_serial"`
		Reason        string `json:"reason"`
	}
	if limitExceeded(w, r, DecodeJSON(r, &in)) {
		return
	}
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
	// Refused before anything is revoked if the portal hostname and certificates could not be released afterwards,
	// which would leave a half-retired appliance.
	if err := b.PortalTLS.CanRelease(ctx, id); err != nil {
		Fail(w, r, http.StatusConflict, "portal_tls_release_pending",
			"The appliance's portal hostname or certificate cannot be released yet ("+err.Error()+"), so it was not deleted.")
		return
	}
	// Fail closed: a record whose certificate or licence could not be revoked is not deleted.
	if err := b.revokeActiveCertificates(ctx, id, operatorID, "appliance deleted"); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The appliance's certificate could not be revoked, so it was not deleted. Try again.")
		return
	}
	revoked, err := b.revokeApplianceBoundLicenses(ctx, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The appliance's licence could not be revoked, so it was not deleted. Try again.")
		return
	}
	// Deleting the record cascades away the portal rows the worker needs to delete the A record and revoke
	// certificates, so that work happens first.
	if err := b.PortalTLS.FinalizeRelease(ctx, id); err != nil {
		Fail(w, r, http.StatusBadGateway, "portal_tls_release_failed",
			"The appliance's portal hostname or certificate could not be released ("+err.Error()+"), so it was not deleted. Try again.")
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)
	// A retired identity key stays refused after deletion; the box needs a factory reset (new key) to register again.
	if _, err := tx.Exec(ctx, `
        INSERT INTO retired_appliance_identities (public_key, serial, appliance_id, reason)
        SELECT public_key, serial, id, 'record deleted'
          FROM appliances WHERE id=$1 AND lifecycle_state IN ('revoked','decommissioned') AND COALESCE(public_key,'') <> ''
        ON CONFLICT (public_key) DO NOTHING`, id); err != nil {
		failInternal(w, r, "delete failed", err)
		return
	}
	// Close its open alerts rather than orphan them.
	if _, err := tx.Exec(ctx, `
        UPDATE appliance_security_alerts SET status='resolved', resolved=true,
               acknowledged_by=NULLIF($2,'')::uuid, acknowledged_at=now()
         WHERE appliance_id=$1 AND NOT resolved`, id, operatorID); err != nil {
		failInternal(w, r, "delete failed", err)
		return
	}
	tag, err := tx.Exec(ctx, `DELETE FROM appliances WHERE id=$1 AND lifecycle_state IN ('pending_approval','revoked','decommissioned')`, id)
	if err != nil {
		failInternal(w, r, "delete failed", err)
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusConflict, "invalid_state", "The appliance changed state; reload and try again.")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(ctx, b.DB, r, "appliance.deleted", "appliance", id, map[string]any{
		"_tenant_id": deref(row.CustomerID), "serial": row.Serial, "activation": row.Activation,
		"reason": in.Reason, "licenses_revoked": len(revoked)})
	w.WriteHeader(http.StatusNoContent)
}

// FetchLogRetention is how long assignment-fetch log rows (one per 30-second poll) are kept.
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
