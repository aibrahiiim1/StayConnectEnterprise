package auth

import (
	"reflect"
	"testing"
)

func sess(roles ...string) *Session { return &Session{Roles: roles} }

// The §7 role table, cell by cell.
func TestRoleCatalog(t *testing.T) {
	every := []string{
		PermFleetView, PermCustomersManage, PermSitesManage, PermUsersManage, PermAppliancesManage,
		PermLicensesManage, PermSecurityManage, PermTeamManage,
		PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage,
	}
	want := map[string][]string{
		"platform_owner": {PermFleetView, PermCustomersManage, PermSitesManage, PermUsersManage,
			PermAppliancesManage, PermLicensesManage, PermSecurityManage, PermTeamManage},
		"platform_admin": {PermFleetView, PermCustomersManage, PermSitesManage, PermUsersManage,
			PermAppliancesManage, PermLicensesManage, PermSecurityManage, PermTeamManage},
		"platform_support": {PermFleetView},
		"tenant_owner":     {PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage},
		"tenant_admin":     {PermCustomerView, PermCustomerSitesManage, PermCustomerUsersManage},
		"tenant_auditor":   {PermCustomerView},
		"viewer":           {PermCustomerView},
		// Legacy roles grant nothing in Central.
		"platform_billing": {},
		"billing":          {},
		"tenant_operator":  {},
		"site_admin":       {},
		"hotel_it":         {},
		"hotel_operator":   {},
		"no_such_role":     {},
	}
	for role, grants := range want {
		g := map[string]bool{}
		for _, p := range grants {
			g[p] = true
		}
		for _, p := range every {
			if got := sess(role).HasPermission(p); got != g[p] {
				t.Errorf("%s: HasPermission(%s)=%v want %v", role, p, got, g[p])
			}
		}
	}
}

func TestLicenceAndActivationWritesArePlatformOnly(t *testing.T) {
	for _, role := range append(append([]string{}, CustomerRoles...), "platform_support") {
		for _, p := range []string{PermLicensesManage, PermAppliancesManage, PermCustomersManage} {
			if sess(role).HasPermission(p) {
				t.Errorf("%s must not hold %s", role, p)
			}
		}
	}
}

func TestNilSessionGrantsNothing(t *testing.T) {
	var s *Session
	if s.HasPermission(PermFleetView) {
		t.Fatal("nil session granted a permission")
	}
	if len(s.Permissions()) != 0 {
		t.Fatal("nil session listed permissions")
	}
}

func TestPermissionsUnionSorted(t *testing.T) {
	got := PermissionsForRoles([]string{"viewer", "tenant_admin", "hotel_it"})
	want := []string{PermCustomerSitesManage, PermCustomerUsersManage, PermCustomerView}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
