//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// feed gives an interface the runtime state of a healthy connected feed (healthy) or one with a continuity gap (not).
func (f *apiFixture) feed(t *testing.T, iface string, healthy bool) {
	t.Helper()
	continuity := "CONTINUOUS"
	if !healthy {
		continuity = "GAP_DETECTED"
	}
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO iam_v2.pms_interface_runtime
		  (tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, published_resync_generation, pinned_revision_id,
		   transport_status, sync_status, continuity_status, last_connected_at, last_heartbeat_at, last_complete_sync_at)
		SELECT i.tenant_id, i.site_id, i.id, 1, 'NONE', 0, i.current_revision_id, 'CONNECTED', 'IN_SYNC', $2, now(), now(), now()
		  FROM iam_v2.pms_interfaces i WHERE i.id=$1::uuid
		ON CONFLICT DO NOTHING`, iface, continuity); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime SET continuity_status=$2 WHERE pms_interface_id=$1::uuid`, iface, continuity); err != nil {
		t.Fatalf("feed: %v", err)
	}
}

func (f *apiFixture) stayInRoom(t *testing.T, iface, room string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,normalized_room_number,status,lifecycle_version,last_applied_event_version)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,$4,$5,'IN_HOUSE',1,0)`, f.tenant, f.site, iface, fmt.Sprintf("R-%s-%s", iface[:8], room), room); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *apiFixture) routing(t *testing.T, network string) map[string]any {
	t.Helper()
	_, b := f.do(t, "GET", "/pms-routing", nil)
	for _, p := range b["policies"].([]any) {
		if m := p.(map[string]any); m["network_id"] == network {
			return m
		}
	}
	t.Fatalf("network %s is not in the routing list: %v", network, b)
	return nil
}

func (f *apiFixture) preview(t *testing.T, network, mode string, ifaces ...string) (int, map[string]any) {
	return f.do(t, "POST", "/pms-routing/"+network+"/preview", map[string]any{"mode": mode, "pms_interface_ids": ifaces})
}

func (f *apiFixture) setRouting(t *testing.T, network, mode string, ifaces ...string) (int, map[string]any) {
	t.Helper()
	f.preview(t, network, mode, ifaces...)
	return f.do(t, "PUT", "/pms-routing/"+network, map[string]any{"mode": mode, "pms_interface_ids": ifaces, "based_on": f.routing(t, network)["fingerprint"]})
}

func (f *apiFixture) clearRouting(t *testing.T, network string) (int, map[string]any) {
	t.Helper()
	return f.do(t, "DELETE", "/pms-routing/"+network+"?based_on="+f.routing(t, network)["fingerprint"].(string), nil)
}

func codes(b map[string]any, key string) []string {
	var out []string
	if l, ok := b[key].([]any); ok {
		for _, x := range l {
			out = append(out, x.(map[string]any)["code"].(string))
		}
	}
	return out
}

func has(list []string, code string) bool {
	for _, c := range list {
		if c == code {
			return true
		}
	}
	return false
}

func (f *apiFixture) mapRows(t *testing.T, network string) int {
	return f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1::uuid`, network)
}

