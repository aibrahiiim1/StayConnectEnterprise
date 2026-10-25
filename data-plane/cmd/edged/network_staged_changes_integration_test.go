//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A fake network service on a unix socket that records what edged asks of it and keeps the revision table the way the real one does, so the staged-change
// lifecycle (stage, apply, confirm, roll back, time out) is exercised against the real database and the real handlers.
type fakeNetd struct {
	t        *testing.T
	f        *apiFixture
	mu       sync.Mutex
	applyAs  string // pending_confirmation | rolled_back | failed
	wanIP    string
	wanBits  int
	lan      string
	revision string
	calls    []string
}

func startFakeNetd(t *testing.T, f *apiFixture) *fakeNetd {
	t.Helper()
	dir, err := os.MkdirTemp("", "nd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "n.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets are not available here: %v", err)
	}
	fn := &fakeNetd{t: t, f: f, applyAs: "pending_confirmation", wanIP: "172.21.60.25", wanBits: 24, lan: "ens192"}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/v1/system-network", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{
			"wan": map[string]any{"interface": "ens160", "ip": fn.wanIP, "prefix_len": fn.wanBits},
			"lan": map[string]any{"physical_interface": fn.lan},
		})
	})
	mux.HandleFunc("/v1/apply", func(w http.ResponseWriter, r *http.Request) {
		fn.mu.Lock()
		defer fn.mu.Unlock()
		fn.calls = append(fn.calls, "apply")
		state := fn.applyAs
		intent := fn.currentIntent()
		var id string
		if err := f.pool.QueryRow(context.Background(), `INSERT INTO network_config_revisions (state, summary, intent)
			VALUES ($1,'fake apply',$2::jsonb) RETURNING id::text`, state, intent).Scan(&id); err != nil {
			reply(w, 500, map[string]string{"error": err.Error()})
			return
		}
		fn.revision = id
		code := 200
		if state == "failed" {
			code = 422
		}
		reply(w, code, map[string]any{"revision_id": id, "state": state})
	})
	mux.HandleFunc("/v1/confirm", func(w http.ResponseWriter, r *http.Request) {
		fn.mu.Lock()
		defer fn.mu.Unlock()
		fn.calls = append(fn.calls, "confirm")
		_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='superseded' WHERE state='active'`)
		_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='active', confirmed_at=now() WHERE id=$1`, fn.revision)
		reply(w, 200, map[string]string{"state": "active"})
	})
	mux.HandleFunc("/v1/rollback", func(w http.ResponseWriter, r *http.Request) {
		fn.mu.Lock()
		defer fn.mu.Unlock()
		fn.calls = append(fn.calls, "rollback")
		_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='rolled_back', failure_reason='operator requested rollback' WHERE id=$1`, fn.revision)
		reply(w, 200, map[string]string{"state": "rolled_back"})
	})
	mux.HandleFunc("/v1/pending", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]string{"revision_id": fn.revision})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	f.app.netd = newNetdClient(sock)
	return fn
}

// currentIntent is the site's rows as netd would apply them (the saved rows at apply time).
func (fn *fakeNetd) currentIntent() string {
	nets, err := fn.f.app.loadSavedIntents(context.Background())
	if err != nil {
		fn.t.Fatal(err)
	}
	raw, _ := json.Marshal(nets)
	return string(raw)
}

// timeOut is what the watchdog does when nobody confirms: the revision becomes rolled_back with no operator action.
func (fn *fakeNetd) timeOut(t *testing.T) {
	t.Helper()
	if _, err := fn.f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='rolled_back', failure_reason='confirmation window elapsed' WHERE id=$1`, fn.revision); err != nil {
		t.Fatal(err)
	}
}

