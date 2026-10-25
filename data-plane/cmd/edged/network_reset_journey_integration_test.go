//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
)

type heavySite struct {
	lan          string
	oldFactory   string
	vlans        []string
	conference   string
	profile      string
	siteName     string
	networkCount int
}

// heavilyReconfigured is a site that was activated and then changed beyond recognition: the client port is a trunk with tagged VLANs, the prepared network has
// been turned into a VLAN, another network carries its own portal design and sign-in options.
func heavilyReconfigured(t *testing.T, f *apiFixture, fn *fakeNetd) heavySite {
	t.Helper()
	ctx := context.Background()
	h := heavySite{lan: uniqueParent()}
	fn.lan = h.lan
	f.retireRevisions(t)

	if st, b := f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true}); st != 201 {
		t.Fatalf("activate = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	_ = f.pool.QueryRow(ctx, `SELECT id::text FROM guest_networks WHERE tenant_id=$1 AND name='Client Wi-Fi'`, f.tenant).Scan(&h.oldFactory)

	st, body := f.do(t, "POST", "/network/ports/"+h.lan+"/mode", map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 60, "name": "Rooms"}, {"vlan_id": 90, "name": "Conference"}}})
	if st != 201 {
		t.Fatalf("trunk = %d %v", st, body)
	}
	h.vlans = toStrings(body["network_ids"])
	f.applyAndKeep(t, fn)
	h.conference = h.vlans[1]

	// The prepared network is turned into a tagged VLAN network on the same port.
	if _, err := f.pool.Exec(ctx, `UPDATE guest_networks SET network_type='vlan', vlan_id=100, name='Old guests', subnet_cidr='10.50.0.0/24', gateway_ip='10.50.0.1', gateway_cidr='10.50.0.1/24' WHERE id=$1::uuid`, h.oldFactory); err != nil {
		t.Fatal(err)
	}
	h.profile = f.newProfile(t, "Conference", "default")
	f.do(t, "PUT", "/portal-branding/profiles/assignments/"+h.conference, map[string]any{"profile_id": h.profile})
	f.do(t, "PUT", "/network/guest-networks/"+h.conference+"/sign-in", map[string]any{"disabled_methods": []string{"voucher"}})
	_ = f.pool.QueryRow(ctx, `SELECT name FROM sites WHERE id=$1`, f.site).Scan(&h.siteName)
	h.networkCount = f.networkCount(t)
	return h
}