// The policy and its interfaces are written together, after a preview, against that preview's fingerprint.
func TestIntegration_Routing_ADedicatedChoiceNeedsItsPreviewAndWritesPolicyAndInterfaceTogether(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Rooms", 3101, "171.0")
	a, _, _ := f.seedInterface(t)

	st, pv := f.preview(t, net, "DEDICATED", a)
	if st != 200 || len(pv["blockers"].([]any)) != 0 {
		t.Fatalf("preview = %d %v", st, pv)
	}
	if g := pv["guarantees"].([]any); len(g) < 3 {
		t.Fatalf("the preview does not state what is NOT touched: %v", g)
	}
	if f.mapRows(t, net) != 0 {
		t.Fatal("previewing changed the routing")
	}
	// no preview fingerprint: refused
	if st, _ := f.do(t, "PUT", "/pms-routing/"+net, map[string]any{"mode": "DEDICATED", "pms_interface_ids": []string{a}}); st != 400 {
		t.Fatalf("a change without its preview = %d, want 400", st)
	}
	// a stale fingerprint: refused, nothing written
	if st, b := f.do(t, "PUT", "/pms-routing/"+net, map[string]any{"mode": "DEDICATED", "pms_interface_ids": []string{a}, "based_on": "deadbeefdeadbeef"}); st != 409 || b["error"] != "routing_changed" {
		t.Fatalf("a stale preview = %d %v", st, b)
	}
	if f.mapRows(t, net) != 0 {
		t.Fatal("a refused change wrote routing")
	}
	if st, b := f.setRouting(t, net, "DEDICATED", a); st != 200 || b["mode"] != "DEDICATED" {
		t.Fatalf("apply = %d %v", st, b)
	}
	if f.mapRows(t, net) != 1 || f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1::uuid AND mode='DEDICATED'`, net) != 1 {
		t.Fatal("policy and interface were not written together")
	}
	if f.count(t, `SELECT count(*) FROM public.audit_log WHERE action='pms_routing.policy_set' AND target_id=$1`, net) != 1 {
		t.Fatal("the change was not audited")
	}
}

// SHARED_STRICT is an explicit set of two or three; the preview says what is shared and what is at risk.
func TestIntegration_Routing_ASharedStrictSetIsExplicitBoundedAndPreviewed(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Resort", 3102, "172.0")
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	c, _, _ := f.seedInterface(t)
	d, _, _ := f.seedInterface(t)
	f.stayInRoom(t, a, "412")
	f.stayInRoom(t, b, "412")
	f.stayInRoom(t, b, "900")

	st, pv := f.preview(t, net, "SHARED_STRICT", a, b)
	if st != 200 || len(pv["blockers"].([]any)) != 0 {
		t.Fatalf("preview = %d %v", st, pv)
	}
	if !has(codes(pv, "warnings"), "ROOM_NUMBERS_OVERLAP") {
		t.Fatalf("the same room number in two selected PMS was not called out: %v", pv["warnings"])
	}
	if pv["impact"].(map[string]any)["room_numbers_in_more_than_one_selected_pms"].(float64) != 1 {
		t.Fatalf("overlap count: %v", pv["impact"])
	}
	if !has(codes(pv, "warnings"), "INTERFACE_NOT_READY") {
		t.Fatalf("interfaces with no live feed were not flagged as unable to answer: %v", pv["warnings"])
	}

	for name, ids := range map[string][]string{"one interface": {a}, "four interfaces": {a, b, c, d}, "none": {}} {
		_, bad := f.preview(t, net, "SHARED_STRICT", ids...)
		if len(bad["blockers"].([]any)) == 0 {
			t.Errorf("%s was accepted as a shared set", name)
		}
	}
	if st, _ := f.setRouting(t, net, "SHARED_STRICT", a, b, c, d); st != 409 {
		t.Fatalf("four interfaces applied = %d", st)
	}
	if f.mapRows(t, net) != 0 {
		t.Fatal("a refused shared set wrote rows")
	}
	if st, body := f.setRouting(t, net, "SHARED_STRICT", b, a); st != 200 || body["mode"] != "SHARED_STRICT" {
		t.Fatalf("apply = %d %v", st, body)
	}
	if f.mapRows(t, net) != 2 || f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1::uuid AND is_default`, net) != 1 {
		t.Fatalf("rows=%d", f.mapRows(t, net))
	}
	// ...and back to one: the removed interface is named, nothing else moves
	_, pv = f.preview(t, net, "DEDICATED", a)
	if rem := pv["removed"].([]any); len(rem) != 1 || rem[0].(map[string]any)["pms_interface_id"] != b {
		t.Fatalf("removed = %v", pv["removed"])
	}
}