func (f *apiFixture) retireRevisions(t *testing.T) {
	t.Helper()
	for _, s := range []string{"active", "applying", "pending_confirmation"} {
		_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='superseded' WHERE state=$1`, s)
	}
}

func (f *apiFixture) enabledOf(t *testing.T, id string) bool {
	t.Helper()
	var e bool
	if err := f.pool.QueryRow(context.Background(), `SELECT enabled FROM guest_networks WHERE id=$1`, id).Scan(&e); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return e
}

func (f *apiFixture) changeStates(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range ids {
		var kind, state string
		if err := f.pool.QueryRow(context.Background(), `SELECT kind, state FROM iam_v2.guest_network_changes WHERE network_id=$1 ORDER BY created_at DESC LIMIT 1`, id).Scan(&kind, &state); err != nil {
			t.Fatalf("change of %s: %v", id, err)
		}
		out[id] = kind + "/" + state
	}
	return out
}

func (f *apiFixture) apply(t *testing.T) (int, map[string]any) {
	t.Helper()
	return f.do(t, "POST", "/network/apply", map[string]any{"summary": "test", "password": f.password})
}

func (f *apiFixture) confirmRev(t *testing.T, rev string) (int, map[string]any) {
	t.Helper()
	return f.do(t, "POST", "/network/revisions/"+rev+"/confirm", map[string]any{"password": f.password})
}

func (f *apiFixture) rollbackRev(t *testing.T, rev string) (int, map[string]any) {
	t.Helper()
	return f.do(t, "POST", "/network/revisions/"+rev+"/rollback", map[string]any{"password": f.password})
}

// ---- the lifecycle -----------------------------------------------------------------------------------------------------------

func TestIntegration_Factory_ActivationIsStagedAppliedAndConfirmed(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	f.retireRevisions(t)

	st, body := f.do(t, "GET", "/network/factory", nil)
	if st != 200 || body["state"] != "prepared" {
		t.Fatalf("factory = %d %v", st, body)
	}
	tmpl := body["template"].(map[string]any)
	if tmpl["subnet_cidr"] != "10.20.0.0/20" || tmpl["gateway_ip"] != "10.20.0.1" {
		t.Fatalf("template %v", tmpl)
	}

	// Without the acknowledgement nothing is staged.
	if st, _ := f.do(t, "POST", "/network/factory/activate", map[string]any{}); st != 400 {
		t.Fatalf("activation without the acknowledgement = %d", st)
	}
	if f.networkCount(t) != 0 {
		t.Fatal("a refused activation left a network behind")
	}

	st, body = f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true})
	if st != 201 {
		t.Fatalf("activate = %d %v", st, body)
	}
	id := body["network_id"].(string)
	if f.enabledOf(t, id) {
		t.Fatal("activation switched the network on before any apply")
	}
	if n := f.overviewOf(t, id); n.Status != "change_pending" || !strings.Contains(n.Reason, "Activation is staged") {
		t.Fatalf("staged: %+v", n)
	}
	if _, body := f.do(t, "GET", "/network/factory", nil); body["state"] != "staged" || body["network_id"] != id {
		t.Fatalf("factory after staging: %v", body)
	}

	// Apply: the flag moves only now, inside the confirmation window.
	st, applied := f.apply(t)
	if st != 200 || applied["state"] != "pending_confirmation" {
		t.Fatalf("apply = %d %v", st, applied)
	}
	if !f.enabledOf(t, id) {
		t.Fatal("the apply did not switch the network on")
	}
	if got := f.changeStates(t, id)[id]; got != "ENABLE/APPLIED" {
		t.Fatalf("change is %s", got)
	}

	// Confirm: settled.
	if st, body := f.confirmRev(t, fn.revision); st != 200 {
		t.Fatalf("confirm = %d %v", st, body)
	}
	if got := f.changeStates(t, id)[id]; got != "ENABLE/CONFIRMED" {
		t.Fatalf("after confirm the change is %s", got)
	}
	if n := f.overviewOf(t, id); n.Status != "active" || !n.Running {
		t.Fatalf("confirmed: %+v", n)
	}
}

func TestIntegration_Factory_NeverForcesAConflictingAddress(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()

	// An enabled network already inside the factory range.
	if st, b := f.do(t, "POST", "/network/guest-networks",
		netBody("Existing", "vlan", uniqueParent()+"x", 150, "10.20.4.0/24", "10.20.4.1", "10.20.4.100", "10.20.4.200")); st != 201 {
		t.Fatalf("%d %v", st, b)
	}
	_, body := f.do(t, "GET", "/network/factory", nil)
	if body["state"] != "blocked" {
		t.Fatalf("a conflicting site was offered the factory network: %v", body)
	}
	if st, _ := f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true}); st != 409 {
		t.Fatalf("activation over a conflict = %d", st)
	}
	var factoryRows int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM guest_networks WHERE tenant_id=$1 AND name='Client Wi-Fi'`, f.tenant).Scan(&factoryRows)
	if factoryRows != 0 {
		t.Fatal("a factory network was created over a conflict")
	}
}

