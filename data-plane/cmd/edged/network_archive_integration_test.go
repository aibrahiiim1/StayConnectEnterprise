//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func (f *apiFixture) archivedCount(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM iam_v2.network_archive a JOIN guest_networks g ON g.id=a.network_id WHERE a.tenant_id=$1::uuid AND NOT g.enabled`, f.tenant)
}

func (f *apiFixture) latestRecoveryState(t *testing.T) string {
	t.Helper()
	_, b := f.do(t, "GET", "/network/recovery", nil)
	l, _ := b["latest"].(map[string]any)
	if l == nil {
		return ""
	}
	return l["state"].(string)
}

func (f *apiFixture) listFlags(t *testing.T) map[string][2]bool {
	t.Helper()
	_, b := f.do(t, "GET", "/network/guest-networks", nil)
	out := map[string][2]bool{}
	for _, n := range b["data"].([]any) {
		m := n.(map[string]any)
		out[m["name"].(string)+"|"+m["id"].(string)[:8]] = [2]bool{m["archived"] == true, m["is_default"] == true}
	}
	return out
}

// Archiving is part of CONFIRMING a reset and of nothing else: staged, applied, cancelled, rolled back or lapsed leave nothing archived.
func TestIntegration_Archive_OnlyAConfirmedResetArchivesAndEveryOtherEndLeavesNothing(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)

	st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	if f.archivedCount(t) != 0 || f.latestRecoveryState(t) != "STAGED" {
		t.Fatalf("staged: archived=%d state=%s", f.archivedCount(t), f.latestRecoveryState(t))
	}

	// Cancelled staging.
	if st, _ := f.do(t, "DELETE", "/network/changes/"+b["group_id"].(string), nil); st != 200 {
		t.Fatal("cancel")
	}
	if f.archivedCount(t) != 0 || f.latestRecoveryState(t) != "CANCELLED" {
		t.Fatalf("cancelled: archived=%d state=%s", f.archivedCount(t), f.latestRecoveryState(t))
	}

	// Applied, then the confirmation lapses.
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("restage = %d %v", st, b)
	}
	if st, ab := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, ab)
	}
	if f.archivedCount(t) != 0 || f.latestRecoveryState(t) != "APPLIED" {
		t.Fatalf("applied: archived=%d state=%s", f.archivedCount(t), f.latestRecoveryState(t))
	}
	fn.timeOut(t)
	// Reading the recovery status alone reconciles a lapsed apply: it must not report APPLIED.
	if f.latestRecoveryState(t) != "STAGED" {
		t.Fatalf("a lapsed recovery is reported as %s", f.latestRecoveryState(t))
	}
	if f.archivedCount(t) != 0 || f.latestRecoveryState(t) != "STAGED" {
		t.Fatalf("lapsed: archived=%d state=%s", f.archivedCount(t), f.latestRecoveryState(t))
	}
	for _, flags := range f.listFlags(t) {
		if flags[0] {
			t.Fatal("a network is archived after a lapsed reset")
		}
	}

	// Operator rollback of an applied reset.
	if st, ab := f.apply(t); st != 200 {
		t.Fatalf("apply 2 = %d %v", st, ab)
	}
	if st, rb := f.rollbackRev(t, fn.revision); st != 200 {
		t.Fatalf("rollback = %d %v", st, rb)
	}
	f.do(t, "GET", "/network/changes", nil)
	if f.archivedCount(t) != 0 {
		t.Fatal("a rolled-back reset left networks archived")
	}

	// Applied and kept: only now.
	f.applyAndKeep(t, fn)
	if f.latestRecoveryState(t) != "CONFIRMED" {
		t.Fatalf("confirmed: state=%s", f.latestRecoveryState(t))
	}
	if f.archivedCount(t) != 3 {
		t.Fatalf("a confirmed reset archived %d networks, want the 3 it retired", f.archivedCount(t))
	}
	flags := f.listFlags(t)
	archived, defaults := 0, 0
	for k, v := range flags {
		if v[0] {
			archived++
			if strings.HasPrefix(k, "Client Wi-Fi") {
				t.Fatal("the Default Network is archived")
			}
		}
		if v[1] {
			defaults++
			if !strings.HasPrefix(k, "Client Wi-Fi") {
				t.Fatalf("the Default Network is %s", k)
			}
		}
	}
	if archived != 3 || defaults != 1 {
		t.Fatalf("list flags: %d archived, %d default: %v", archived, defaults, flags)
	}
	_, ov := f.do(t, "GET", "/network/overview", nil)
	for _, n := range ov["networks"].([]any) {
		m := n.(map[string]any)
		if m["name"] == "Client Wi-Fi" && (m["is_default"] != true || m["archived"] == true) {
			t.Fatalf("overview: %v", m)
		}
	}
	_, ar := f.do(t, "GET", "/network/archive", nil)
	if ar["count"] != float64(3) {
		t.Fatalf("archive list: %v", ar)
	}
	// Nothing was deleted: every retired network is still there, with its history rows.
	if f.networkCount(t) != h.networkCount+1 {
		t.Fatal("archiving deleted a network")
	}
}

