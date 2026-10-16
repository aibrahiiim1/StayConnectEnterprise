//go:build integration

package main

// THE PACKAGE AND PLAN EDITORS' SERVER CONTRACT (audit O-1, O-15, O-20, O-21), through the real router, the
// real auth middleware and role matrix, and a real database:
//
//	a revision of an existing package states its commercial terms or is refused (commercial_fields_required)
//	the revision fields an editor does not show are carried from the current revision when absent
//	changing price, currency, methods or the plan revision needs the operator's password
//	revising a service plan needs the password and keeps its time accounting
//	reactivating a package and issuing vouchers need the password

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func newCommerceAPI(t *testing.T) *apiFixture {
	t.Helper()
	f := newAPI(t, "site_admin")
	a, err := iamv2.NewCommerceAdmin(iamv2.CommerceConfig{MasterEnabled: true, AdminEnabled: true},
		iamv2.NewPgCommerceAdminRepository(f.pool), iamv2.NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	a.AllowAggregateOnlineTime(true)
	f.app.commerce = a
	f.app.commerceCfg = iamv2.CommerceConfig{MasterEnabled: true, AdminEnabled: true}
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(f.app.requireAuth)
			mountResource(r, f.app, "commercial-packages", f.app.commercialPackagesRoutes)
			mountResource(r, f.app, "vouchers", f.app.vouchersRoutes)
		})
	})
	f.srv = httptest.NewServer(r)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *apiFixture) planRevision(t *testing.T, code string) (string, string) {
	t.Helper()
	_, out := f.do(t, "GET", "/commercial-packages/plans", nil)
	rows, _ := out["data"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if m["code"] == code {
			mode, _ := m["time_accounting_mode"].(string)
			return m["current_revision_id"].(string), mode
		}
	}
	t.Fatalf("plan %s not listed: %v", code, out)
	return "", ""
}

func (f *apiFixture) packageCurrent(t *testing.T, code string) map[string]any {
	t.Helper()
	_, out := f.do(t, "GET", "/commercial-packages", nil)
	rows, _ := out["data"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if m["code"] == code {
			_, cur := f.do(t, "GET", "/commercial-packages/"+m["package_id"].(string)+"/current", nil)
			return cur
		}
	}
	t.Fatalf("package %s not listed", code)
	return nil
}