func TestIntegration_Factory_WANOverlapBlocks(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan, fn.wanIP, fn.wanBits = uniqueParent(), "10.20.5.9", 24
	if _, body := f.do(t, "GET", "/network/factory", nil); body["state"] != "blocked" {
		t.Fatalf("%v", body)
	}
}

func TestIntegration_Factory_ExistingNetworksOnThePortAreAdoptedNotDuplicated(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.lan = uniqueParent()
	if st, b := f.do(t, "POST", "/network/guest-networks",
		netBody("Lobby", "untagged", fn.lan, 0, "192.168.77.0/24", "192.168.77.1", "192.168.77.100", "192.168.77.200")); st != 201 {
		t.Fatalf("%d %v", st, b)
	}
	before := f.networkCount(t)
	_, body := f.do(t, "GET", "/network/factory", nil)
	if body["state"] != "adopted" {
		t.Fatalf("%v", body)
	}
	if st, _ := f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true}); st != 409 {
		t.Fatalf("activation on an adopted port = %d", st)
	}
	if f.networkCount(t) != before {
		t.Fatal("a network was added to a port that already had one")
	}
}

func TestIntegration_Factory_NothingBeforeSiteAssignment(t *testing.T) {
	f := newAPI(t)
	startFakeNetd(t, f)
	saved, savedSite := f.app.tenantID, f.app.siteID
	f.app.tenantID, f.app.siteID = "", ""
	defer func() { f.app.tenantID, f.app.siteID = saved, savedSite }()
	_, body := f.do(t, "GET", "/network/factory", nil)
	if body["state"] != "awaiting_assignment" {
		t.Fatalf("%v", body)
	}
	if st, _ := f.do(t, "POST", "/network/factory/activate", map[string]any{"no_other_dhcp_server": true}); st == 201 {
		t.Fatal("a network was activated on an appliance with no site")
	}
}

// ---- a failed or rolled-back apply puts every flag back ------------------------------------------------------------------------

func stageTwo(t *testing.T, f *apiFixture) (a, b string) {
	t.Helper()
	parent := uniqueParent()
	mk := func(name string, vlan int, subnet, gw, lo, hi string) string {
		st, body := f.do(t, "POST", "/network/guest-networks", netBody(name, "vlan", parent, vlan, subnet, gw, lo, hi))
		if st != 201 {
			t.Fatalf("create %s = %d %v", name, st, body)
		}
		id := body["id"].(string)
		f.do(t, "POST", "/network/guest-networks/"+id+"/disable", nil)
		return id
	}
	a = mk("Staged A", 161, "10.161.0.0/24", "10.161.0.1", "10.161.0.100", "10.161.0.200")
	b = mk("Staged B", 162, "10.162.0.0/24", "10.162.0.1", "10.162.0.100", "10.162.0.200")
	ctx := context.Background()
	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.stageChanges(ctx, tx, "ACTIVATE", []changeItem{{a, changeEnable}, {b, changeEnable}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestIntegration_StagedChanges_AFailedApplyPutsEveryFlagBack(t *testing.T) {
	for _, as := range []string{"rolled_back", "failed"} {
		t.Run(as, func(t *testing.T) {
			f := newAPI(t)
			fn := startFakeNetd(t, f)
			fn.applyAs = as
			f.retireRevisions(t)
			a, b := stageTwo(t, f)

			st, _ := f.apply(t)
			if st < 300 && as == "failed" {
				t.Fatalf("a failed apply answered %d", st)
			}
			for _, id := range []string{a, b} {
				if f.enabledOf(t, id) {
					t.Fatalf("network %s was left switched on by an apply that did not hold", id)
				}
			}
			for id, got := range f.changeStates(t, a, b) {
				if got != "ENABLE/PENDING" {
					t.Fatalf("change of %s is %s, want it waiting to be applied again", id, got)
				}
			}
		})
	}
}

func TestIntegration_StagedChanges_AnOperatorRollbackPutsEveryFlagBack(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	a, b := stageTwo(t, f)
	if st, body := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, body)
	}
	if !f.enabledOf(t, a) || !f.enabledOf(t, b) {
		t.Fatal("setup: the apply should have switched both on")
	}
	if st, body := f.rollbackRev(t, fn.revision); st != 200 {
		t.Fatalf("rollback = %d %v", st, body)
	}
	if f.enabledOf(t, a) || f.enabledOf(t, b) {
		t.Fatal("a rollback left a network switched on in the database")
	}
	for id, got := range f.changeStates(t, a, b) {
		if got != "ENABLE/PENDING" {
			t.Fatalf("%s is %s", id, got)
		}
	}
}

// The watchdog rolls back with nobody asking; the database follows the next time anyone looks.
func TestIntegration_StagedChanges_AConfirmationThatLapsesIsReconciled(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	a, b := stageTwo(t, f)
	if st, body := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, body)
	}
	fn.timeOut(t)
	// Until something reads, the rows still say enabled; reading reconciles.
	if st, _ := f.do(t, "GET", "/network/guest-networks", nil); st != 200 {
		t.Fatal("list")
	}
	if f.enabledOf(t, a) || f.enabledOf(t, b) {
		t.Fatal("the lapsed confirmation left networks switched on in the database")
	}
	if n := f.overviewOf(t, a); n.Status != "change_pending" {
		t.Fatalf("after the lapse the change is waiting to be applied again: %+v", n)
	}
}

