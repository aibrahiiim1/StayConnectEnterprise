package api

import (
	"net/http"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// tenantScopeForList resolves the effective tenant for a customer-scoped LIST
// endpoint that supports the Global Customer Context "All Customers" mode.
//
//   - A regular operator is always pinned to their own tenant (EffectiveTenantID
//     returns their DefaultTenantID; any ?tenant_id= is ignored server-side).
//   - A super-admin (platform admin) may pass ?tenant_id=<uuid> to scope to one
//     customer, or omit it to request ALL customers (fan-out), in which case this
//     returns "" and the caller MUST use a `($1 = '' OR col::text = $1)` predicate.
//   - A non-super with no resolvable tenant is rejected with 400 (never silently
//     shown another tenant's data).
//
// Returns (tenantID, ok). When ok is false a 400 has already been written.
func (b *Base) tenantScopeForList(w http.ResponseWriter, r *http.Request) (string, bool) {
	tid := auth.EffectiveTenantID(r)
	if tid == "" {
		if s := auth.FromContext(r.Context()); s == nil || !s.IsSuperAdmin {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant scope required")
			return "", false
		}
	}
	return tid, true
}
