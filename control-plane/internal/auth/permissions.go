package auth

import (
	"net/http"
	"slices"
)

// Executable permission catalog. Roles are mapped to explicit permission
// strings and enforced in the API (RequirePermission), not merely documented.
// A platform_owner is a super-set. This is authorization, not navigation.
var rolePermissions = map[string][]string{
	"platform_owner": {"*"}, // everything
	"platform_admin": {
		"platform.tenants.view", "platform.tenants.manage",
		"platform.plans.view", "platform.plans.manage",
		"platform.subscriptions.view", "platform.subscriptions.manage",
		"platform.licenses.view", "platform.licenses.issue", "platform.licenses.revoke",
		"platform.appliances.view", "platform.appliances.manage",
		"platform.appliances.claim", "platform.appliances.assign",
		"platform.appliances.reassign", "platform.appliances.revoke",
		"platform.enrollment_tokens.create", "platform.enrollment_tokens.revoke",
		"platform.certificates.issue", "platform.certificates.revoke",
		"platform.commands.issue",
		"platform.fleet.view", "platform.updates.manage",
		"platform.operators.manage", "platform.audit.view",
	},
	"platform_support": {
		"platform.tenants.view", "platform.plans.view", "platform.subscriptions.view",
		"platform.licenses.view", "platform.appliances.view", "platform.fleet.view",
		"platform.audit.view",
	},
	"platform_billing": {
		"platform.tenants.view", "platform.plans.view", "platform.plans.manage",
		"platform.subscriptions.view", "platform.subscriptions.manage",
		"platform.licenses.view", "platform.audit.view",
	},
	"tenant_owner": {
		"tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
		"tenant.subscription.view", "tenant.operators.manage", "tenant.audit.view",
	},
	"tenant_admin": {
		"tenant.sites.view", "tenant.appliances.view", "tenant.appliances.support_request",
		"tenant.reports.view", "tenant.subscription.view", "tenant.operators.manage", "tenant.audit.view",
	},
	"tenant_auditor": {
		"tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
		"tenant.subscription.view", "tenant.audit.view",
	},
	"site_admin": {
		"tenant.sites.view", "tenant.appliances.view",
		"site.reports.view", "site.vouchers.manage", "site.pms.manage",
		"site.portal.manage", "site.payments.manage", "site.network.manage", "site.audit.view",
	},
	"hotel_it": {
		"site.reports.view", "site.network.manage", "site.pms.manage", "site.portal.manage", "site.audit.view",
	},
	"hotel_operator": {
		"site.reports.view", "site.vouchers.manage", "site.audit.view",
	},
	// Legacy role names carried over from the pre-separation schema, mapped to
	// read-only equivalents so existing operators keep least-privilege access.
	"viewer": {
		"tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
		"tenant.subscription.view", "tenant.audit.view",
	},
	"tenant_operator": {
		"tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
		"tenant.subscription.view", "site.reports.view",
	},
}

// HasPermission reports whether any of the session's roles grants perm.
func (s *Session) HasPermission(perm string) bool {
	if s == nil {
		return false
	}
	for _, role := range s.Roles {
		perms, ok := rolePermissions[role]
		if !ok {
			continue
		}
		for _, p := range perms {
			if p == "*" || p == perm {
				return true
			}
		}
	}
	return false
}

// RequirePermission is middleware that enforces a catalog permission. Role
// membership alone is not sufficient — the permission must be granted.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := FromContext(r.Context())
			if s == nil {
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "login required", r)
				return
			}
			if !s.HasPermission(perm) {
				jsonErr(w, http.StatusForbidden, "forbidden", "missing permission: "+perm, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// EnsureSiteAccess enforces site-level isolation: a site-bound operator may only
// reach their own site(s); a tenant-wide operator may reach any site in the
// tenant; a super admin may reach any site. Cross-tenant is handled upstream.
func (s *Session) EnsureSiteAccess(siteID string) bool {
	if s == nil {
		return false
	}
	if s.IsSuperAdmin || s.TenantWide {
		return true
	}
	return slices.Contains(s.SiteIDs, siteID)
}
