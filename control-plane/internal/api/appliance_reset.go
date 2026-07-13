package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// deactivateAppliance revokes the appliance's active license (so the appliance
// deactivates on next check) and drops it back to 'assigned'. The appliance
// keeps its identity/assignment; re-activating (or a new license) reactivates
// it. Self-service reset for testing. Permission + step-up gated.
func (b *Base) deactivateAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var exists bool
	if err := b.DB.QueryRow(ctx, `SELECT true FROM appliances WHERE id=$1`, id).Scan(&exists); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	// Centralized license termination — revoke the licenses bound to THIS appliance
	// (deactivate is reversible: identity/certificate are intentionally preserved).
	revoked, _ := b.revokeApplianceBoundLicenses(ctx, id)
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state='assigned', updated_at=now()
	    WHERE id=$1 AND lifecycle_state IN ('activated','licensed','online','offline','grace')`, id)
	audit.Op(r.Context(), b.DB, r, "appliance.deactivated", "appliance", id, map[string]any{"licenses_revoked": len(revoked)})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "deactivated", "licenses_revoked": len(revoked)})
}

// applianceImpact counts the technical leaf records that will be removed with an
// appliance, for the pre-delete impact preview and the audit event.
func (b *Base) applianceImpact(ctx context.Context, id string) map[string]int {
	imp := map[string]int{}
	for label, q := range map[string]string{
		"certificates":         `SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1`,
		"certificate_requests": `SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1`,
		"signed_assignments":   `SELECT count(*) FROM appliance_signed_assignments WHERE appliance_id=$1`,
		"assignments":          `SELECT count(*) FROM appliance_assignments WHERE appliance_id=$1`,
		"commands":             `SELECT count(*) FROM appliance_commands WHERE appliance_id=$1`,
		"networks":             `SELECT count(*) FROM networks WHERE appliance_id=$1`,
		"lifecycle_events":     `SELECT count(*) FROM appliance_lifecycle_events WHERE appliance_id=$1`,
	} {
		var n int
		if b.DB.QueryRow(ctx, q, id).Scan(&n) == nil && n > 0 {
			imp[label] = n
		}
	}
	return imp
}

// GetApplianceDeleteImpact (GET .../{id}/delete-impact) returns the technical
// records that a delete would remove, so the UI can show a preview BEFORE the
// operator confirms. Read-only, view permission.
func (b *Base) GetApplianceDeleteImpact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var serial, siteID string
	if err := b.DB.QueryRow(ctx, `SELECT serial, COALESCE(site_id::text,'') FROM appliances WHERE id=$1`, id).Scan(&serial, &siteID); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	licenses := []string{}
	if siteID != "" {
		if ids, _, err := b.countIDs(ctx, `SELECT id::text FROM licenses WHERE site_id=$1 AND status IN ('active','suspended')`, siteID); err == nil {
			licenses = ids
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"serial":            serial,
		"technical_records": b.applianceImpact(ctx, id),
		"licenses_revoked":  licenses,
		"terminates":        []string{"API mTLS access", "NATS access", "signed assignment"},
	})
}

// forceReconcile re-asserts an appliance's commercial lifecycle state from its
// current signed license — an Advanced Support recovery for an appliance that is
// stuck (e.g. licensed centrally but not converged). It never changes identity,
// assignment or certificates; it only re-drives the lifecycle_state and records
// an immutable reconcile event + audit.
func (b *Base) forceReconcile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	_ = DecodeJSON(r, &in)
	ctx, cancel := DBCtx(r)
	defer cancel()

	var siteID, from string
	if err := b.DB.QueryRow(ctx, `SELECT COALESCE(site_id::text,''), COALESCE(lifecycle_state,'') FROM appliances WHERE id=$1`, id).Scan(&siteID, &from); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	to := from
	if siteID != "" {
		var activeLicenses int
		_ = b.DB.QueryRow(ctx, `SELECT count(*) FROM licenses WHERE site_id=$1 AND status='active'`, siteID).Scan(&activeLicenses)
		switch {
		case activeLicenses > 0 && (from == "assigned" || from == "grace" || from == "license_expired" || from == "suspended"):
			to = "licensed"
		case activeLicenses == 0 && (from == "licensed" || from == "activated"):
			// No active license but marked licensed → drop to assigned so it re-licenses.
			to = "assigned"
		}
	}
	if to != from {
		_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state=$2, updated_at=now() WHERE id=$1`, id, to)
	}
	actor := ""
	if s := auth.FromContext(r.Context()); s != nil {
		actor = s.OperatorID
	}
	recordLifecycle(ctx, b.DB, id, from, to, actor, clientIPFromReq(r), "force-reconcile: "+in.Reason)
	audit.Op(r.Context(), b.DB, r, "appliance.force_reconciled", "appliance", id, map[string]any{
		"from": from, "to": to, "reason": in.Reason,
	})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "reconciled", "from": from, "to": to})
}

// deleteApplianceAdmin permanently removes an appliance and everything bound to
// it — signed assignments, certificate requests + certs, commands, networks — via
// ON DELETE CASCADE, which also terminates its API mTLS and NATS access (both are
// validated against these rows). Its site license is revoked first. After this
// the physical appliance re-registers as a fresh Pending on its next check.
//
// Requires permission + password step-up (route), a reason and a typed serial
// confirmation, all recorded in an immutable audit event. It NEVER touches
// another appliance, site or customer.
func (b *Base) deleteApplianceAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var serial, siteID string
	if err := b.DB.QueryRow(ctx, `SELECT serial, COALESCE(site_id::text,'') FROM appliances WHERE id=$1`, id).Scan(&serial, &siteID); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}

	var req deleteConfirm
	_ = DecodeJSON(r, &req)
	if req.Confirm != serial {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"type the exact appliance serial to confirm permanent deletion",
			map[string]any{"expected": serial})
		return
	}

	impact := b.applianceImpact(ctx, id)

	// Revoke + mark the appliance's certificates (defence in depth: the cascade
	// delete below also removes them, terminating mTLS + NATS trust).
	_, _ = b.DB.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now() WHERE appliance_id=$1 AND status='active'`, id)

	// Centralized license termination — revoke licenses BOUND to this appliance
	// (a site-wide license stays valid while the site exists).
	revoked, _ := b.revokeApplianceBoundLicenses(ctx, id)

	// Audit BEFORE the delete so the record is durable and complete.
	audit.Op(r.Context(), b.DB, r, "appliance.deleted", "appliance", id, map[string]any{
		"serial": serial, "site_id": siteID, "reason": req.Reason,
		"licenses_revoked": len(revoked), "technical_records": impact,
	})

	ct, err := b.DB.Exec(ctx, `DELETE FROM appliances WHERE id=$1`, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
