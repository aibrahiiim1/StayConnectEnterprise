//go:build integration

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func (f *apiFixture) mkNet(t *testing.T, name string, vlan int, third string) string {
	t.Helper()
	st, body := f.do(t, "POST", "/network/guest-networks", netBody(name, "vlan", uniqueParent(), vlan, "10."+third+".0/24", "10."+third+".1", "10."+third+".100", "10."+third+".200"))
	if st != 201 {
		t.Fatalf("create %s = %d %v", name, st, body)
	}
	return body["id"].(string)
}

func (f *apiFixture) nameOf(t *testing.T, id string) string {
	t.Helper()
	var n string
	if err := f.pool.QueryRow(context.Background(), `SELECT name FROM guest_networks WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *apiFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *apiFixture) applyAndKeep(t *testing.T, fn *fakeNetd) {
	t.Helper()
	if st, b := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, b)
	}
	if st, b := f.confirmRev(t, fn.revision); st != 200 {
		t.Fatalf("confirm = %d %v", st, b)
	}
}

func actionOf(plan map[string]any, id string) string {
	for _, n := range plan["networks"].([]any) {
		m := n.(map[string]any)
		if m["id"] == id {
			return m["action"].(string)
		}
	}
	return ""
}

func reviewState(plan map[string]any, network, layer string) string {
	for _, n := range plan["review"].([]any) {
		m := n.(map[string]any)
		if m["network_id"] == network && m["layer"] == layer {
			return m["state"].(string)
		}
	}
	return ""
}

func TestIntegration_Recovery_RestoreLastWorkingIsStagedAppliedAndPutBackByARollback(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	ctx := context.Background()

	a := f.mkNet(t, "Rooms", 171, "171.0")
	f.applyAndKeep(t, fn)
	profile := f.newProfile(t, "Pool", "default")

	// After the last confirmation: A is renamed, a second network is switched on, A is given a portal profile, and the snapshot is made to remember a PMS interface
	// A was routed to then.
	if st, b := f.do(t, "PUT", "/network/guest-networks/"+a, map[string]any{"name": "Drifted", "lease_default_seconds": 1800}); st != 200 {
		t.Fatalf("edit = %d %v", st, b)
	}
	b := f.mkNet(t, "Extra", 172, "172.0")
	if st, _ := f.do(t, "PUT", "/portal-branding/profiles/assignments/"+a, map[string]any{"profile_id": profile}); st != 200 {
		t.Fatal("assign")
	}
	ghost := "99999999-9999-4999-8999-999999999999"
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.network_layer_snapshots SET layers = jsonb_set(layers, ARRAY[$1::text,'pms_interfaces'], to_jsonb(ARRAY[$2::text])) WHERE tenant_id=$3::uuid`, a, ghost, f.tenant); err != nil {
		t.Fatal(err)
	}

	st, plan := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "restore_last_working"})
	if st != 200 {
		t.Fatalf("preview = %d %v", st, plan)
	}
	if actionOf(plan, a) != "restore_settings" || actionOf(plan, b) != "switch_off" {
		t.Fatalf("plan: %v", plan["networks"])
	}
	if reviewState(plan, a, "portal_profile") != "CHANGED" || reviewState(plan, a, "pms_routing") != "CHANGED" || reviewState(plan, a, "signin") != "UNCHANGED" {
		t.Fatalf("layers: %v", plan["review"])
	}
	if f.nameOf(t, a) != "Drifted" || !f.enabledOf(t, b) {
		t.Fatal("previewing changed something")
	}

	st, staged := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "restore_last_working"})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, staged)
	}
	if f.nameOf(t, a) != "Drifted" || !f.enabledOf(t, b) || f.count(t, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, a) != 1 {
		t.Fatal("staging changed something before any apply")
	}

	if st, ap := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, ap)
	}
	if f.nameOf(t, a) != "Rooms" || f.enabledOf(t, b) || f.count(t, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, a) != 0 {
		t.Fatal("the apply did not restore the last working configuration")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.network_review_items WHERE network_id=$1::uuid AND resolved_at IS NULL`, a) != 2 {
		t.Fatal("the restore left nothing to review")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid AND 'pms'=ANY(disabled_methods)`, a) != 1 {
		t.Fatal("room sign-in was not paused on the network whose PMS routing differs")
	}

	// A rollback undoes all of it, including the review and the pause.
	if st, rb := f.rollbackRev(t, fn.revision); st != 200 {
		t.Fatalf("rollback = %d %v", st, rb)
	}
	f.do(t, "GET", "/network/changes", nil)
	if f.nameOf(t, a) != "Drifted" || !f.enabledOf(t, b) || f.count(t, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, a) != 1 {
		t.Fatal("a rollback did not put the settings back")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.network_review_items WHERE network_id=$1::uuid`, a) != 0 || f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, a) != 0 {
		t.Fatal("a rollback left the review items or the pause behind")
	}
	if got := f.changeStates(t, a)[a]; got != "SETTINGS/PENDING" {
		t.Fatalf("after the rollback the change is %s", got)
	}

	// Apply again and keep it; acknowledging the PMS item lifts the pause.
	f.applyAndKeep(t, fn)
	if f.nameOf(t, a) != "Rooms" {
		t.Fatal("second apply")
	}
	_, items := f.do(t, "GET", "/network/recovery/review", nil)
	var pmsItem string
	for _, i := range items["data"].([]any) {
		if m := i.(map[string]any); m["layer"] == "pms_routing" {
			pmsItem = m["id"].(string)
			if m["room_sign_in_paused"] != true {
				t.Fatalf("the PMS item does not say room sign-in is paused: %v", m)
			}
		}
	}
	if pmsItem == "" {
		t.Fatalf("no PMS review item: %v", items)
	}
	if st, _ := f.do(t, "POST", "/network/recovery/review/"+pmsItem+"/acknowledge", map[string]any{}); st != 200 {
		t.Fatal("acknowledge")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, a) != 0 {
		t.Fatal("acknowledging did not lift the pause the restore had added")
	}
}

func TestIntegration_Recovery_AnAcknowledgementNeverLiftsAPauseTheOperatorSet(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	ctx := context.Background()
	a := f.mkNet(t, "Rooms", 173, "173.0")
	f.applyAndKeep(t, fn)
	if st, _ := f.do(t, "PUT", "/network/guest-networks/"+a+"/sign-in", map[string]any{"disabled_methods": []string{"pms"}}); st != 200 {
		t.Fatal("operator pause")
	}
	_, _ = f.pool.Exec(ctx, `UPDATE iam_v2.network_layer_snapshots SET layers = jsonb_set(layers, ARRAY[$1::text,'pms_interfaces'], to_jsonb(ARRAY['99999999-9999-4999-8999-999999999999'::text])) WHERE tenant_id=$2::uuid`, a, f.tenant)
	// The snapshot also recorded no override, so the operator's own pause is a "change" and is put back: use a different drift for the layer under test.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.network_layer_snapshots SET layers = jsonb_set(layers, ARRAY[$1::text,'signin'], (SELECT to_jsonb(o) FROM (SELECT ARRAY['pms']::text[] AS disabled_methods, ''::text AS primary_method, NULL::int AS remember_days) o)) WHERE tenant_id=$2::uuid`, a, f.tenant); err != nil {
		t.Fatal(err)
	}
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "restore_last_working"}); st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	_, items := f.do(t, "GET", "/network/recovery/review", nil)
	for _, i := range items["data"].([]any) {
		if m := i.(map[string]any); m["layer"] == "pms_routing" {
			if m["room_sign_in_paused"] != false {
				t.Fatalf("the restore claimed a pause the operator had set: %v", m)
			}
			f.do(t, "POST", "/network/recovery/review/"+m["id"].(string)+"/acknowledge", map[string]any{})
		}
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid AND 'pms'=ANY(disabled_methods)`, a) != 1 {
		t.Fatal("acknowledging lifted the operator's own pause")
	}
}

func TestIntegration_Recovery_NothingConfirmedMeansNothingToRestore(t *testing.T) {
	f := newAPI(t)
	startFakeNetd(t, f)
	f.retireRevisions(t)
	_, plan := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "restore_last_working"})
	if cannot := plan["cannot_restore"].([]any); len(cannot) == 0 {
		t.Fatalf("a plan was offered with no confirmed configuration: %v", plan)
	}
	if st, _ := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "restore_last_working"}); st != 409 {
		t.Fatalf("stage with nothing to restore = %d", st)
	}
	if st, _ := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "bogus"}); st != 400 {
		t.Fatalf("unknown operation = %d", st)
	}
}

func TestIntegration_Recovery_FactoryResetKeepsEveryNetworkAndNeedsTheSiteName(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	ctx := context.Background()

	// A site whose prepared network was never activated can still be returned to it: the network is made and switched on in the same change.
	_, plan := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "factory_defaults"})
	if plan["stageable"] != true || len(plan["cannot_restore"].([]any)) != 0 || actionOf(plan, "") != "create_and_switch_on" {
		t.Fatalf("a factory restore before any activation: %v", plan)
	}

	if st, b := f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true}); st != 201 {
		t.Fatalf("activate = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	var factory string
	_ = f.pool.QueryRow(ctx, `SELECT id::text FROM guest_networks WHERE tenant_id=$1 AND name='Client Wi-Fi'`, f.tenant).Scan(&factory)

	other := f.mkNet(t, "Conference", 181, "181.0")
	f.applyAndKeep(t, fn)
	profile := f.newProfile(t, "Conf", "default")
	f.do(t, "PUT", "/portal-branding/profiles/assignments/"+other, map[string]any{"profile_id": profile})
	f.do(t, "PUT", "/network/guest-networks/"+other+"/sign-in", map[string]any{"disabled_methods": []string{"voucher"}})
	// The factory network was edited since.
	if st, b := f.do(t, "PUT", "/network/guest-networks/"+factory, map[string]any{"name": "Renamed"}); st != 200 {
		t.Fatalf("edit factory = %d %v", st, b)
	}

	networks := f.networkCount(t)
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset"}); st != 400 {
		t.Fatalf("a full reset without the site name = %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": "not the name"}); st != 400 {
		t.Fatalf("a full reset with the wrong name = %d %v", st, b)
	}
	var siteName string
	siteName = f.app.siteDisplayName(ctx)
	_, pv := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "full_reset"})
	if pv["confirm_site_name"] != siteName || len(pv["protected"].([]any)) == 0 {
		t.Fatalf("preview: %v", pv)
	}
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": siteName}); st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	if f.nameOf(t, factory) != "Renamed" || !f.enabledOf(t, other) {
		t.Fatal("staging changed something before any apply")
	}
	f.applyAndKeep(t, fn)

	if f.nameOf(t, factory) != "Client Wi-Fi" || !f.enabledOf(t, factory) {
		t.Fatal("the prepared network was not put back")
	}
	if f.enabledOf(t, other) {
		t.Fatal("another network is still on after a full reset")
	}
	if f.networkCount(t) != networks {
		t.Fatal("a reset deleted or created a network")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, other) != 0 ||
		f.count(t, `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, other) != 0 {
		t.Fatal("a full reset left the layered settings")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.portal_profiles WHERE id=$1::uuid`, profile) != 1 {
		t.Fatal("a reset deleted a portal design")
	}
	var subnet string
	_ = f.pool.QueryRow(ctx, `SELECT text(subnet_cidr) FROM guest_networks WHERE id=$1`, factory).Scan(&subnet)
	if subnet != "10.20.0.0/20" {
		t.Fatalf("the factory network is at %s", subnet)
	}
}

func TestIntegration_Recovery_NeverForcesTheFactoryAddressOverTheWAN(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	if st, _ := f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true}); st != 201 {
		t.Fatal("activate")
	}
	f.applyAndKeep(t, fn)
	fn.wanIP, fn.wanBits = "10.20.3.9", 24
	st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "factory_defaults"})
	if st != 409 {
		t.Fatalf("a factory restore over the WAN network = %d %v", st, b)
	}
	raw, _ := json.Marshal(b)
	if !strings.Contains(string(raw), "never forced") {
		t.Fatalf("the refusal does not say why: %s", raw)
	}
}

// A recovery that cannot be done completely is not done at all: part of a restore is a configuration nobody confirmed.
func TestIntegration_Recovery_ARestoreThatCannotBeCompleteStagesNothing(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	ctx := context.Background()

	a := f.mkNet(t, "Rooms", 191, "191.0")
	gone := f.mkNet(t, "Gone", 192, "192.0")
	moved := f.mkNet(t, "Moved", 193, "193.0")
	f.applyAndKeep(t, fn)

	// After the confirmation: A drifts (restorable), one network is deleted, one is moved to another VLAN.
	if st, b := f.do(t, "PUT", "/network/guest-networks/"+a, map[string]any{"name": "Drifted"}); st != 200 {
		t.Fatalf("edit = %d %v", st, b)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE guest_networks SET vlan_id=299 WHERE id=$1::uuid`, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM guest_networks WHERE id=$1::uuid`, gone); err != nil {
		t.Fatal(err)
	}

	st, plan := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "restore_last_working"})
	if st != 200 || plan["stageable"] != false || len(plan["cannot_restore"].([]any)) != 2 {
		t.Fatalf("preview = %d %v", st, plan)
	}
	if actionOf(plan, a) != "restore_settings" {
		t.Fatalf("the preview should still show what could be restored: %v", plan["networks"])
	}
	before := f.count(t, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid`, f.tenant)
	st, body := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "restore_last_working"})
	if st != 409 || body["error"] != "cannot_restore" || !strings.Contains(body["message"].(string), "Nothing was staged") {
		t.Fatalf("a partial restore was not refused: %d %v", st, body)
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid`, f.tenant) != before {
		t.Fatal("a refused restore left staged changes behind")
	}
	if f.nameOf(t, a) != "Drifted" {
		t.Fatal("a refused restore changed a network")
	}
}

// Two untagged networks cannot be enabled on one port at once, so backing out a swap has to switch the new one off before the old one comes back.
func TestIntegration_Recovery_BackingOutASwapOfTwoUntaggedNetworksRestoresTheOriginal(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	ctx := context.Background()
	port := uniqueParent()
	mk := func(name, third string) string {
		parent := port
		if name != "Original" {
			parent = uniqueParent() + "r"
		}
		st, body := f.do(t, "POST", "/network/guest-networks", netBody(name, "untagged", parent, 0, "10."+third+".0/24", "10."+third+".1", "10."+third+".100", "10."+third+".200"))
		if st != 201 {
			t.Fatalf("create %s = %d %v", name, st, body)
		}
		return body["id"].(string)
	}
	x := mk("Original", "201.0")
	y := mk("Replacement", "202.0")
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+y+"/disable", nil); st != 200 {
		t.Fatal("disable")
	}
	// Both sit on one port; only one of them may be enabled at a time.
	if _, err := f.pool.Exec(ctx, `UPDATE guest_networks SET parent_interface=$2 WHERE id=$1::uuid`, y, port); err != nil {
		t.Fatal(err)
	}
	f.applyAndKeep(t, fn)

	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.stageChanges(ctx, tx, "RESTORE", []changeItem{{x, changeDisable}, {y, changeEnable}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if st, b := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, b)
	}
	if f.enabledOf(t, x) || !f.enabledOf(t, y) {
		t.Fatal("the swap did not apply")
	}
	if st, b := f.rollbackRev(t, fn.revision); st != 200 {
		t.Fatalf("rollback = %d %v", st, b)
	}
	f.do(t, "GET", "/network/changes", nil)
	if !f.enabledOf(t, x) || f.enabledOf(t, y) {
		t.Fatalf("after backing out, original enabled=%v replacement enabled=%v", f.enabledOf(t, x), f.enabledOf(t, y))
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid AND state='PENDING'`, f.tenant) != 2 {
		t.Fatal("the swap is not staged again, in full")
	}
}