func TestIntegration_PackageRevisionStatesItsTermsAndChangesNeedThePassword(t *testing.T) {
	f := newCommerceAPI(t)
	plan := map[string]any{"code": "PLN", "name": "Plan", "down_kbps": 2000, "up_kbps": 1000,
		"max_concurrent_devices": 2, "create_only": true}
	if code, out := f.do(t, "POST", "/commercial-packages/plans", plan); code != 200 {
		t.Fatalf("create plan: %d %v", code, out)
	}
	planRev, _ := f.planRevision(t, "PLN")
	pkg := func(extra map[string]any) map[string]any {
		b := map[string]any{"code": "VCH", "service_plan_revision_id": planRev,
			"display": map[string]any{"name": "Voucher day", "badge": "kept"}, "duration_policy": map[string]any{"end_mode": "MANUAL_END"},
			"grant_tiers": []map[string]any{{"order": 10, "grant": map[string]any{}}}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	terms := map[string]any{"price_minor": 0, "currency": "USD", "currency_exponent": 2,
		"acquisition_methods": []string{"PREPAID"}, "room_charge_mappings": []any{}}
	create := pkg(terms)
	create["create_only"] = true
	create["renewable"] = true
	create["max_purchases_per_stay"] = 2
	if code, out := f.do(t, "POST", "/commercial-packages", create); code != 200 {
		t.Fatalf("create a voucher-only package: %d %v", code, out)
	}

	// O-1: the repin's old body -- no price, no methods -- used to republish the package as FREE.
	if code, out := f.do(t, "POST", "/commercial-packages", pkg(nil)); code != 400 || out["error"] != "commercial_fields_required" {
		t.Fatalf("a revision without its terms must be refused: %d %v", code, out)
	}
	if cur := f.packageCurrent(t, "VCH"); cur["revision_no"].(float64) != 1 {
		t.Fatalf("the refused revision was published: %v", cur)
	}

	// A rename with the same terms needs no password -- and carries what the editor did not send (O-15).
	if code, out := f.do(t, "POST", "/commercial-packages", pkg(terms)); code != 200 {
		t.Fatalf("a rename with unchanged terms: %d %v", code, out)
	}
	cur := f.packageCurrent(t, "VCH")
	if cur["renewable"] != true || cur["max_purchases_per_stay"] != float64(2) {
		t.Fatalf("an edit dropped the carried fields: %v", cur)
	}
	if d, _ := cur["display"].(map[string]any); d["badge"] != "kept" {
		t.Fatalf("the display object was reduced: %v", cur["display"])
	}
	methods, _ := cur["settlement_methods"].([]any)
	if len(methods) != 1 || methods[0] != "PREPAID" {
		t.Fatalf("the voucher-only package lost its method: %v", cur["settlement_methods"])
	}

	// Changing how it is acquired needs the password (O-21).
	changed := map[string]any{}
	for k, v := range terms {
		changed[k] = v
	}
	changed["acquisition_methods"] = []string{"NOT_REQUIRED", "PREPAID"}
	if code, out := f.do(t, "POST", "/commercial-packages", pkg(changed)); code != 401 || out["error"] != "reauth_required" {
		t.Fatalf("a change of methods without the password: %d %v", code, out)
	}
	changed["password"] = "wrong"
	if code, _ := f.do(t, "POST", "/commercial-packages", pkg(changed)); code != 401 {
		t.Fatalf("a wrong password must be refused: %d", code)
	}
	changed["password"] = f.password
	if code, out := f.do(t, "POST", "/commercial-packages", pkg(changed)); code != 200 {
		t.Fatalf("the change with the password: %d %v", code, out)
	}

	// A plan change (the repin) needs the password too.
	if code, out := f.do(t, "POST", "/commercial-packages/plans", map[string]any{"code": "PLN2", "name": "Plan 2",
		"down_kbps": 4000, "up_kbps": 1000, "max_concurrent_devices": 2, "create_only": true}); code != 200 {
		t.Fatalf("create plan 2: %d %v", code, out)
	}
	plan2, _ := f.planRevision(t, "PLN2")
	moved := pkg(map[string]any{"price_minor": 0, "currency": "USD", "currency_exponent": 2,
		"acquisition_methods": []string{"NOT_REQUIRED", "PREPAID"}, "room_charge_mappings": []any{}})
	moved["service_plan_revision_id"] = plan2
	if code, _ := f.do(t, "POST", "/commercial-packages", moved); code != 401 {
		t.Fatalf("moving a package to another plan without the password: %d", code)
	}

	// Reactivation needs the password (T30); deactivation already did.
	id := f.packageCurrent(t, "VCH")["package_id"].(string)
	if code, out := f.do(t, "POST", "/commercial-packages/"+id+"/active",
		map[string]any{"active": false, "reason": "test", "password": f.password}); code != 200 {
		t.Fatalf("deactivate: %d %v", code, out)
	}
	if code, _ := f.do(t, "POST", "/commercial-packages/"+id+"/active", map[string]any{"active": true}); code != 401 {
		t.Fatalf("reactivation without the password: %d", code)
	}
	if code, out := f.do(t, "POST", "/commercial-packages/"+id+"/active",
		map[string]any{"active": true, "password": f.password}); code != 200 {
		t.Fatalf("reactivation with the password: %d %v", code, out)
	}

	// Issuing vouchers needs the password (T30). Refused before anything reaches scd.
	if code, out := f.do(t, "POST", "/vouchers/issue", map[string]any{"package_revision_id": planRev, "count": 1}); code != 401 ||
		out["error"] != "reauth_required" {
		t.Fatalf("voucher issue without the password: %d %v", code, out)
	}
}

func TestIntegration_ServicePlanEditKeepsItsTimeAccountingAndNeedsThePassword(t *testing.T) {
	f := newCommerceAPI(t)
	if code, out := f.do(t, "POST", "/commercial-packages/plans", map[string]any{"code": "AGG", "name": "Online time",
		"down_kbps": 2000, "up_kbps": 1000, "max_concurrent_devices": 2, "time_quota_seconds": 7200,
		"time_accounting_mode": "AGGREGATE_ONLINE_TIME", "create_only": true}); code != 200 {
		t.Fatalf("create an aggregate plan: %d %v", code, out)
	}
	edit := map[string]any{"code": "AGG", "name": "Online time, faster", "down_kbps": 4000, "up_kbps": 1000,
		"max_concurrent_devices": 2, "time_quota_seconds": 7200}
	if code, _ := f.do(t, "POST", "/commercial-packages/plans", edit); code != 401 {
		t.Fatalf("revising a plan without the password: %d", code)
	}
	edit["password"] = f.password
	if code, out := f.do(t, "POST", "/commercial-packages/plans", edit); code != 200 {
		t.Fatalf("revise with the password: %d %v", code, out)
	}
	if _, mode := f.planRevision(t, "AGG"); mode != "AGGREGATE_ONLINE_TIME" {
		t.Fatalf("editing an aggregate plan converted it to %q", mode)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.service_plan_revisions r
		JOIN iam_v2.service_plans p ON p.id = r.service_plan_id WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.code='AGG'`,
		f.tenant, f.site).Scan(&n); err != nil || n != 2 {
		t.Fatalf("expected two revisions, got %d (%v)", n, err)
	}
}