func TestIntegration_StagedChanges_AConfirmSettlesAndALaterRollbackCannotUndoIt(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	a, b := stageTwo(t, f)
	f.apply(t)
	if st, _ := f.confirmRev(t, fn.revision); st != 200 {
		t.Fatal("confirm")
	}
	// A stale reconcile (a later list, a late watchdog tick) must not revert a confirmed change.
	f.do(t, "GET", "/network/guest-networks", nil)
	if !f.enabledOf(t, a) || !f.enabledOf(t, b) {
		t.Fatal("a confirmed switch-on was undone")
	}
	for id, got := range f.changeStates(t, a, b) {
		if got != "ENABLE/CONFIRMED" {
			t.Fatalf("%s is %s", id, got)
		}
	}
}

// Either both networks come on, or the whole apply is refused and nothing is touched.
func TestIntegration_StagedChanges_ACollisionAtApplyRefusesTheWholeApply(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	a, b := stageTwo(t, f)
	// Someone enables another network on A's subnet after the staging.
	parent := uniqueParent()
	if st, body := f.do(t, "POST", "/network/guest-networks", netBody("Intruder", "vlan", parent, 170, "10.161.0.0/25", "10.161.0.1", "10.161.0.10", "10.161.0.20")); st != 409 && st != 201 {
		t.Fatalf("%d %v", st, body)
	}
	st, body := f.apply(t)
	if st == 200 {
		t.Fatalf("an apply that would put two networks on one subnet was accepted: %v", body)
	}
	if f.enabledOf(t, a) || f.enabledOf(t, b) {
		t.Fatal("part of a refused apply stayed applied")
	}
	if len(fn.calls) != 0 {
		t.Fatalf("netd was asked to apply a configuration edged had already refused: %v", fn.calls)
	}
}

func TestIntegration_StagedChanges_AGroupCanBeCancelledUntilItIsApplied(t *testing.T) {
	f := newAPI(t)
	startFakeNetd(t, f)
	f.retireRevisions(t)
	a, b := stageTwo(t, f)
	st, body := f.do(t, "GET", "/network/changes", nil)
	if st != 200 {
		t.Fatal(body)
	}
	groups := body["groups"].([]any)
	var mine map[string]any
	for _, g := range groups {
		gm := g.(map[string]any)
		for _, c := range gm["changes"].([]any) {
			if c.(map[string]any)["network_id"] == a {
				mine = gm
			}
		}
	}
	if mine == nil || len(mine["changes"].([]any)) != 2 {
		t.Fatalf("the operation is not listed as one group: %v", body)
	}
	if st, _ := f.do(t, "DELETE", "/network/changes/"+mine["group_id"].(string), nil); st != 200 {
		t.Fatal("cancel")
	}
	for id, got := range f.changeStates(t, a, b) {
		if got != "ENABLE/CANCELLED" {
			t.Fatalf("%s is %s", id, got)
		}
	}
	if st, _ := f.do(t, "DELETE", "/network/changes/"+mine["group_id"].(string), nil); st != 404 {
		t.Fatalf("cancelling twice = %d", st)
	}
}