// What the recovery code reads and writes must be granted to the Admin Console's own database role: the integration database is usually reached as a superuser,
// which hides a missing grant until the appliance is used (the site name was once read from a table that role cannot see).
func TestIntegration_Recovery_TheAdminConsoleRoleHoldsEveryPrivilegeTheRecoveryUses(t *testing.T) {
	f := newAPI(t)
	need := map[string][]string{
		"public.guest_networks": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "public.dhcp_pools": {"SELECT", "INSERT", "DELETE"},
		"public.network_config_revisions": {"SELECT"}, "public.tenants": {"SELECT"}, "iam_v2.sessions": {"SELECT"}, "iam_v2.guest_network_pms_map": {"SELECT"},
		"iam_v2.guest_network_changes": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "iam_v2.portal_profiles": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"iam_v2.portal_profile_assignments": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "iam_v2.network_signin_overrides": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"iam_v2.network_layer_snapshots": {"SELECT", "INSERT", "UPDATE"}, "iam_v2.network_review_items": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"iam_v2.network_archive": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "iam_v2.internet_packages": {"SELECT"},
		"iam_v2.pms_interfaces": {"SELECT"}, "iam_v2.auth_resolutions": {"SELECT"}, "iam_v2.device_network_appearances": {"SELECT"},
	}
	for table, privs := range need {
		for _, p := range privs {
			var ok bool
			if err := f.pool.QueryRow(context.Background(), `SELECT has_table_privilege('svc_edged', $1, $2)`, table, p).Scan(&ok); err != nil || !ok {
				t.Errorf("svc_edged lacks %s on %s (%v)", p, table, err)
			}
		}
	}
}