func TestIntegration_Routing_NoneTakesNoInterfacesAndOtherCombinationsAreRefused(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Vouchers", 3103, "173.0")
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)

	for name, c := range map[string]struct {
		mode string
		ids  []string
		code string
	}{
		"none with an interface":        {"NONE", []string{a}, "NONE_TAKES_NO_INTERFACES"},
		"dedicated with two":            {"DEDICATED", []string{a, b}, "DEDICATED_NEEDS_ONE"},
		"dedicated with none":           {"DEDICATED", nil, "DEDICATED_NEEDS_ONE"},
		"an unknown mode":               {"EVERYTHING", []string{a}, "MODE_UNKNOWN"},
		"an interface that is not here": {"DEDICATED", []string{"00000000-0000-4000-8000-000000000001"}, "INTERFACE_NOT_FOUND"},
	} {
		_, pv := f.preview(t, net, c.mode, c.ids...)
		if !has(codes(pv, "blockers"), c.code) {
			t.Errorf("%s: blockers %v, want %s", name, pv["blockers"], c.code)
		}
	}
	// an interface that is not in service cannot be selected: it could not check anybody
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces SET lifecycle_state='AUTH_DISABLED' WHERE id=$1::uuid`, b); err != nil {
		t.Fatal(err)
	}
	if _, pv := f.preview(t, net, "DEDICATED", b); !has(codes(pv, "blockers"), "INTERFACE_NOT_IN_SERVICE") {
		t.Fatalf("an out-of-service interface was selectable: %v", pv["blockers"])
	}
	if st, _ := f.setRouting(t, net, "NONE"); st != 200 {
		t.Fatal("NONE refused")
	}
	if f.mapRows(t, net) != 0 || f.routing(t, net)["mode"] != "NONE" {
		t.Fatal("NONE left interfaces behind or no policy")
	}
	// NONE is a decision: the network is no longer undecided
	_, r := f.do(t, "GET", "/pms-routing", nil)
	for _, u := range r["unmapped_guest_networks"].([]any) {
		if u.(map[string]any)["guest_network_id"] == net {
			t.Fatal("a PMS-free network is still listed as unconfigured")
		}
	}
}

func TestIntegration_Routing_ClearReturnsTheNetworkToUndecided(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Rooms", 3104, "174.0")
	a, _, _ := f.seedInterface(t)
	if st, _ := f.setRouting(t, net, "DEDICATED", a); st != 200 {
		t.Fatal("set")
	}
	if st, _ := f.clearRouting(t, net); st != 200 {
		t.Fatal("clear")
	}
	if f.mapRows(t, net) != 0 || f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1::uuid`, net) != 0 {
		t.Fatal("clearing left routing or a policy behind")
	}
}

// Only a site administrator changes routing; reading and previewing follow the same boundary.
func TestIntegration_Routing_OnlyTheOwnerOfRoutingChangesIt(t *testing.T) {
	f := newAPI(t, "site_viewer")
	net := "00000000-0000-4000-8000-0000000000a1"
	a := "00000000-0000-4000-8000-0000000000a2"
	if st, _ := f.do(t, "GET", "/pms-routing", nil); st != 200 {
		t.Fatalf("a viewer cannot read the routing: %d", st)
	}
	if st, _ := f.preview(t, net, "DEDICATED", a); st != 403 {
		t.Fatalf("a viewer previewed a change: %d", st)
	}
	if st, _ := f.do(t, "PUT", "/pms-routing/"+net, map[string]any{"pms_interface_id": a}); st != 403 {
		t.Fatalf("a viewer changed the routing: %d", st)
	}
	if st, _ := f.do(t, "DELETE", "/pms-routing/"+net, nil); st != 403 {
		t.Fatalf("a viewer cleared the routing: %d", st)
	}
}