func TestIntegration_Archive_RestoreFromTheArchiveAndSwitchingOnLeaveNoStaleMarker(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	if f.archivedCount(t) != 3 {
		t.Fatal("setup")
	}

	if st, _ := f.do(t, "POST", "/network/guest-networks/"+h.vlans[0]+"/unarchive", nil); st != 200 {
		t.Fatal("restore from the archive")
	}
	if f.enabledOf(t, h.vlans[0]) || f.archivedCount(t) != 2 {
		t.Fatalf("restore left enabled=%v archived=%d; it must only take the marker off", f.enabledOf(t, h.vlans[0]), f.archivedCount(t))
	}
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+h.vlans[0]+"/unarchive", nil); st != 404 {
		t.Fatalf("restoring twice = %d", st)
	}
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+h.vlans[1]+"/enable", nil); st != 200 {
		t.Fatal("enable")
	}
	for k, v := range f.listFlags(t) {
		if strings.Contains(k, h.vlans[1][:8]) && v[0] {
			t.Fatal("a network that is switched on is still listed as archived")
		}
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.network_archive WHERE network_id=$1::uuid`, h.oldFactory) != 1 {
		t.Fatal("restoring another network disturbed this one's marker")
	}
}

// Factory defaults retires the same way; a Restore Last Working does not archive anything.
func TestIntegration_Archive_RestoreLastWorkingArchivesNothing(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)
	a := f.mkNet(t, "Rooms", 171, "171.0")
	f.applyAndKeep(t, fn)
	b := f.mkNet(t, "Extra", 172, "172.0")
	_ = b
	if st, body := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "restore_last_working"}); st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	f.applyAndKeep(t, fn)
	if f.enabledOf(t, b) || !f.enabledOf(t, a) {
		t.Fatal("the restore did not switch the new network off")
	}
	if f.archivedCount(t) != 0 {
		t.Fatal("a Restore Last Working archived a network")
	}
}

// The Default Network is described honestly. PMS routing and package eligibility are never copied, merged or widened for it.
func TestIntegration_Default_ReadinessNamesWhatIsUnresolvedAndChangesNothing(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	ctx := context.Background()

	ifaceA, _, _ := f.seedInterface(t)
	ifaceB, _, _ := f.seedInterface(t)
	if st, b := f.setRouting(t, h.vlans[0], "DEDICATED", ifaceA); st != 200 {
		t.Fatalf("route A = %d %v", st, b)
	}
	if st, b := f.setRouting(t, h.vlans[1], "DEDICATED", ifaceB); st != 200 {
		t.Fatalf("route B = %d %v", st, b)
	}
	f.app.commerce = f.commerceAdmin(t)
	pkg, rev := f.seedNetworkPackage(t, "pin-"+uniqueParent(), h.vlans[0], nil)
	_ = pkg

	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	var def string
	if err := f.pool.QueryRow(ctx, `SELECT id::text FROM guest_networks WHERE tenant_id=$1 AND enabled`, f.tenant).Scan(&def); err != nil {
		t.Fatal(err)
	}

	routesBefore := f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1::uuid`, f.tenant)
	rulesBefore := f.count(t, `SELECT count(*) FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1::uuid AND rule_type='SITE_NETWORK'`, rev)

	st, r := f.do(t, "GET", "/network/default", nil)
	if st != 200 || r["present"] != true || r["network_id"] != def {
		t.Fatalf("default = %d %v", st, r)
	}
	states := map[string]string{}
	for _, it := range r["items"].([]any) {
		m := it.(map[string]any)
		states[m["id"].(string)] = m["state"].(string)
	}
	for _, id := range []string{"dhcp", "dns", "portal", "signin"} {
		if states[id] != "ok" {
			t.Fatalf("%s = %s: %v", id, states[id], r["items"])
		}
	}
	if states["pms"] != "unresolved" || states["packages"] != "unresolved" {
		t.Fatalf("pms=%s packages=%s: %v", states["pms"], states["packages"], r["items"])
	}
	cands := r["pms_candidates"].([]any)
	if len(cands) != 2 {
		t.Fatalf("two independent interfaces must be two separate candidates: %v", cands)
	}
	for _, c := range cands {
		if len(c.(map[string]any)["routed_from"].([]any)) != 1 {
			t.Fatalf("a candidate merges networks: %v", c)
		}
	}
	pinned := r["packages_pinned_to_other_networks"].([]any)
	if len(pinned) != 1 {
		t.Fatalf("pinned packages: %v", pinned)
	}

	// The retired networks keep their routes in the routing list: PMS routing did not silently disappear. They are not reported as unrouted networks.
	_, routing := f.do(t, "GET", "/pms-routing", nil)
	raw, _ := json.Marshal(routing)
	if !strings.Contains(string(raw), h.vlans[0]) || !strings.Contains(string(raw), h.vlans[1]) {
		t.Fatalf("the archived networks' routes vanished from PMS routing: %s", raw)
	}
	if un, _ := routing["unmapped_guest_networks"].([]any); len(un) != 1 {
		t.Fatalf("unmapped networks should be only the Default Network, not the archived ones: %v", un)
	}

	// Reading it changed nothing, and nothing was chosen for the operator.
	if f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1::uuid`, def) != 0 {
		t.Fatal("a PMS route was copied to the Default Network")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1::uuid`, f.tenant) != routesBefore ||
		f.count(t, `SELECT count(*) FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1::uuid AND rule_type='SITE_NETWORK'`, rev) != rulesBefore {
		t.Fatal("routing or package rules changed")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1::uuid AND rule_value::text LIKE '%'||$2||'%'`, rev, def) != 0 {
		t.Fatal("the pinned package's rule was widened to the Default Network")
	}

	// The operator chooses ONE interface through the existing, explicit routing call. The other interface's route stays where it was.
	first := cands[0].(map[string]any)["pms_interface_id"].(string)
	if st, b := f.setRouting(t, def, "DEDICATED", first); st != 200 {
		t.Fatalf("route default = %d %v", st, b)
	}
	_, r = f.do(t, "GET", "/network/default", nil)
	for _, it := range r["items"].([]any) {
		// Routed now, so the item reports whether the chosen interface can answer. The fixture's interface has no live feed, so it is honestly not ready; what must
		// have gone is the "choose how room sign-in works" decision.
		if m := it.(map[string]any); m["id"] == "pms" && strings.Contains(m["detail"].(string), "Choose") {
			t.Fatalf("pms after the operator's choice still asks for a decision: %v", m)
		}
	}
	if len(r["pms_candidates"].([]any)) != 0 {
		t.Fatal("candidates are still offered once the Default Network is routed")
	}
	if f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1::uuid`, f.tenant) != routesBefore+1 {
		t.Fatal("choosing one interface moved or removed another route")
	}
}

