package api

import (
	"net/http"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type IdentityBase struct {
	*Base
	ReplayCache *applianceauth.ReplayCache
}

// OfflineReconcile marks an offline activation package consumed. Idempotent; the package must belong to the
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
	// Scoped to the authenticated appliance so one cannot reconcile another's package.
	tag, err := b.DB.Exec(ctx, `
        UPDATE offline_activation_packages
           SET consumed_at = COALESCE(consumed_at, now()), reconciled_at = now()
         WHERE package_id::text = $1 AND appliance_id = $2`, in.PackageID, ident.ApplianceID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "reconcile failed")
		return
	}
	if tag.RowsAffected() == 0 {
		WriteJSON(w, http.StatusOK, map[string]any{"status": "no_match"})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"status": "reconciled", "package_id": in.PackageID})
}

// HelloHandler is the signed liveness call and licence enforcement: a deleted appliance gets 401, a retired one 403.
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