// ---- Access / Trunk ---------------------------------------------------------------------------------------------------------------

func accessPort(t *testing.T, f *apiFixture) (port, nativeID string) {
	t.Helper()
	port = uniqueParent()
	st, body := f.do(t, "POST", "/network/guest-networks", netBody("Native", "untagged", port, 0, "10.181.0.0/24", "10.181.0.1", "10.181.0.100", "10.181.0.200"))
	if st != 201 {
		t.Fatalf("native = %d %v", st, body)
	}
	return port, body["id"].(string)
}

func TestIntegration_Ports_AccessToTrunkIsPreviewedStagedAndAppliedTogether(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	port, native := accessPort(t, f)
	fn.lan = port

	_, ports := f.do(t, "GET", "/network/ports", nil)
	found := false
	for _, p := range ports["ports"].([]any) {
		pm := p.(map[string]any)
		if pm["name"] == port {
			found = true
			if pm["mode"] != "access" {
				t.Fatalf("mode = %v", pm["mode"])
			}
		}
	}
	if !found {
		t.Fatalf("the port is not listed: %v", ports)
	}

	req := map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 191}, {"vlan_id": 192, "name": "Conference"}}}
	preview := map[string]any{"mode": "trunk", "preview": true, "vlans": req["vlans"]}
	st, plan := f.do(t, "POST", "/network/ports/"+port+"/mode", preview)
	if st != 200 {
		t.Fatalf("preview = %d %v", st, plan)
	}
	if len(plan["create"].([]any)) != 2 || plan["keep_native"] == nil {
		t.Fatalf("plan %v", plan)
	}
	if !strings.Contains(strings.Join(toStrings(plan["impact"]), " "), "not guaranteed to be uninterrupted") {
		t.Fatalf("the plan does not say the change may interrupt: %v", plan["impact"])
	}
	if !strings.Contains(strings.Join(toStrings(plan["switch_requirements"]), " "), "VLAN 191, 192") {
		t.Fatalf("switch requirements: %v", plan["switch_requirements"])
	}
	if f.networkCount(t) != 1 {
		t.Fatal("a preview wrote networks")
	}

	st, body := f.do(t, "POST", "/network/ports/"+port+"/mode", req)
	if st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	ids := toStrings(body["network_ids"])
	if len(ids) != 2 {
		t.Fatalf("%v", body)
	}
	for _, id := range ids {
		if f.enabledOf(t, id) {
			t.Fatal("staging switched a VLAN on")
		}
	}
	if st, applied := f.apply(t); st != 200 {
		t.Fatalf("apply = %d %v", st, applied)
	}
	for _, id := range append(ids, native) {
		if !f.enabledOf(t, id) {
			t.Fatalf("network %s is not on after the apply", id)
		}
	}
	f.confirmRev(t, fn.revision)
	_, ports = f.do(t, "GET", "/network/ports", nil)
	for _, p := range ports["ports"].([]any) {
		if pm := p.(map[string]any); pm["name"] == port && pm["mode"] != "trunk" {
			t.Fatalf("after the conversion the port is %v", pm["mode"])
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func TestIntegration_Ports_AFailedConversionLeavesTheAccessPortAsItWas(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	fn.applyAs = "rolled_back"
	f.retireRevisions(t)
	port, native := accessPort(t, f)
	st, body := f.do(t, "POST", "/network/ports/"+port+"/mode", map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 201}, {"vlan_id": 202}}})
	if st != 201 {
		t.Fatalf("%d %v", st, body)
	}
	ids := toStrings(body["network_ids"])
	f.apply(t)
	for _, id := range ids {
		if f.enabledOf(t, id) {
			t.Fatal("a VLAN stayed on after the apply was rolled back")
		}
	}
	if !f.enabledOf(t, native) {
		t.Fatal("the native network was disturbed")
	}
	_, ports := f.do(t, "GET", "/network/ports", nil)
	for _, p := range ports["ports"].([]any) {
		if pm := p.(map[string]any); pm["name"] == port && pm["mode"] != "access" {
			t.Fatalf("the port is %v after a failed conversion", pm["mode"])
		}
	}
}

