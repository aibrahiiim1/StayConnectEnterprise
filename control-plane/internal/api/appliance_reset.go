package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

// deactivateAppliance revokes the appliance's active license (so the appliance
// deactivates on next check) and drops it back to 'assigned'. The appliance
// keeps its identity/assignment; re-activating (or a new license) reactivates
// it. Self-service reset for testing. Permission + step-up gated.
func (b *Base) deactivateAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var siteID string
	if err := b.DB.QueryRow(ctx, `SELECT COALESCE(site_id::text,'') FROM appliances WHERE id=$1`, id).Scan(&siteID); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	revoked := 0
	if siteID != "" && b.Lic != nil {
		rows, err := b.DB.Query(ctx, `SELECT id::text FROM licenses WHERE site_id=$1 AND status IN ('active','suspended')`, siteID)
		if err == nil {
			var ids []string
			for rows.Next() {
				var lid string
				if rows.Scan(&lid) == nil {
					ids = append(ids, lid)
				}
			}
			rows.Close()
			for _, lid := range ids {
				if b.Lic.Revoke(ctx, lid) == nil {
					revoked++
				}
			}
		}
	}
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state='assigned', updated_at=now()
	    WHERE id=$1 AND lifecycle_state IN ('activated','licensed','online','offline','grace')`, id)
	audit.Op(r.Context(), b.DB, r, "appliance.deactivated", "appliance", id, map[string]any{"licenses_revoked": revoked})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "deactivated", "licenses_revoked": revoked})
}

// deleteApplianceAdmin permanently removes an appliance (any state, incl.
// unassigned/pending) and everything bound to it — signed assignments,
// certificate requests + certs, commands, networks, security alerts — via
// ON DELETE CASCADE. Licenses reference the site (not the appliance), so any
// active license for the site is revoked first so it can't linger. After this
// the physical appliance re-registers as a fresh Pending on its next check.
// Permission + step-up gated.
func (b *Base) deleteApplianceAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var siteID string
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(site_id::text,'') FROM appliances WHERE id=$1`, id).Scan(&siteID)
	if siteID != "" && b.Lic != nil {
		rows, err := b.DB.Query(ctx, `SELECT id::text FROM licenses WHERE site_id=$1 AND status IN ('active','suspended')`, siteID)
		if err == nil {
			var ids []string
			for rows.Next() {
				var lid string
				if rows.Scan(&lid) == nil {
					ids = append(ids, lid)
				}
			}
			rows.Close()
			for _, lid := range ids {
				_ = b.Lic.Revoke(ctx, lid)
			}
		}
	}
	ct, err := b.DB.Exec(ctx, `DELETE FROM appliances WHERE id=$1`, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "appliance.deleted", "appliance", id, map[string]any{"site_id": siteID})
	w.WriteHeader(http.StatusNoContent)
}