// One authoritative assessment: blocked while a candidate cannot answer, ready when all can, and it clears by itself when corrected.
func TestIntegration_Readiness_IsNetworkAwareAndClearsWhenCorrected(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Resort", 3106, "176.0")
	vouch := f.mkNet(t, "Vouchers", 3107, "177.0")
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	f.feed(t, a, true)
	f.feed(t, b, false)
	if st, _ := f.setRouting(t, net, "SHARED_STRICT", a, b); st != 200 {
		t.Fatal("shared")
	}
	if st, _ := f.setRouting(t, vouch, "NONE"); st != 200 {
		t.Fatal("none")
	}
	ass := func() map[string]any {
		_, r := f.do(t, "GET", "/pms-routing/readiness", nil)
		return r
	}
	state := func(r map[string]any, id string) string {
		for _, n := range r["networks"].([]any) {
			if m := n.(map[string]any); m["network_id"] == id {
				return m["state"].(string)
			}
		}
		return ""
	}
	r := ass()
	if state(r, net) != "BLOCKED" || state(r, vouch) != "OFF" {
		t.Fatalf("shared=%s vouchers=%s: %v", state(r, net), state(r, vouch), r["networks"])
	}
	issues := r["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["network_id"] != net {
		t.Fatalf("issues: %v", issues)
	}
	// the dashboard's Needs attention is the same verdict
	attention := func() []attentionItem {
		ov := overviewResp{}
		ov.PMS = f.app.overviewPMS(context.Background())
		return deriveAttention(attentionInputFrom(ov))
	}
	found := false
	for _, a := range attention() {
		if a.ID == "room-signin:"+net {
			found = true
			if a.Href != "/pms-interfaces" || !strings.Contains(a.Detail, "cannot sign in") {
				t.Fatalf("the warning does not say what is affected and where to fix it: %+v", a)
			}
		}
		if strings.Contains(a.ID, vouch) {
			t.Fatalf("the PMS-free network raised a warning: %+v", a)
		}
	}
	if !found {
		t.Fatalf("the dashboard does not carry the blocked network: %v", attention())
	}
	// corrected: B recovers
	f.feed(t, b, true)
	r = ass()
	if state(r, net) != "READY" || len(r["issues"].([]any)) != 0 {
		t.Fatalf("a corrected network still warns: %v %v", state(r, net), r["issues"])
	}
	for _, a := range attention() {
		if strings.HasPrefix(a.ID, "room-signin:") {
			t.Fatalf("the dashboard still warns after the correction: %+v", a)
		}
	}
	// the network list reads the same verdict
	_, nov := f.do(t, "GET", "/network/overview", nil)
	for _, n := range nov["networks"].([]any) {
		m := n.(map[string]any)
		if m["id"] == net && (m["room_sign_in"] == nil || m["room_sign_in"].(map[string]any)["state"] != "READY") {
			t.Fatalf("client networks disagree with the assessment: %v", m["room_sign_in"])
		}
	}
}

