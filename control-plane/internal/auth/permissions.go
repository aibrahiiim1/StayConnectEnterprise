package auth

import (
	"net/http"
	"sort"
)

// The Central permission catalog (docs/CENTRAL_CONTROL_PLANE.md §7). Roles map to explicit permissions and
// the API enforces permissions, never role names.
//
// Platform permissions act on every customer. The customer.* permissions act ONLY on the customer the
// session belongs to (Session.DefaultTenantID); handlers resolve that scope, never the caller.
const (
	PermFleetView        = "fleet.view"        // read everything, every customer
	PermCustomersManage  = "customers.manage"  // create, rename, archive, restore, delete customers
	PermSitesManage      = "sites.manage"      // any customer's sites
	PermUsersManage      = "users.manage"      // any customer's users
	PermAppliancesManage = "appliances.manage" // activate, move, retire, replace, rebind, reissue, delete
	PermLicensesManage   = "licenses.manage"   // set, suspend, resume, revoke, offline licence
	PermSecurityManage   = "security.manage"   // security-alert triage
	PermTeamManage       = "team.manage"       // Central operators

	PermCustomerView        = "customer.view"         // read own customer
	PermCustomerSitesManage = "customer.sites.manage" // own customer's sites
	PermCustomerUsersManage = "customer.users.manage" // own customer's users
)

var platformAll = []string{
	PermFleetView, PermCustomersManage, PermSitesManage, PermUsersManage,
	PermAppliancesManage, PermLicensesManage, PermSecurityManage, PermTeamManage,
}

// rolePermissions is the executable catalog. A role that is absent grants nothing — which is exactly what the
// legacy roles (platform_billing, billing, tenant_operator, site_admin, hotel_it, hotel_operator) get.
var rolePermissions = map[string][]string{
	"platform_owner":   platformAll,
	"platform_admin":   platformAll,
	"platform_support": {PermFleetView},
	"tenant_owner":     {PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage},
	"tenant_admin":     {PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage},
	"tenant_auditor":   {PermCustomerView},
	"viewer":           {PermCustomerView},
}

// PlatformRoles are the roles a Central operator (Team) may hold. They carry no customer.
var PlatformRoles = []string{"platform_owner", "platform_admin", "platform_support"}

// CustomerRoles are the roles a customer's own user may hold. They are always bound to one customer.
var CustomerRoles = []string{"tenant_owner", "tenant_admin", "tenant_auditor", "viewer"}

// IsPlatformRole reports whether role is one of PlatformRoles.
func IsPlatformRole(role string) bool { return contains(PlatformRoles, role) }

// IsCustomerRole reports whether role is one of CustomerRoles.
func IsCustomerRole(role string) bool { return contains(CustomerRoles, role) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// PermissionsForRoles returns the sorted union of permissions the roles grant.
func PermissionsForRoles(roles []string) []string {
	set := map[string]bool{}
	for _, role := range roles {
		for _, p := range rolePermissions[role] {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// HasPermission reports whether any of the session's roles grants perm.
func (s *Session) HasPermission(perm string) bool {
	if s == nil {
		return false
	}
	for _, role := range s.Roles {
		for _, p := range rolePermissions[role] {
			if p == perm {
				return true
			}
		}
	}
	return false
}

// Permissions lists what the session may do (whoami).
func (s *Session) Permissions() []string {
	if s == nil {
		return []string{}
	}
	return PermissionsForRoles(s.Roles)
}

// RequirePermission is middleware that enforces a catalog permission.
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
