//go:build integration

package main

// THE DASHBOARD SNAPSHOT AND THE ROUTING MUTATIONS, AGAINST A REAL ROUTER AND A REAL DATABASE.
//
// Two things are proved here that no browser test and no unit test can reach, and both are the reason this file
// exists rather than being covered by the UI suite:
//
//   AUTHORIZATION — /reports/dashboard is a new read surface. It is mounted through mountResource, so it
//   inherits requireAuth and the `reports` row of the role matrix; that is an ASSERTION about wiring, and wiring
//   is exactly what silently regresses when a route is moved. An unauthenticated caller must get 401 and a role
//   with no claim on `reports` must get 403 — from the real middleware chain, not from a mock.
//
//   SITE CONFINEMENT — the endpoint aggregates eight different tables. Tenant scoping alone is not the contract:
//   a multi-property customer has several sites under ONE tenant, so a tenant-only WHERE clause would let one
//   property's dashboard count another property's guests, stays, PMS messages and packages. Every assertion
//   below seeds a SECOND site under the SAME tenant and requires the first site's figures to ignore it. This is
//   the case a single-site fixture cannot fail on, which is precisely why it is written this way.
//
// The routing mutations get the same treatment from the other direction: they are the one WRITE this delivery
// added, edged confines them to site_admin, and a guest network belonging to another site must be refused rather
// than silently mapped.
//
// Nothing here touches an appliance, a production database, a PMS or any live service.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// seedSiteActivity fills one site with one of everything the dashboard counts, and returns nothing: the point is
// always "does the OTHER site see it", never the ids.
func seedSiteActivity(t *testing.T, f *apiFixture, roomNumber string) {
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
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
		  external_stay_identity,normalized_room_number,status,posting_allowed,arrival,departure)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,$4,$5,'IN_HOUSE',true,current_date,current_date)
		RETURNING id::text`,
		f.tenant, f.site, iface, "R"+roomNumber, roomNumber).Scan(&stay); err != nil {
		t.Fatalf("seed stay: %v", err)
	}

	// A PMS message on this site's feed, so the PMS block has something to count.
	//
	// INSERTED AS PENDING WITH NO stay_id, because iam_v2.p3_stay_event_appendonly enforces exactly that: an
	// event arrives unprocessed and unresolved, and only the applier may move it on. A fixture that wrote
	// 'APPLIED' with a pre-resolved stay_id was rejected by the trigger -- correctly, and the test is corrected
	// rather than the rule being worked around, because that rule is the Stay domain's append-only guarantee.
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,external_event_identity,
		  event_type,processing_status,received_at)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,'GUEST_IN','PENDING',now())`,
		f.tenant, f.site, iface, fmt.Sprintf("EV-%s-%d", roomNumber, time.Now().UnixNano())); err != nil {
		t.Fatalf("seed stay event: %v", err)
	}

	// A guest network and a successful sign-in check, so the sign-in block has something too.
	var gn string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO public.guest_networks(id,tenant_id,site_id,name,enabled)
		VALUES (gen_random_uuid(),$1,$2,$3,true) RETURNING id::text`,
		f.tenant, f.site, "Guest VLAN "+roomNumber).Scan(&gn); err != nil {
		t.Fatalf("seed guest network: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.auth_resolutions(id,tenant_id,site_id,guest_network_id,resolved_stay_id,
		  outcome_code,resolved_at)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4::uuid,'VERIFIED',now())`,
		f.tenant, f.site, gn, stay); err != nil {
		t.Fatalf("seed resolution: %v", err)
	}
}

// body is `do` with the status asserted, for the reads whose status is not the thing under test.
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

// THE SNAPSHOT IS CONFINED TO ONE SITE, even when another site of the SAME customer is busy.
//
// A tenant-only WHERE clause passes a single-site fixture and fails here, which is the whole point: a hotel group
// on one appliance estate would see its sister property's occupancy, PMS traffic and sign-ins folded into its own
// dashboard, and every number on the screen would be wrong in a way nobody could explain.
func TestIntegration_API_DashboardCountsOnlyItsOwnSite(t *testing.T) {
	a := newAPI(t)
	seedSiteActivity(t, a, "101")

	// A second site under the SAME tenant, with its own interface, stay, event, network and resolution.
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

	// Occupancy: site A has exactly its own one in-house stay, never site B's two.
	if v := num(t, got, "occupancy", "in_house"); v != 1 {
		t.Fatalf("occupancy.in_house = %v; site B's stays have leaked into site A", v)
	}
	if v := num(t, got, "occupancy", "arrivals_today"); v != 1 {
		t.Fatalf("occupancy.arrivals_today = %v; want 1", v)
	}

	// The PMS block counts this site's feed only. The seeded events are PENDING (the domain admits them no other
	// way), so `applied` is legitimately zero -- asserting that too keeps the test honest about what was seeded
	// instead of quietly passing on a number it did not create.
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

	// Sign-in checks are site-confined too.
	if v := num(t, got, "sign_in_checks", "total"); v != 1 {
		t.Fatalf("sign_in_checks.total = %v; site B's attempts have leaked into site A", v)
	}
	if v := num(t, got, "sign_in_checks", "verified"); v != 1 {
		t.Fatalf("sign_in_checks.verified = %v; want 1", v)
	}

	// ...and the other direction, so the test cannot pass by the endpoint simply returning zeros.
	other := b.dashboard(t)
	if v := num(t, other, "occupancy", "in_house"); v != 2 {
		t.Fatalf("site B's own dashboard shows in_house = %v; want its own 2", v)
	}
	if v := num(t, other, "pms", "events_today"); v != 2 {
		t.Fatalf("site B's own dashboard shows events_today = %v; want its own 2", v)
	}
}

