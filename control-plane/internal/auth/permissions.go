package auth

import (
	"net/http"
	"sort"
)

// The API enforces permissions, never role names. Platform permissions act on every customer; customer.* permissions
// act only on the session's own customer (Session.DefaultTenantID), resolved by handlers, never by the caller.
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

// rolePermissions is the catalog. An absent role grants nothing, which is what legacy roles get.
var rolePermissions = map[string][]string{
	"platform_owner":   platformAll,
	"platform_admin":   platformAll,
	"platform_support": {PermFleetView},
	"tenant_owner":     {PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage},
	"tenant_admin":     {PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage},
	"tenant_auditor":   {PermCustomerView},
	"viewer":           {PermCustomerView},
}

// PlatformRoles are the roles a Team operator may hold; they carry no customer.
var PlatformRoles = []string{"platform_owner", "platform_admin", "platform_support"}

// CustomerRoles are the roles a customer's user may hold; always bound to one customer.
var CustomerRoles = []string{"tenant_owner", "tenant_admin", "tenant_auditor", "viewer"}

func IsPlatformRole(role string) bool { return contains(PlatformRoles, role) }

func IsCustomerRole(role string) bool { return contains(CustomerRoles, role) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

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

func (s *Session) Permissions() []string {
	if s == nil {
		return []string{}
	}
	return PermissionsForRoles(s.Roles)
}

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