func TestIntegration_Default_NothingIsClaimedWithoutADefaultNetwork(t *testing.T) {
	f := newAPI(t)
	startFakeNetd(t, f)
	f.retireRevisions(t)
	_, r := f.do(t, "GET", "/network/default", nil)
	if r["present"] != false || len(r["items"].([]any)) != 1 || r["items"].([]any)[0].(map[string]any)["state"] != "attention" {
		t.Fatalf("%v", r)
	}
}

// The journey again, with the server using the Admin Console's own database role (svc_edged), as the appliance does. The integration database is usually reached as a
// superuser, which hides a missing privilege; this is the proof that the reset, its confirmation, the archive and the Default Network checks need no more than the role holds.
func TestIntegration_Archive_TheWholeJourneyRunsAsTheAdminConsoleRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	f.app.commerce = f.commerceAdmin(t)
	iface, _, _ := f.seedInterface(t)
	if st, b := f.setRouting(t, h.vlans[0], "DEDICATED", iface); st != 200 {
		t.Fatalf("route = %d %v", st, b)
	}
	f.seedNetworkPackage(t, "role-"+uniqueParent(), h.vlans[0], nil)

	rp, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	super := f.app.db
	f.app.db = rp
	defer func() { f.app.db = super }()

	if st, b := f.do(t, "POST", "/network/recovery/preview", map[string]any{"op": "full_reset"}); st != 200 {
		t.Fatalf("preview as svc_edged = %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("stage as svc_edged = %d %v", st, b)
	}
	if st, b := f.apply(t); st != 200 {
		t.Fatalf("apply as svc_edged = %d %v", st, b)
	}
	if st, b := f.confirmRev(t, fn.revision); st != 200 {
		t.Fatalf("confirm as svc_edged = %d %v", st, b)
	}
	for _, path := range []string{"/network/recovery", "/network/archive", "/network/default", "/network/overview", "/network/guest-networks", "/network/changes", "/network/ports"} {
		if st, b := f.do(t, "GET", path, nil); st != 200 {
			t.Fatalf("GET %s as svc_edged = %d %v", path, st, b)
		}
	}
	_, d := f.do(t, "GET", "/network/default", nil)
	for _, it := range d["items"].([]any) {
		if m := it.(map[string]any); m["id"] == "pms" || m["id"] == "packages" {
			if m["state"] != "unresolved" {
				t.Fatalf("as svc_edged %v is %v (a read failure is reported as attention, not as a finding)", m["id"], m["state"])
			}
		}
	}
	if st, b := f.do(t, "POST", "/network/guest-networks/"+h.vlans[0]+"/unarchive", nil); st != 200 {
		t.Fatalf("unarchive as svc_edged = %d %v", st, b)
	}
}