// Guest networks are read from public.guest_networks, which is the one table in this snapshot that is NOT in the
// iam_v2 schema. It gets its own assertion for that reason: a missing site term there leaks the estate's network
// topology, which is a different and more sensitive disclosure than a count.
func TestIntegration_API_DashboardNetworksAreSiteConfined(t *testing.T) {
	a := newAPI(t)
	seedSiteActivity(t, a, "301")
	b := newAPIIn(t, a.tenant)
	seedSiteActivity(t, b, "302")

	nets, _ := a.dashboard(t)["networks"].([]any)
	if len(nets) != 1 {
		t.Fatalf("networks has %d entries; another site's guest networks are visible", len(nets))
	}
	name, _ := nets[0].(map[string]any)["name"].(string)
	if name != "Guest VLAN 301" {
		t.Fatalf("networks[0].name = %q; the wrong site's network is being reported", name)
	}
}

// The package block joins four tables and is the easiest place for a scope term to be forgotten, because the
// outermost WHERE looks scoped while the two sub-selects are where the counting actually happens.
func TestIntegration_API_DashboardPackagesAreSiteConfined(t *testing.T) {
	a := newAPI(t)
	b := newAPIIn(t, a.tenant)
	// is_system packages are excluded from the operator catalogue, so seed non-system ones here.
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

// UNAUTHENTICATED IS 401, NOT AN EMPTY SNAPSHOT. A read surface that answers 200 with zeros to an anonymous
// caller looks healthy in a browser and is an unauthenticated disclosure of the property's operational shape.
func TestIntegration_API_DashboardRefusesAnonymousCallers(t *testing.T) {
	f := newAPI(t)
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/edge/v1/reports/dashboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately NO session cookie.
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard read answered %d; want 401", resp.StatusCode)
	}
}

// A role with no claim on `reports` is refused by the same middleware that gates every other resource. This is
// the assertion that fails if the route is ever moved out of mountResource onto a bare router.
func TestIntegration_API_DashboardRequiresTheReportsPermission(t *testing.T) {
	// `no_such_role` is in no row of rolePerms, so permFor answers false for every resource. It stands in for
	// any future role that has not been granted reports.
	f := newAPI(t, "no_such_role")
	code, _ := f.do(t, http.MethodGet, "/reports/dashboard", nil)
	if code != http.StatusForbidden {
		t.Fatalf("a role with no reports claim read the dashboard with status %d; want 403", code)
	}

	// ...and a role that legitimately holds reports:read does get it, so the refusal above is the permission
	// check and not a broken route.
	v := newAPI(t, "site_viewer")
	if code, _ := v.do(t, http.MethodGet, "/reports/dashboard", nil); code != http.StatusOK {
		t.Fatalf("site_viewer could not read the dashboard: status %d", code)
	}
}

// ---------------------------------------------------------------------------------------------------------
// NETWORK ROUTING — the one write this delivery added.
// ---------------------------------------------------------------------------------------------------------

func (f *apiFixture) seedNet(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO public.guest_networks(id,tenant_id,site_id,name,enabled)
		VALUES (gen_random_uuid(),$1,$2,$3,true) RETURNING id::text`, f.tenant, f.site, name).Scan(&id); err != nil {
		t.Fatalf("seed guest network %q: %v", name, err)
	}
	return id
}

// WRITE IS site_admin ONLY, and that is enforced by the server rather than by the button being hidden.
//
// edged's rolePerms gives `pms-routing` permRead to hotel_it_manager, the two desk roles and the viewer, and
// permWrite to nobody but site_admin. The UI hides the controls for those roles; this proves the API refuses them
// even when the UI is bypassed, which is the only version of that claim that means anything.
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
			// ...and the read they DO hold still works, so the refusals above are about the method and not
			// about the resource being unreachable for them.
			if code, _ := f.do(t, http.MethodGet, "/pms-routing", nil); code != http.StatusOK {
				t.Fatalf("%s could not read the routing list: status %d", role, code)
			}
		})
	}
}

// The happy path, with the mapping read back through the API rather than out of the table — a write that lands
// somewhere the read cannot see is not a working feature.
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

// A PMS interface belonging to ANOTHER SITE of the same customer must be refused. Accepting it would point one
// property's Wi-Fi at a different property's guest list — the exact failure the routing screen warns about, and
// one that reports healthy on every other surface.
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

// A guest network belonging to another site cannot be mapped either, from the other direction.
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
