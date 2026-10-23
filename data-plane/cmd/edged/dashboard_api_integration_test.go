//go:build integration

package main

// Authorization: /reports/dashboard goes through mountResource, so it inherits requireAuth and the `reports`
// row; wiring silently regresses when a route is moved, so the real middleware chain is asserted (401 / 403).
//
// Site confinement: the dashboard aggregates eight tables and a tenant-only WHERE would leak one property's
// counts into another under the same tenant, so every assertion seeds a second site under the same tenant,
// which a single-site fixture can never fail on.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedSiteActivity fills one site with one of everything the dashboard counts and returns the guest network's
// bridge name (derived from the network's uuid; bridge names are globally unique).
func seedSiteActivity(t *testing.T, f *apiFixture, roomNumber string) string {
	t.Helper()
	ctx := context.Background()

	var iface string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias',$3,'ACTIVE') RETURNING id::text`,
		f.tenant, f.site, fmt.Sprintf("PMS %s %d", roomNumber, time.Now().UnixNano())).Scan(&iface); err != nil {
		t.Fatalf("seed interface: %v", err)
	}

	var stay string
	// posting_allowed is computed by the database.
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
		INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
		  external_stay_identity,normalized_room_number,status,arrival,departure)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,$4,$5,'IN_HOUSE',current_date,current_date)
		RETURNING id::text`,
			f.tenant, f.site, iface, "R"+roomNumber, roomNumber).Scan(&stay)
	}); err != nil {
		t.Fatalf("seed stay: %v", err)
	}

	// Inserted PENDING with no stay_id: iam_v2.p3_stay_event_appendonly requires events to arrive unprocessed and
	// unresolved; only the applier may move them on.
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,external_event_identity,
		  event_type,processing_status,received_at)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,'GUEST_IN','PENDING',now())`,
			f.tenant, f.site, iface, fmt.Sprintf("EV-%s-%d", roomNumber, time.Now().UnixNano()))
		return err
	}); err != nil {
		t.Fatalf("seed stay event: %v", err)
	}

	// Seeded with addressing, not just a name: the dashboard reports the subnet and counts devices by bridge.
	gn, bridge := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Guest VLAN "+roomNumber, "10."+roomNumber[:1]+".0.0/24")
	if err := controlled(ctx, f.pool, []string{"auth_resolution"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO iam_v2.auth_resolutions(id,tenant_id,site_id,guest_network_id,resolved_stay_id,
		  outcome_code,resolved_at)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4::uuid,'VERIFIED',now())`,
			f.tenant, f.site, gn, stay)
		return err
	}); err != nil {
		t.Fatalf("seed resolution: %v", err)
	}
	return bridge
}

func (f *apiFixture) body(t *testing.T, method, path string, payload any) map[string]any {
	t.Helper()
	code, b := f.do(t, method, path, payload)
	if code != http.StatusOK {
		t.Fatalf("%s %s status %d: %v", method, path, code, b)
	}
	return b
}

func (f *apiFixture) dashboard(t *testing.T) map[string]any {
	t.Helper()
	code, body := f.do(t, http.MethodGet, "/reports/dashboard", nil)
	if code != http.StatusOK {
		t.Fatalf("dashboard status %d: %v", code, body)
	}
	return body
}

func num(t *testing.T, m map[string]any, section, key string) float64 {
	t.Helper()
	sec, ok := m[section].(map[string]any)
	if !ok {
		t.Fatalf("section %q missing from the dashboard payload: %v", section, m)
	}
	v, ok := sec[key].(float64)
	if !ok {
		t.Fatalf("%s.%s missing or not a number: %v", section, key, sec[key])
	}
	return v
}

// The snapshot is confined to one site even when another site of the same customer is busy.
func TestIntegration_API_DashboardCountsOnlyItsOwnSite(t *testing.T) {
	a := newAPI(t)
	seedSiteActivity(t, a, "101")

	b := newAPIIn(t, a.tenant)
	if b.tenant != a.tenant {
		t.Fatalf("fixture did not reuse the tenant: %s vs %s", a.tenant, b.tenant)
	}
	if b.site == a.site {
		t.Fatal("fixture did not create a second site")
	}
	seedSiteActivity(t, b, "202")
	seedSiteActivity(t, b, "203")

	got := a.dashboard(t)

	if v := num(t, got, "occupancy", "in_house"); v != 1 {
		t.Fatalf("occupancy.in_house = %v; site B's stays have leaked into site A", v)
	}
	if v := num(t, got, "occupancy", "arrivals_today"); v != 1 {
		t.Fatalf("occupancy.arrivals_today = %v; want 1", v)
	}

	// Seeded events are PENDING, so `applied` is legitimately zero.
	if v := num(t, got, "pms", "events_today"); v != 1 {
		t.Fatalf("pms.events_today = %v; site B's PMS traffic has leaked into site A", v)
	}
	if v := num(t, got, "pms", "events_applied_today"); v != 0 {
		t.Fatalf("pms.events_applied_today = %v; nothing was applied, so this must be 0", v)
	}
	ifaces, _ := got["pms"].(map[string]any)["interfaces"].([]any)
	if len(ifaces) != 1 {
		t.Fatalf("pms.interfaces has %d entries; site B's interfaces are visible on site A", len(ifaces))
	}

	if v := num(t, got, "sign_in_checks", "total"); v != 1 {
		t.Fatalf("sign_in_checks.total = %v; site B's attempts have leaked into site A", v)
	}
	if v := num(t, got, "sign_in_checks", "verified"); v != 1 {
		t.Fatalf("sign_in_checks.verified = %v; want 1", v)
	}

	// The other direction, so the test cannot pass on an endpoint that returns zeros.
	other := b.dashboard(t)
	if v := num(t, other, "occupancy", "in_house"); v != 2 {
		t.Fatalf("site B's own dashboard shows in_house = %v; want its own 2", v)
	}
	if v := num(t, other, "pms", "events_today"); v != 2 {
		t.Fatalf("site B's own dashboard shows events_today = %v; want its own 2", v)
	}
}

// public.guest_networks is the one snapshot table outside iam_v2; a missing site term there leaks network
// topology, a more sensitive disclosure than a count.
func TestIntegration_API_DashboardNetworksAreSiteConfined(t *testing.T) {
	a := newAPI(t)
	wantBridge := seedSiteActivity(t, a, "301")
	b := newAPIIn(t, a.tenant)
	seedSiteActivity(t, b, "302")

	nets, _ := a.dashboard(t)["networks"].([]any)
	if len(nets) != 1 {
		t.Fatalf("networks has %d entries; another site's guest networks are visible", len(nets))
	}
	first := nets[0].(map[string]any)
	if name, _ := first["name"].(string); name != "Guest VLAN 301" {
		t.Fatalf("networks[0].name = %q; the wrong site's network is being reported", name)
	}
	if cidr, _ := first["subnet_cidr"].(string); cidr != "10.3.0.0/24" {
		t.Fatalf("networks[0].subnet_cidr = %q; the network's addressing was not reported", cidr)
	}
	if br, _ := first["bridge_name"].(string); br != wantBridge {
		t.Fatalf("networks[0].bridge_name = %q", br)
	}
}

// The package block joins four tables; the outer WHERE looks scoped while the sub-selects do the counting.
func TestIntegration_API_DashboardPackagesAreSiteConfined(t *testing.T) {
	a := newAPI(t)
	b := newAPIIn(t, a.tenant)
	// is_system packages are excluded from the operator catalogue, so seed non-system ones.
	mkPkg := func(f *apiFixture, code string) {
		t.Helper()
		if _, err := f.pool.Exec(context.Background(), `WITH
		  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
		         VALUES (gen_random_uuid(),$1,$2,$3||'-plan',true) RETURNING id),
		  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
		            down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode)
		          SELECT gen_random_uuid(),$1,$2,sp.id,1,8000,2000,3,'REJECT_NEW_DEVICE','VALIDITY_WINDOW'
		          FROM sp RETURNING id),
		  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
		         VALUES (gen_random_uuid(),$1,$2,$3,false,true) RETURNING id),
		  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
		            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy,display)
		          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'GENERAL',0,ARRAY['NOT_REQUIRED']::text[],
		                 '{}'::jsonb, jsonb_build_object('name',$3)
		          FROM ip, spr RETURNING id, package_id)
		  UPDATE iam_v2.internet_packages p SET current_revision_id = ipr.id
		    FROM ipr WHERE p.id = ipr.package_id`, f.tenant, f.site, code); err != nil {
			t.Fatalf("seed package %s: %v", code, err)
		}
	}
	mkPkg(a, "SITE-A-PKG")
	mkPkg(b, "SITE-B-PKG")

	pkgs, _ := a.dashboard(t)["packages"].([]any)
	if len(pkgs) != 1 {
		t.Fatalf("packages has %d entries; another site's catalogue is visible: %v", len(pkgs), pkgs)
	}
	if code, _ := pkgs[0].(map[string]any)["code"].(string); code != "SITE-A-PKG" {
		t.Fatalf("packages[0].code = %q; the wrong site's package is being reported", code)
	}
}

// Unauthenticated is 401, not an empty snapshot: a 200 with zeros would still disclose the property's shape.
func TestIntegration_API_DashboardRefusesAnonymousCallers(t *testing.T) {
	f := newAPI(t)
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/edge/v1/reports/dashboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard read answered %d; want 401", resp.StatusCode)
	}
}

// Fails if the route is ever moved out of mountResource onto a bare router.
func TestIntegration_API_DashboardRequiresTheReportsPermission(t *testing.T) {
	// operator_roles' CHECK refuses an unknown role, so use `billing`: accepted by the database, no row in rolePerms.
	f := newAPI(t, "billing")
	code, _ := f.do(t, http.MethodGet, "/reports/dashboard", nil)
	if code != http.StatusForbidden {
		t.Fatalf("a role with no reports claim read the dashboard with status %d; want 403", code)
	}

	// A role holding reports:read gets it, so the refusal above is the permission check, not a broken route.
	v := newAPI(t, "site_viewer")
	if code, _ := v.do(t, http.MethodGet, "/reports/dashboard", nil); code != http.StatusOK {
		t.Fatalf("site_viewer could not read the dashboard: status %d", code)
	}
}

func (f *apiFixture) seedNet(t *testing.T, name string) string {
	t.Helper()
	id, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, name, "10.250.0.0/24")
	return id
}

// Routing write is site_admin only, enforced by the server rather than by hiding the button.
func TestIntegration_API_RoutingWriteIsRefusedToEveryNonAdminRole(t *testing.T) {
	for _, role := range []string{"hotel_it_manager", "front_office_operator", "guest_relations_operator", "site_viewer"} {
		t.Run(role, func(t *testing.T) {
			f := newAPI(t, role)
			iface, _, _ := f.seedInterface(t)
			gn := f.seedNet(t, "Guest VLAN "+role)

			code, _ := f.do(t, http.MethodPut, "/pms-routing/"+gn,
				map[string]any{"pms_interface_id": iface, "routing_mode": "MAPPED"})
			if code != http.StatusForbidden {
				t.Fatalf("%s mapped a network with status %d; want 403", role, code)
			}
			if code, _ := f.do(t, http.MethodDelete, "/pms-routing/"+gn, nil); code != http.StatusForbidden {
				t.Fatalf("%s cleared a mapping with status %d; want 403", role, code)
			}
			if code, _ := f.do(t, http.MethodGet, "/pms-routing", nil); code != http.StatusOK {
				t.Fatalf("%s could not read the routing list: status %d", role, code)
			}
		})
	}
}

// The mapping is read back through the API, not the table.
func TestIntegration_API_SiteAdminCanSetAndClearARoute(t *testing.T) {
	f := newAPI(t) // site_admin
	iface, _, _ := f.seedInterface(t)
	gn := f.seedNet(t, "Guest VLAN set-clear")

	if code, body := f.do(t, http.MethodPut, "/pms-routing/"+gn,
		map[string]any{"pms_interface_id": iface, "routing_mode": "MAPPED"}); code != http.StatusOK {
		t.Fatalf("set route status %d: %v", code, body)
	}
	routes, _ := f.body(t, http.MethodGet, "/pms-routing", nil)["routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["guest_network_id"] != gn {
		t.Fatalf("the route was not read back: %v", routes)
	}

	if code, body := f.do(t, http.MethodDelete, "/pms-routing/"+gn, nil); code != http.StatusOK {
		t.Fatalf("clear route status %d: %v", code, body)
	}
	after, _ := f.body(t, http.MethodGet, "/pms-routing", nil)["routes"].([]any)
	if len(after) != 0 {
		t.Fatalf("the mapping survived its deletion: %v", after)
	}
	unmapped, _ := f.body(t, http.MethodGet, "/pms-routing", nil)["unmapped_guest_networks"].([]any)
	if len(unmapped) != 1 {
		t.Fatalf("the network did not return to the unmapped list: %v", unmapped)
	}
}

// Another site's PMS interface must be refused: it would point one property's Wi-Fi at another's guest list
// while every other surface reports healthy.
func TestIntegration_API_RoutingRefusesAnotherSitesInterface(t *testing.T) {
	a := newAPI(t)
	b := newAPIIn(t, a.tenant)
	foreign, _, _ := b.seedInterface(t)
	gn := a.seedNet(t, "Guest VLAN cross-site")

	code, body := a.do(t, http.MethodPut, "/pms-routing/"+gn,
		map[string]any{"pms_interface_id": foreign, "routing_mode": "MAPPED"})
	if code == http.StatusOK {
		t.Fatalf("a guest network was mapped to another site's PMS interface: %v", body)
	}
	if code != http.StatusBadRequest && code != http.StatusNotFound && code != http.StatusForbidden {
		t.Fatalf("cross-site mapping refused with an unexpected status %d: %v", code, body)
	}
	routes, _ := a.body(t, http.MethodGet, "/pms-routing", nil)["routes"].([]any)
	if len(routes) != 0 {
		t.Fatalf("a cross-site mapping was persisted despite the refusal: %v", routes)
	}
}

func TestIntegration_API_RoutingRefusesAnotherSitesGuestNetwork(t *testing.T) {
	a := newAPI(t)
	b := newAPIIn(t, a.tenant)
	iface, _, _ := a.seedInterface(t)
	foreignNet := b.seedNet(t, "Guest VLAN other-site")

	code, body := a.do(t, http.MethodPut, "/pms-routing/"+foreignNet,
		map[string]any{"pms_interface_id": iface, "routing_mode": "MAPPED"})
	if code == http.StatusOK {
		t.Fatalf("another site's guest network was mapped: %v", body)
	}
	routes, _ := a.body(t, http.MethodGet, "/pms-routing", nil)["routes"].([]any)
	if len(routes) != 0 {
		t.Fatalf("a mapping for another site's network was persisted: %v", routes)
	}
}