// A PMS that also keeps a route to an archived network does not warn while it serves an appropriate active network.
func TestIntegration_Readiness_AnArchivedRouteAloneIsNotAWarning(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	a, _, _ := f.seedInterface(t)
	f.feed(t, a, true)
	if st, b := f.setRouting(t, h.vlans[0], "DEDICATED", a); st != 200 {
		t.Fatalf("route %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatalf("stage %d %v", st, b)
	}
	f.applyAndKeep(t, fn)
	var def string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM guest_networks WHERE tenant_id=$1 AND enabled`, f.tenant).Scan(&def); err != nil {
		t.Fatal(err)
	}
	_, r := f.do(t, "GET", "/pms-routing/readiness", nil)
	// the default network has not been decided: that is the only warning, and it is about the active network
	issues := r["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["network_id"] != def || issues[0].(map[string]any)["href"] != "/pms-routing" {
		t.Fatalf("issues: %v", issues)
	}
	// the operator chooses: dedicated to the same PMS. Now nothing warns although the PMS also keeps its archived route.
	if st, b := f.setRouting(t, def, "DEDICATED", a); st != 200 {
		t.Fatalf("choose %d %v", st, b)
	}
	_, r = f.do(t, "GET", "/pms-routing/readiness", nil)
	if len(r["issues"].([]any)) != 0 || r["overall"] != "READY" {
		t.Fatalf("issues %v overall %v", r["issues"], r["overall"])
	}
	for _, i := range r["interfaces"].([]any) {
		if m := i.(map[string]any); m["pms_interface_id"] == a && (m["state"] != "SERVING" || m["archived_or_off_routes"].(float64) < 1) {
			t.Fatalf("interface summary: %v", m)
		}
	}
}

// A reset that is staged, cancelled, applied-and-lapsed, rolled back or confirmed never changes any routing; after a confirmed one the room sign-in step is offered,
// separately, and nothing changes until the operator confirms it.
func TestIntegration_Reset_NeverChangesPmsRoutingAndOffersRoomSignInOnlyAfterConfirmation(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	f.feed(t, a, true)
	f.feed(t, b, true)
	if st, body := f.setRouting(t, h.vlans[0], "SHARED_STRICT", a, b); st != 200 {
		t.Fatalf("shared %d %v", st, body)
	}
	if st, body := f.setRouting(t, h.vlans[1], "DEDICATED", a); st != 200 {
		t.Fatalf("dedicated %d %v", st, body)
	}
	snapshot := func() string {
		var s string
		if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE((SELECT string_agg(guest_network_id||':'||pms_interface_id||':'||is_default::text, ',' ORDER BY guest_network_id, pms_interface_id)
			FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1::uuid),'') || '|' ||
			COALESCE((SELECT string_agg(guest_network_id||':'||mode, ',' ORDER BY guest_network_id) FROM iam_v2.guest_network_pms_policy WHERE tenant_id=$1::uuid AND guest_network_id IN
			  (SELECT id FROM guest_networks WHERE tenant_id=$1::uuid AND name <> 'Client Wi-Fi')),'')`, f.tenant).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	same := func(what string) {
		t.Helper()
		if got := snapshot(); got != before {
			t.Fatalf("%s changed PMS routing:\n before %s\n after  %s", what, before, got)
		}
	}

	st, sg := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName})
	if st != 201 {
		t.Fatalf("stage %d %v", st, sg)
	}
	same("staging")
	_, rec := f.do(t, "GET", "/network/recovery", nil)
	if l := rec["latest"].(map[string]any); l["room_sign_in_step"] != nil {
		t.Fatalf("the room sign-in step is offered before the reset is confirmed: %v", l)
	}
	f.do(t, "DELETE", "/network/changes/"+sg["group_id"].(string), nil)
	same("cancelling")
	if st, _ := f.do(t, "POST", "/network/recovery/stage", map[string]any{"op": "full_reset", "confirm_site_name": h.siteName}); st != 201 {
		t.Fatal("restage")
	}
	if st, _ := f.apply(t); st != 200 {
		t.Fatal("apply")
	}
	same("applying")
	fn.timeOut(t)
	f.do(t, "GET", "/network/recovery", nil)
	same("a lapsed confirmation")
	if st, _ := f.apply(t); st != 200 {
		t.Fatal("apply 2")
	}
	if st, _ := f.rollbackRev(t, fn.revision); st != 200 {
		t.Fatal("rollback")
	}
	same("a rollback")
	_, rec = f.do(t, "GET", "/network/recovery", nil)
	if l, _ := rec["latest"].(map[string]any); l != nil && l["room_sign_in_step"] != nil {
		t.Fatalf("the step appeared after a rollback: %v", l)
	}

	f.applyAndKeep(t, fn)
	same("confirming")
	_, rec = f.do(t, "GET", "/network/recovery", nil)
	step := rec["latest"].(map[string]any)["room_sign_in_step"].(map[string]any)
	if step["offered"] != true || step["policy"] != "" || step["network_name"] != "Client Wi-Fi" {
		t.Fatalf("the optional step is not offered on the Default Network: %v", step)
	}
	def := step["network_id"].(string)
	if f.mapRows(t, def) != 0 {
		t.Fatal("the reset routed the Default Network to a PMS by itself")
	}
	// the operator chooses; only now does the Default Network get a policy, and the archived networks keep theirs
	if st, body := f.setRouting(t, def, "SHARED_STRICT", a, b); st != 200 {
		t.Fatalf("choose %d %v", st, body)
	}
	after := snapshot()
	if f.mapRows(t, h.vlans[0]) != 2 || f.mapRows(t, h.vlans[1]) != 1 {
		t.Fatalf("choosing for the Default Network disturbed the archived networks' routing: %s", after)
	}
	_, rec = f.do(t, "GET", "/network/recovery", nil)
	if s := rec["latest"].(map[string]any)["room_sign_in_step"].(map[string]any); s["offered"] != false || s["policy"] != "SHARED_STRICT" {
		t.Fatalf("the step still offers after the decision: %v", s)
	}
}