func (f *apiFixture) enabledNames(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT name FROM guest_networks WHERE tenant_id=$1 AND enabled ORDER BY name`, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func TestIntegration_Reset_AHeavilyReconfiguredLANComesBackToTheApprovedAccessNetwork(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	ctx := context.Background()

	// Before: a trunk with tagged networks.
	_, ports := f.do(t, "GET", "/network/ports", nil)
	for _, p := range ports["ports"].([]any) {
		if pm := p.(map[string]any); pm["name"] == h.lan && pm["mode"] != "trunk" {
			t.Fatalf("the fixture is not a trunk: %v", pm)
		}
	}

	st, plan := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "full_reset"})
	if st != 200 || plan["stageable"] != true {
		t.Fatalf("preview = %d %v", st, plan)
	}
	if actionOf(plan, "") != "create_and_switch_on" || actionOf(plan, h.conference) != "switch_off" || actionOf(plan, h.oldFactory) != "switch_off" {
		t.Fatalf("plan: %v", plan["networks"])
	}
	if f.enabledNames(t)[0] == "" || f.networkCount(t) != h.networkCount {
		t.Fatal("previewing changed something")
	}
	if st, _ := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset"}); st != 400 {
		t.Fatalf("a full reset without the site name = %d", st)
	}
	st, staged := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, staged)
	}
	// Staging adds the prepared network switched off, and changes nothing else.
	if f.networkCount(t) != h.networkCount+1 || len(f.enabledNames(t)) != 3 {
		t.Fatalf("staging changed the running networks: %v", f.enabledNames(t))
	}
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "factory_defaults"}); st != 409 || b["error"] != "recovery_in_progress" {
		t.Fatalf("a second recovery while one is staged = %d %v", st, b)
	}

	f.applyAndKeep(t, fn)

	if names := f.enabledNames(t); len(names) != 1 || names[0] != "Client Wi-Fi" {
		t.Fatalf("after the reset the enabled networks are %v", names)
	}
	var typ, parent, subnet, gw, dhcp, pool string
	if err := f.pool.QueryRow(ctx, `SELECT g.network_type, g.parent_interface, text(g.subnet_cidr), host(g.gateway_ip), g.dhcp_mode,
		(SELECT host(start_ip)||'-'||host(end_ip) FROM dhcp_pools WHERE guest_network_id=g.id)
		FROM guest_networks g WHERE g.tenant_id=$1 AND g.enabled`, f.tenant).Scan(&typ, &parent, &subnet, &gw, &dhcp, &pool); err != nil {
		t.Fatal(err)
	}
	if typ != "untagged" || parent != h.lan || subnet != "10.20.0.0/20" || gw != "10.20.0.1" || dhcp != "local" || pool != "10.20.0.10-10.20.15.254" {
		t.Fatalf("the prepared network is %s on %s, %s via %s, DHCP %s, pool %s", typ, parent, subnet, gw, dhcp, pool)
	}
	_, ports = f.do(t, "GET", "/network/ports", nil)
	for _, p := range ports["ports"].([]any) {
		if pm := p.(map[string]any); pm["name"] == h.lan && pm["mode"] != "access" {
			t.Fatalf("the client port is %v, want access", pm["mode"])
		}
	}

	// History and identity stay: nothing was deleted, the old networks are switched off and keep their settings, and the portal design is untouched.
	if f.networkCount(t) != h.networkCount+1 {
		t.Fatal("a reset deleted a network")
	}
	var vlan int
	_ = f.pool.QueryRow(ctx, `SELECT COALESCE(vlan_id,0) FROM guest_networks WHERE id=$1::uuid`, h.oldFactory).Scan(&vlan)
	if vlan != 100 || f.enabledOf(t, h.oldFactory) {
		t.Fatal("the old prepared network was not left switched off as it was")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, h.conference) != 0 ||
		f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, h.conference) != 0 ||
		f.count(t, `SELECT count(*) FROM iam_v2.portal_profiles WHERE id=$1::uuid`, h.profile) != 1 {
		t.Fatal("the layered settings were not reset, or a portal design was deleted")
	}

	// Done means done: a second reset has nothing to change, and the new network is the prepared network from now on.
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "factory_defaults"}); st != 409 || b["error"] != "nothing_to_do" {
		t.Fatalf("a second reset = %d %v", st, b)
	}
}

// A reset whose apply does not hold, or whose service stops half way, leaves the old configuration running and the reset staged again.
func TestIntegration_Reset_AnInterruptedApplyPutsTheHeavyConfigurationBack(t *testing.T) {
	for _, how := range []string{"confirmation lapses", "service stopped before recording the apply"} {
		t.Run(how, func(t *testing.T) {
			f := newAPI(t)
			fn := startFakeNetd(t, f)
			h := heavilyReconfigured(t, f, fn)
			ctx := context.Background()
			before := f.enabledNames(t)

			if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
				t.Fatalf("stage = %d %v", st, b)
			}
			if st, b := f.apply(t); st != 200 {
				t.Fatalf("apply = %d %v", st, b)
			}
			if names := f.enabledNames(t); len(names) != 1 {
				t.Fatalf("the apply did not switch the networks: %v", names)
			}
			// The appliance's own watchdog or boot reclaim ends the unconfirmed revision either way; what differs is whether this side ever recorded the apply.
			fn.timeOut(t)
			if how != "confirmation lapses" {
				if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.guest_network_changes SET revision_id=NULL, applied_at=now()-interval '2 hours' WHERE tenant_id=$1::uuid AND state='APPLIED'`, f.tenant); err != nil {
					t.Fatal(err)
				}
			}
			f.do(t, "GET", "/network/changes", nil)

			if got := f.enabledNames(t); strings.Join(got, ",") != strings.Join(before, ",") {
				t.Fatalf("after the interruption the enabled networks are %v, want %v", got, before)
			}
			if f.count(t, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, h.conference) != 1 ||
				f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, h.conference) != 1 {
				t.Fatal("the layered settings were not put back")
			}
			if f.count(t, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid AND state='PENDING' AND group_kind='FULL_RESET'`, f.tenant) == 0 {
				t.Fatal("the reset is not staged again")
			}

			// And it can be tried again and kept.
			f.applyAndKeep(t, fn)
			if names := f.enabledNames(t); len(names) != 1 || names[0] != "Client Wi-Fi" {
				t.Fatalf("the second attempt left %v", names)
			}
		})
	}
}

func TestIntegration_Reset_CancellingARestagedResetRemovesTheNetworkItMade(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	if st, _ := f.do(t, "DELETE", "/network/changes/"+b["group_id"].(string), nil); st != 200 {
		t.Fatalf("cancel = %d", st)
	}
	if f.networkCount(t) != h.networkCount {
		t.Fatal("cancelling left the prepared network the reset had made")
	}
	if len(f.enabledNames(t)) != 3 {
		t.Fatalf("cancelling changed the running networks: %v", f.enabledNames(t))
	}
	// A cancelled reset can be staged again.
	if st, _ := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatal("re-stage")
	}
}

func TestIntegration_Reset_NeverForcesAGenuineConflictOrTheManagementPort(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	enabled := f.enabledNames(t)
	changes := f.count(t, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid`, f.tenant)

	fn.wanIP, fn.wanBits = "10.20.8.9", 24
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 409 || !strings.Contains(b["message"].(string), "never forced") {
		t.Fatalf("a WAN overlap = %d %v", st, b)
	}
	fn.wanIP, fn.wanBits = "172.21.60.25", 24
	fn.lan = "ens160"
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 409 || !strings.Contains(b["message"].(string), "WAN/management port") {
		t.Fatalf("the management port as the client port = %d %v", st, b)
	}
	if f.networkCount(t) != h.networkCount || strings.Join(f.enabledNames(t), ",") != strings.Join(enabled, ",") ||
		f.count(t, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid`, f.tenant) != changes {
		t.Fatal("a refused reset changed something")
	}
}
