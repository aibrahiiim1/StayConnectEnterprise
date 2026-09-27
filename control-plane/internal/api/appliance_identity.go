// Appliance-facing identity endpoints: token-less registration lives in register.go; this file holds the
// signed hello and offline-package reconciliation.
package api

import (
	"net/http"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// IdentityBase serves the appliance-facing identity endpoints.
type IdentityBase struct {
	*Base
	ReplayCache *applianceauth.ReplayCache
}

// OfflineReconcile (appliance-authed) marks an offline activation package as
// consumed/reconciled centrally. Idempotent: repeating it never creates a
// duplicate record and never re-activates. The package must belong to the
// authenticated appliance.
func (b *IdentityBase) OfflineReconcile(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	var in struct {
		PackageID string `json:"package_id"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.PackageID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "package_id required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Idempotent: set consumed_at once, refresh reconciled_at; scope to the
	// authenticated appliance so one appliance can't reconcile another's.
	tag, err := b.DB.Exec(ctx, `
        UPDATE offline_activation_packages
           SET consumed_at = COALESCE(consumed_at, now()), reconciled_at = now()
         WHERE package_id::text = $1 AND appliance_id = $2`, in.PackageID, ident.ApplianceID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "reconcile failed")
		return
	}
	if tag.RowsAffected() == 0 {
		// Unknown/foreign package — do not create anything.
		WriteJSON(w, http.StatusOK, map[string]any{"status": "no_match"})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"status": "reconciled", "package_id": in.PackageID})
}

// HelloHandler is the signed liveness call. It is also licence ENFORCEMENT (CLAUDE.md §0E): a deleted
// appliance gets 401 here and learns it is orphaned; a retired one gets 403.
func (b *IdentityBase) HelloHandler(w http.ResponseWriter, r *http.Request) {
	a := auth.ApplianceFromContext(r.Context())
	if a == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"appliance_id": a.ApplianceID,
		"tenant_id":    a.TenantID,
		"site_id":      a.SiteID,
		"serial":       a.Serial,
		"server_time":  time.Now().UTC().Format(time.RFC3339),
	})
}
