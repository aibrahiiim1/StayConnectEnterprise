package auth

import "testing"

func sess(roles ...string) *Session { return &Session{Roles: roles} }

// Denial tests for every privilege boundary in the catalog.
func TestPermissionBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		s     *Session
		perm  string
		grant bool
	}{
		// platform boundaries
		{"platform_admin can manage plans", sess("platform_admin"), "platform.plans.manage", true},
		{"platform_support CANNOT manage plans", sess("platform_support"), "platform.plans.manage", false},
		{"platform_support can view tenants", sess("platform_support"), "platform.tenants.view", true},
		{"platform_billing can manage subscriptions", sess("platform_billing"), "platform.subscriptions.manage", true},
		{"platform_billing CANNOT issue licenses", sess("platform_billing"), "platform.licenses.issue", false},
		{"platform_owner is superset", sess("platform_owner"), "platform.licenses.revoke", true},
		// tenant boundaries
		{"tenant_admin can manage operators", sess("tenant_admin"), "tenant.operators.manage", true},
		{"tenant_admin CANNOT manage platform plans", sess("tenant_admin"), "platform.plans.manage", false},
		{"tenant_auditor CANNOT manage operators", sess("tenant_auditor"), "tenant.operators.manage", false},
		{"tenant_auditor can view reports", sess("tenant_auditor"), "tenant.reports.view", true},
		// site boundaries
		{"site_admin can manage site network", sess("site_admin"), "site.network.manage", true},
		{"site_admin CANNOT manage platform plans", sess("site_admin"), "platform.plans.manage", false},
		{"site_admin CANNOT manage tenant operators", sess("site_admin"), "tenant.operators.manage", false},
		{"hotel_operator can manage vouchers", sess("hotel_operator"), "site.vouchers.manage", true},
		{"hotel_operator CANNOT manage site network", sess("hotel_operator"), "site.network.manage", false},
		{"hotel_it CANNOT manage vouchers", sess("hotel_it"), "site.vouchers.manage", false},
		// no role grants nothing
		{"unknown role grants nothing", sess("nope"), "tenant.reports.view", false},
		{"nil session grants nothing", nil, "tenant.reports.view", false},
	}
	for _, c := range cases {
		if got := c.s.HasPermission(c.perm); got != c.grant {
			t.Errorf("%s: HasPermission(%s)=%v want %v", c.name, c.perm, got, c.grant)
		}
	}
}

// EnsureSiteAccess: site-bound operator only reaches their site; tenant-wide any.
func TestEnsureSiteAccess(t *testing.T) {
	siteA := &Session{Roles: []string{"site_admin"}, SiteIDs: []string{"site-a"}}
	if !siteA.EnsureSiteAccess("site-a") {
		t.Error("site_admin must reach own site")
	}
	if siteA.EnsureSiteAccess("site-b") {
		t.Error("site_admin must NOT reach another site")
	}
	tw := &Session{Roles: []string{"tenant_admin"}, TenantWide: true}
	if !tw.EnsureSiteAccess("any-site") {
		t.Error("tenant-wide operator must reach any site in tenant")
	}
	super := &Session{IsSuperAdmin: true}
	if !super.EnsureSiteAccess("whatever") {
		t.Error("super admin reaches any site")
	}
}
