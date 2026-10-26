//go:build integration

package main

import (
	"context"
	"net/http"
	"testing"
)

func stayRows(t *testing.T, b map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, x := range b["data"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

// The same room number at two independent PMS connections is two rooms. Nothing treats it as a duplicate, and the page can look at one connection or at both together.
func TestIntegration_API_TheSameRoomNumberAtTwoPMSConnectionsIsTwoRooms(t *testing.T) {
	f := newAPI(t)
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	f.seedStay(t, a, "RES-A-1", "101", "")
	f.seedStay(t, b, "RES-B-1", "101", "")
	f.seedStay(t, b, "RES-B-2", "102", "")

	st, all := f.do(t, http.MethodGet, "/pms-stays?status=IN_HOUSE&page_size=200", nil)
	if st != http.StatusOK {
		t.Fatalf("stays = %d %v", st, all)
	}
	room101 := 0
	owners := map[string]bool{}
	for _, r := range stayRows(t, all) {
		if r["room"] == "101" {
			room101++
			owners[r["pms_interface_id"].(string)] = true
			if r["pms_interface_label"] == nil || r["pms_interface_label"] == "" {
				t.Fatalf("a stay does not name the connection it belongs to: %v", r)
			}
		}
	}
	if room101 < 2 || !owners[a] || !owners[b] {
		t.Fatalf("room 101 appears %d times under %d connections, want one per connection", room101, len(owners))
	}

	st, only := f.do(t, http.MethodGet, "/pms-stays?status=IN_HOUSE&pms_interface_id="+b+"&page_size=200", nil)
	if st != http.StatusOK {
		t.Fatalf("filtered stays = %d %v", st, only)
	}
	rows := stayRows(t, only)
	if len(rows) != 2 {
		t.Fatalf("one connection's stays = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r["pms_interface_id"] != b {
			t.Fatalf("the connection filter let another connection's stay through: %v", r)
		}
	}

	by := map[string]float64{}
	for _, x := range only["by_interface"].([]any) {
		m := x.(map[string]any)
		by[m["pms_interface_id"].(string)] = m["total"].(float64)
	}
	shell := f.shell(t, "Never configured")
	_, again := f.do(t, http.MethodGet, "/pms-stays?status=IN_HOUSE&page_size=1", nil)
	for _, x := range again["by_interface"].([]any) {
		if x.(map[string]any)["pms_interface_id"] == shell {
			t.Fatal("a connection that was never configured is offered as a stays tab")
		}
	}
	if by[a] != 1 || by[b] != 2 {
		t.Fatalf("the connection tabs must count every connection under the same filters, ignoring the connection filter: %v", by)
	}

	if st, b2 := f.do(t, http.MethodGet, "/pms-stays?pms_interface_id=not-a-uuid", nil); st != http.StatusBadRequest {
		t.Fatalf("a malformed connection id = %d %v, want 400", st, b2)
	}
}

func TestIntegration_API_ReconciliationRowsNameTheirConnection(t *testing.T) {
	f := newAPI(t)
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	// Two stays in room 201 at each connection: sharing is ordinary, and the two rooms are different rooms.
	for _, x := range []struct{ iface, res string }{{a, "A-1"}, {a, "A-2"}, {b, "B-1"}, {b, "B-2"}} {
		f.seedStay(t, x.iface, "RES-"+x.res, "201", "")
	}
	st, body := f.do(t, http.MethodGet, "/pms-reconciliation/rooms", nil)
	if st != http.StatusOK {
		t.Fatalf("rooms = %d %v", st, body)
	}
	seen := map[string]bool{}
	for _, x := range body["rooms"].([]any) {
		m := x.(map[string]any)
		if m["room"] == "201" {
			seen[m["pms_interface_id"].(string)] = true
			if m["pms_interface_label"] == "" {
				t.Fatalf("a multi-occupancy row has no connection label: %v", m)
			}
		}
	}
	if !seen[a] || !seen[b] {
		t.Fatalf("room 201 must appear once per connection, got %v", seen)
	}
}

func TestIntegration_API_ClientNetworksHaveOneServiceState(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	serving := f.seedNet(t, "State serving")
	off := f.seedNet(t, "State off")
	archived := f.seedNet(t, "State archived")
	for _, id := range []string{off, archived} {
		if _, err := f.pool.Exec(ctx, `UPDATE public.guest_networks SET enabled=false WHERE id=$1::uuid`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.network_archive(network_id,tenant_id,site_id,group_kind) VALUES ($1::uuid,$2::uuid,$3::uuid,'FACTORY_RESET')`, archived, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}

	_, list := f.do(t, http.MethodGet, "/network/guest-networks", nil)
	got := map[string]string{}
	for _, x := range list["data"].([]any) {
		m := x.(map[string]any)
		got[m["id"].(string)] = m["service_state"].(string)
	}
	if got[serving] != "serving" || got[off] != "off" || got[archived] != "archived" {
		t.Fatalf("list states = %v", got)
	}
	_, ov := f.do(t, http.MethodGet, "/network/overview", nil)
	for _, x := range ov["networks"].([]any) {
		m := x.(map[string]any)
		if m["id"] == archived && m["service_state"] != "archived" {
			t.Fatalf("the overview disagrees with the list: %v", m)
		}
	}

	_, prof := f.do(t, http.MethodGet, "/portal-branding/profiles", nil)
	states := map[string]string{}
	for _, x := range prof["networks"].([]any) {
		m := x.(map[string]any)
		states[m["id"].(string)] = m["service_state"].(string)
		if m["profile_id"] == nil || m["profile_id"] == "" {
			t.Fatalf("a network does not say which design it shows: %v", m)
		}
	}
	if states[serving] != "serving" || states[off] != "off" || states[archived] != "archived" {
		t.Fatalf("the portal design screen reads another state: %v", states)
	}

	_, dash := f.do(t, http.MethodGet, "/reports/dashboard", nil)
	onDash := map[string]bool{}
	for _, x := range dash["networks"].([]any) {
		onDash[x.(map[string]any)["name"].(string)] = true
	}
	if onDash["State archived"] {
		t.Fatal("an archived network is listed on the dashboard")
	}
	if !onDash["State serving"] || !onDash["State off"] {
		t.Fatalf("a serving or switched-off network is missing from the dashboard: %v", onDash)
	}
}

func TestIntegration_API_EveryNetworksSignInNarrowingInOneRead(t *testing.T) {
	f := newAPI(t)
	a := f.seedNet(t, "Narrow A")
	b := f.seedNet(t, "Narrow B")
	if st, r := f.do(t, http.MethodPut, "/network/guest-networks/"+a+"/sign-in", map[string]any{"disabled_methods": []string{"voucher", "sms"}, "primary_method": "guest_account"}); st != http.StatusOK {
		t.Fatalf("narrow = %d %v", st, r)
	}
	st, body := f.do(t, http.MethodGet, "/network/sign-in-overrides", nil)
	if st != http.StatusOK {
		t.Fatalf("overrides = %d %v", st, body)
	}
	ov := body["overrides"].(map[string]any)
	got, ok := ov[a].(map[string]any)
	if !ok || len(got["disabled_methods"].([]any)) != 2 || got["primary_method"] != "guest_account" {
		t.Fatalf("network A's narrowing = %v", ov[a])
	}
	if _, has := ov[b]; has {
		t.Fatal("a network that narrows nothing has no entry: it offers everything the site does")
	}
}