func TestIntegration_Ports_TrunkToAccessSwitchesOffEveryVLANAndKeepsTheNative(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	port, native := accessPort(t, f)
	st, body := f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []any{
		netBody("V1", "vlan", port, 211, "10.211.0.0/24", "10.211.0.1", "10.211.0.100", "10.211.0.200"),
		netBody("V2", "vlan", port, 212, "10.212.0.0/24", "10.212.0.1", "10.212.0.100", "10.212.0.200"),
	}})
	if st != 201 {
		t.Fatalf("%d %v", st, body)
	}

	st, plan := f.do(t, "POST", "/network/ports/"+port+"/mode", map[string]any{"mode": "access", "preview": true})
	if st != 200 || len(plan["switch_off"].([]any)) != 2 {
		t.Fatalf("plan = %d %v", st, plan)
	}
	if st, body := f.do(t, "POST", "/network/ports/"+port+"/mode", map[string]any{"mode": "access"}); st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	if st, _ := f.apply(t); st != 200 {
		t.Fatal("apply")
	}
	f.confirmRev(t, fn.revision)
	if !f.enabledOf(t, native) {
		t.Fatal("the native network was switched off")
	}
	_, ports := f.do(t, "GET", "/network/ports", nil)
	for _, p := range ports["ports"].([]any) {
		if pm := p.(map[string]any); pm["name"] == port && (pm["mode"] != "access" || len(pm["vlans"].([]any)) != 0) {
			t.Fatalf("%v", pm)
		}
	}
}

func TestIntegration_Ports_RefusalsAreSpecific(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	f.retireRevisions(t)
	port, _ := accessPort(t, f)
	post := func(iface string, body map[string]any) (int, map[string]any) {
		return f.do(t, "POST", "/network/ports/"+iface+"/mode", body)
	}
	// Already access.
	if st, _ := post(port, map[string]any{"mode": "access"}); st != 409 {
		t.Fatalf("access on an access port = %d", st)
	}
	// Overlaps an enabled network.
	st, body := post(port, map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 221, "subnet_cidr": "10.181.0.0/25",
		"pools": []map[string]string{{"start_ip": "10.181.0.10", "end_ip": "10.181.0.20"}}}}})
	if st != 409 || !strings.Contains(fmt.Sprint(body), "overlaps") {
		t.Fatalf("overlap = %d %v", st, body)
	}
	// A VLAN id out of range, and a duplicate within the request.
	if st, body := post(port, map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 5000}}}); st != 409 {
		t.Fatalf("range = %d %v", st, body)
	}
	if st, body := post(port, map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 230}, {"vlan_id": 230}}}); st != 409 {
		t.Fatalf("duplicate = %d %v", st, body)
	}
	// The WAN subnet.
	fn.wanIP, fn.wanBits = "10.240.0.9", 24
	if st, body := post(port, map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 231, "subnet_cidr": "10.240.0.0/24"}}}); st != 409 {
		t.Fatalf("wan overlap = %d %v", st, body)
	}
	// Unknown mode and unknown port.
	if st, _ := post(port, map[string]any{"mode": "bridge"}); st != 400 {
		t.Fatalf("mode = %d", st)
	}
	if st, _ := post("nosuchport", map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 240}}}); st != 404 {
		t.Fatalf("port = %d", st)
	}
	if f.networkCount(t) != 1 {
		t.Fatal("a refused conversion left networks behind")
	}
}

func TestIntegration_Ports_TrunkToAccessNeedsANativeNetworkToKeep(t *testing.T) {
	f := newAPI(t)
	startFakeNetd(t, f)
	f.retireRevisions(t)
	port := uniqueParent()
	st, body := f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []any{
		netBody("OnlyVLAN", "vlan", port, 251, "10.251.0.0/24", "10.251.0.1", "10.251.0.100", "10.251.0.200"),
	}})
	if st != 201 {
		t.Fatalf("%d %v", st, body)
	}
	st, body = f.do(t, "POST", "/network/ports/"+port+"/mode", map[string]any{"mode": "access"})
	if st != 409 || !strings.Contains(fmt.Sprint(body), "no untagged network to keep") {
		t.Fatalf("%d %v", st, body)
	}
}