// A topology change keeps its own network's routing and previews its consequences; it never merges routing across networks.
func TestIntegration_Impact_PortConversionAndReplacementNameWhatTheyAffect(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	f.feed(t, a, true)
	f.feed(t, b, true)
	if st, body := f.setRouting(t, h.vlans[0], "DEDICATED", a); st != 200 {
		t.Fatalf("%d %v", st, body)
	}
	if st, body := f.setRouting(t, h.vlans[1], "DEDICATED", b); st != 200 {
		t.Fatalf("%d %v", st, body)
	}

	st, imp := f.do(t, "GET", "/network/guest-networks/"+h.vlans[0]+"/impact?to_type=untagged&to_parent="+h.lan, nil)
	if st != 200 {
		t.Fatalf("impact = %d %v", st, imp)
	}
	aff := imp["affected"].([]any)[0].(map[string]any)
	if aff["room_sign_in"].(map[string]any)["policy"] != "DEDICATED" {
		t.Fatalf("the consequence omits the routing: %v", aff)
	}
	ext := imp["external_requirements"].([]any)
	joined := fmt.Sprint(ext)
	if !strings.Contains(joined, "Switch") || !strings.Contains(joined, "ESXi") || !strings.Contains(joined, "does not change your switch") {
		t.Fatalf("external requirements: %v", ext)
	}

	// Access conversion preview: the VLAN networks being switched off are named with their routing, and nothing is staged.
	st, plan := f.do(t, "POST", "/network/ports/"+h.lan+"/mode", map[string]any{"mode": "access", "preview": true})
	if st == 200 {
		affected := plan["affected"].([]any)
		if len(affected) == 0 {
			t.Fatalf("an access conversion preview names no affected network: %v", plan)
		}
		for _, x := range affected {
			if x.(map[string]any)["room_sign_in"] == nil {
				t.Fatalf("a conversion consequence omits PMS routing: %v", x)
			}
		}
		if len(plan["external_requirements"].([]any)) == 0 {
			t.Fatal("no switch/ESXi requirements in the conversion preview")
		}
	}
	if f.mapRows(t, h.vlans[0]) != 1 || f.mapRows(t, h.vlans[1]) != 1 {
		t.Fatal("previewing changed routing")
	}
}

// The whole routing journey with the server on the Admin Console's own database role: preview, shared apply, readiness, impact, the reset step. A superuser
// connection hides a missing privilege; this is the proof the role holds what the feature reads and writes.
func TestIntegration_Routing_TheWholeJourneyRunsAsTheAdminConsoleRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	h := heavilyReconfigured(t, f, fn)
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	f.feed(t, a, true)
	f.feed(t, b, true)
	f.stayInRoom(t, a, "412")
	f.stayInRoom(t, b, "412")

	rp, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	super := f.app.db
	f.app.db = rp
	defer func() { f.app.db = super }()

	if st, body := f.setRouting(t, h.vlans[0], "SHARED_STRICT", a, b); st != 200 {
		t.Fatalf("shared as svc_edged = %d %v", st, body)
	}
	if st, body := f.setRouting(t, h.vlans[1], "NONE"); st != 200 {
		t.Fatalf("none as svc_edged = %d %v", st, body)
	}
	for _, path := range []string{"/pms-routing", "/pms-routing/readiness", "/network/overview", "/network/default",
		"/network/guest-networks/" + h.vlans[0] + "/impact?to_type=vlan&to_vlan=70"} {
		if st, body := f.do(t, "GET", path, nil); st != 200 {
			t.Fatalf("GET %s as svc_edged = %d %v", path, st, body)
		}
	}
	_, r := f.do(t, "GET", "/pms-routing/readiness", nil)
	if r["overall"] != "READY" && r["overall"] != "PARTIAL" {
		t.Fatalf("as svc_edged the assessment is %v (an unreadable fact must not read as a verdict)", r["overall"])
	}
	if st, body := f.do(t, "POST", "/network/ports/"+h.lan+"/mode", map[string]any{"mode": "access", "preview": true}); st != 200 && st != 409 {
		t.Fatalf("access preview as svc_edged = %d %v", st, body)
	}
	if st, body := f.clearRouting(t, h.vlans[1]); st != 200 {
		t.Fatalf("clear as svc_edged = %d %v", st, body)
	}
}