// A check that cannot read its data still answers with lists, never null: the page that shows it must not fail on the very case it is reporting.
func TestIntegration_Default_ReadinessKeepsListsWhenChecksCannotRead(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	f.app.commerce = nil
	_, r := f.do(t, "GET", "/network/default", nil)
	for _, k := range []string{"items", "pms_candidates", "packages_pinned_to_other_networks"} {
		if _, ok := r[k].([]any); !ok {
			t.Fatalf("%s is %v, want a list", k, r[k])
		}
	}
	for _, it := range r["items"].([]any) {
		if m := it.(map[string]any); m["id"] == "packages" && m["state"] != "attention" {
			t.Fatalf("packages that cannot be read must say so, got %v", m)
		}
	}
}

// A reset confirmed before the archive existed is caught up, from the recorded facts: the networks it switched off (migration 0154) and the ones that were
// already switched off before it (the catch-up on the reads that show the archive). Networks touched since are not archived, and a network restored from the
// archive is not archived again.
func TestIntegration_Archive_AResetConfirmedBeforeTheFeatureIsCaughtUp(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	earlier := f.mkNet(t, "Retired earlier", 3071, "207.0")
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+earlier+"/disable", nil); st != 200 {
		t.Fatal("disable")
	}
	h := heavilyReconfigured(t, f, fn)
	ctx := context.Background()
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("stage = %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	if f.archivedCount(t) != 4 {
		t.Fatalf("a confirmed reset archived %d, want the 3 it switched off and the one already off", f.archivedCount(t))
	}
	// As if the reset had been confirmed by the previous version: no markers.
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.network_archive WHERE tenant_id=$1::uuid`, f.tenant); err != nil {
		t.Fatal(err)
	}
	// Someone switched one retired network back on later: it must not come back as archived.
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+h.vlans[0]+"/enable", nil); st != 200 {
		t.Fatal("enable")
	}
	raw, err := os.ReadFile("../../migrations/0154_a_client_network_a_reset_retired_is_archived_not_deleted.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if _, err := f.pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("run %d of the migration: %v", i+1, err)
		}
	}
	if got := f.archivedCount(t); got != 2 {
		t.Fatalf("the migration archived %d, want the 2 the reset switched off that are still off", got)
	}
	// The catch-up on the read adds the one that was already off, and nothing the operator touched.
	_, ar := f.do(t, "GET", "/network/archive", nil)
	if ar["count"] != float64(3) {
		t.Fatalf("after the catch-up: %v", ar)
	}
	for k, v := range f.listFlags(t) {
		if strings.Contains(k, h.vlans[0][:8]) && v[0] {
			t.Fatal("a network that was switched on again was archived")
		}
		if strings.HasPrefix(k, "Client Wi-Fi") && v[0] {
			t.Fatal("the Default Network was archived")
		}
	}
	// A network restored from the archive stays restored across later reads.
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+earlier+"/unarchive", nil); st != 200 {
		t.Fatal("restore")
	}
	f.do(t, "GET", "/network/archive", nil)
	f.do(t, "GET", "/network/guest-networks", nil)
	f.do(t, "GET", "/network/overview", nil)
	if f.count(t, `SELECT count(*) FROM iam_v2.network_archive WHERE network_id=$1::uuid`, earlier) != 0 {
		t.Fatal("a network restored from the archive was archived again by the catch-up")
	}
}