// Two interfaces whose published revisions carry the same source fingerprint are, by the contract, one property's PMS counted twice. The source-conflicts page detects
// them from the fingerprints, because nothing records those rows by itself; the routing preview and apply use the same detection.
func TestIntegration_Routing_ADetectedDuplicateSourceCannotBeSharedOnOneNetwork(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Resort", 3108, "178.0")
	ctx := context.Background()
	mk := func(label, fingerprint string) string {
		var iface, rev string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind,display_label,lifecycle_state)
			VALUES ($1,$2,'protel-fias',$3,'ACTIVE') RETURNING id::text`, f.tenant, f.site, label).Scan(&iface); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interface_revisions
			(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config,normalization_version,source_fingerprint)
			VALUES ($1,$2,$3::uuid,1,'UTC','UNSET','{}'::jsonb,1,$4) RETURNING id::text`, f.tenant, f.site, iface, fingerprint).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2::uuid WHERE id=$1::uuid`, iface, rev); err != nil {
			t.Fatal(err)
		}
		return iface
	}
	a, b, c := mk("Old link", "same-physical-source"), mk("New link", "same-physical-source"), mk("Spa", "another-source")

	_, pv := f.preview(t, net, "SHARED_STRICT", a, b)
	if !has(codes(pv, "blockers"), "DUPLICATE_SOURCE") {
		t.Fatalf("a detected duplicate source was approved for sharing: %v", pv["blockers"])
	}
	if st, _ := f.setRouting(t, net, "SHARED_STRICT", a, b); st != 409 {
		t.Fatalf("applied = %d", st)
	}
	if f.mapRows(t, net) != 0 {
		t.Fatal("a refused shared set wrote rows")
	}
	// a different pair is unaffected, and so is a network that uses only one of the two
	_, pv = f.preview(t, net, "SHARED_STRICT", a, c)
	if has(codes(pv, "blockers"), "DUPLICATE_SOURCE") {
		t.Fatalf("an unrelated pair was blocked: %v", pv["blockers"])
	}
	_, pv = f.preview(t, net, "DEDICATED", a)
	if has(codes(pv, "blockers"), "DUPLICATE_SOURCE") {
		t.Fatalf("a dedicated route was blocked by a duplicate it does not use: %v", pv["blockers"])
	}
}

// Tagged VLAN networks are independent: each owns its policy, and choosing or changing one never touches another's policy, interfaces or fingerprint.
func TestIntegration_Routing_EveryVLANNetworkOwnsItsOwnPolicy(t *testing.T) {
	f := newAPI(t)
	fn := startFakeNetd(t, f)
	lan := uniqueParent()
	fn.lan = lan
	f.retireRevisions(t)
	st, body := f.do(t, "POST", "/network/ports/"+lan+"/mode", map[string]any{"mode": "trunk", "vlans": []map[string]any{{"vlan_id": 3201, "name": "Rooms"}, {"vlan_id": 3202, "name": "Spa"}, {"vlan_id": 3203, "name": "Conference"}}})
	if st != 201 {
		t.Fatalf("trunk = %d %v", st, body)
	}
	nets := toStrings(body["network_ids"])
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	if st, r := f.setRouting(t, nets[0], "DEDICATED", a); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	if st, r := f.setRouting(t, nets[1], "SHARED_STRICT", a, b); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	if st, r := f.setRouting(t, nets[2], "NONE"); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	before := []string{f.routing(t, nets[0])["fingerprint"].(string), f.routing(t, nets[2])["fingerprint"].(string)}

	// change the middle VLAN twice, to a different PMS and then to none
	if st, r := f.setRouting(t, nets[1], "DEDICATED", b); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	if st, r := f.setRouting(t, nets[1], "NONE"); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	after := []string{f.routing(t, nets[0])["fingerprint"].(string), f.routing(t, nets[2])["fingerprint"].(string)}
	if before[0] != after[0] || before[1] != after[1] {
		t.Fatalf("changing one VLAN's room sign-in changed another's: %v -> %v", before, after)
	}
	if f.routing(t, nets[0])["mode"] != "DEDICATED" || f.routing(t, nets[2])["mode"] != "NONE" || f.mapRows(t, nets[0]) != 1 || f.mapRows(t, nets[2]) != 0 {
		t.Fatal("another VLAN's policy or interfaces moved")
	}
	// and a preview for one network never carries another's interfaces
	_, pv := f.preview(t, nets[2], "DEDICATED", a)
	if rem := pv["removed"].([]any); len(rem) != 0 {
		t.Fatalf("a preview for one VLAN lists another's interfaces as removed: %v", rem)
	}
}

func TestIntegration_Routing_NoRequestCanReplaceOrRemoveAnApprovedRouteWithoutItsPreview(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Resort", 3110, "180.0")
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	if st, r := f.setRouting(t, net, "SHARED_STRICT", a, b); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	want := f.routing(t, net)["fingerprint"]
	intact := func(what string) {
		t.Helper()
		got := f.routing(t, net)
		if got["mode"] != "SHARED_STRICT" || got["fingerprint"] != want || f.mapRows(t, net) != 2 {
			t.Fatalf("%s changed the shared set: mode=%v rows=%d", what, got["mode"], f.mapRows(t, net))
		}
	}

	for name, body := range map[string]map[string]any{
		"the earlier single-interface request":              {"pms_interface_id": a},
		"the earlier request with its routing_mode":         {"pms_interface_id": a, "routing_mode": "MAPPED"},
		"a dedicated replacement with no preview":           {"mode": "DEDICATED", "pms_interface_ids": []string{a}},
		"a dedicated replacement with an empty fingerprint": {"mode": "DEDICATED", "pms_interface_ids": []string{a}, "based_on": ""},
		"a request with no mode":                            {"pms_interface_ids": []string{a}, "based_on": want},
	} {
		st, r := f.do(t, "PUT", "/pms-routing/"+net, body)
		if st != 400 || (r["error"] != "preview_required" && r["error"] != "bad_request") {
			t.Fatalf("%s = %d %v, want 400", name, st, r)
		}
		intact(name)
	}
	if st, r := f.do(t, "PUT", "/pms-routing/"+net, map[string]any{"mode": "DEDICATED", "pms_interface_ids": []string{a}, "based_on": "0000000000000000"}); st != 409 || r["error"] != "routing_changed" {
		t.Fatalf("a replacement against a stale fingerprint = %d %v", st, r)
	}
	intact("a stale replacement")

	if st, r := f.do(t, "DELETE", "/pms-routing/"+net, nil); st != 400 || r["error"] != "preview_required" {
		t.Fatalf("a removal with no fingerprint = %d %v", st, r)
	}
	if st, r := f.do(t, "DELETE", "/pms-routing/"+net+"?based_on=0000000000000000", nil); st != 409 || r["error"] != "routing_changed" {
		t.Fatalf("a removal against a stale fingerprint = %d %v", st, r)
	}
	intact("an unprotected or stale removal")

	// a fingerprint taken from a preview of ANOTHER state does not authorise this change after the set changed underneath it
	_, pv := f.preview(t, net, "DEDICATED", a)
	stale := pv["network"].(map[string]any)["fingerprint"]
	if st, r := f.setRouting(t, net, "SHARED_STRICT", b, a); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	if st, r := f.setRouting(t, net, "NONE"); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	if st, r := f.do(t, "PUT", "/pms-routing/"+net, map[string]any{"mode": "DEDICATED", "pms_interface_ids": []string{a}, "based_on": stale}); st != 409 {
		t.Fatalf("an old preview authorised a later change: %d %v", st, r)
	}
}

func TestIntegration_Routing_ARoutingFromAnOlderRelease_CannotDowngradeASharedSetThroughTheDatabase(t *testing.T) {
	f := newAPI(t)
	net := f.mkNet(t, "Resort", 3111, "181.0")
	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	if st, r := f.setRouting(t, net, "SHARED_STRICT", a, b); st != 200 {
		t.Fatalf("%d %v", st, r)
	}
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1::uuid AND pms_interface_id <> $2::uuid`, net, a); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default,routing_mode)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,true,'MAPPED') ON CONFLICT (guest_network_id, pms_interface_id) DO UPDATE SET is_default=true`, f.tenant, f.site, net, a); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("the replace-with-one write of an older release was accepted against a shared policy")
	}
	if f.routing(t, net)["mode"] != "SHARED_STRICT" || f.mapRows(t, net) != 2 {
		t.Fatal("the shared set did not survive the refused downgrade")
	}
}
