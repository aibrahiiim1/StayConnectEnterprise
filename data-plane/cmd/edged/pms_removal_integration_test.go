//go:build integration

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func (f *apiFixture) shell(t *testing.T, label string) string {
	t.Helper()
	st, b := f.do(t, http.MethodPost, "/pms-interfaces", map[string]any{"connector_kind": "protel-fias", "display_label": label})
	if st != http.StatusCreated {
		t.Fatalf("create %q = %d %v", label, st, b)
	}
	return b["id"].(string)
}

func (f *apiFixture) removal(t *testing.T, id string) map[string]any {
	t.Helper()
	st, b := f.do(t, http.MethodGet, "/pms-interfaces/"+id+"/removal", nil)
	if st != http.StatusOK {
		t.Fatalf("removal preview = %d %v", st, b)
	}
	return b
}

func (f *apiFixture) remove(t *testing.T, id, outcome string) (int, map[string]any) {
	t.Helper()
	return f.do(t, http.MethodPost, "/pms-interfaces/"+id+"/removal", map[string]any{"outcome": outcome, "reason_code": "NO_LONGER_NEEDED", "password": f.password})
}

func (f *apiFixture) listedIDs(t *testing.T, query string) map[string]bool {
	t.Helper()
	st, b := f.do(t, http.MethodGet, "/pms-interfaces"+query, nil)
	if st != http.StatusOK {
		t.Fatalf("list = %d %v", st, b)
	}
	out := map[string]bool{}
	for _, x := range b["interfaces"].([]any) {
		out[x.(map[string]any)["id"].(string)] = true
	}
	return out
}

func blockerCodes(b map[string]any) string {
	var out []string
	for _, x := range b["blockers"].([]any) {
		out = append(out, x.(map[string]any)["code"].(string))
	}
	return strings.Join(out, ",")
}

func TestIntegration_API_ANeverUsedConnectionIsDeleted(t *testing.T) {
	f := newAPI(t)
	shell := f.shell(t, "Never used")
	if got := f.removal(t, shell)["outcome"]; got != "DELETE" {
		t.Fatalf("an empty shell = %v, want DELETE", got)
	}

	drafted, _, st, b := f.createAuthorPublish(t, "Published, never switched on", "unused-pms.local:5010")
	if st != http.StatusOK {
		t.Fatalf("publish = %d %v", st, b)
	}
	if got := f.removal(t, drafted)["outcome"]; got != "DELETE" {
		t.Fatalf("a published but never activated connection = %v, want DELETE", got)
	}

	if st, b := f.remove(t, shell, "RETIRE"); st != http.StatusConflict || b["error"] != "removal_changed" {
		t.Fatalf("confirming the wrong outcome = %d %v, want 409 removal_changed", st, b)
	}
	if st, b := f.do(t, http.MethodPost, "/pms-interfaces/"+shell+"/removal", map[string]any{"outcome": "DELETE", "reason_code": "X", "password": "wrong"}); st != http.StatusUnauthorized && st != http.StatusForbidden {
		t.Fatalf("wrong password = %d %v", st, b)
	}
	if st, b := f.do(t, http.MethodPost, "/pms-interfaces/"+shell+"/removal", map[string]any{"outcome": "DELETE", "password": f.password}); st != http.StatusBadRequest {
		t.Fatalf("no reason = %d %v", st, b)
	}
	for _, id := range []string{shell, drafted} {
		if st, b := f.remove(t, id, "DELETE"); st != http.StatusOK {
			t.Fatalf("delete %s = %d %v", id, st, b)
		}
		if f.listedIDs(t, "?include=retired")[id] {
			t.Fatalf("%s is still listed after deletion", id)
		}
		if n := f.count(t, `SELECT count(*) FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, id); n != 0 {
			t.Fatalf("%s still exists", id)
		}
		if n := f.count(t, `SELECT count(*) FROM iam_v2.pms_interface_revisions WHERE pms_interface_id=$1::uuid`, id); n != 0 {
			t.Fatalf("%s left revisions behind", id)
		}
		if n := f.auditTargetCount(t, "pms_interface.deleted", id); n != 1 {
			t.Fatalf("%s: %d deletion audit entries, want 1", id, n)
		}
	}
	if st, _ := f.remove(t, shell, "DELETE"); st != http.StatusNotFound {
		t.Fatalf("deleting twice = %d, want 404", st)
	}
}

func TestIntegration_API_AConnectionThatWasSwitchedOnIsRetiredNotDeleted(t *testing.T) {
	f := newAPI(t)
	id, _, st, b := f.createAuthorPublish(t, "Was live once", "was-live.local:5010")
	if st != http.StatusOK {
		t.Fatalf("publish = %d %v", st, b)
	}
	if st, b := f.activate(t, id); st != http.StatusOK {
		t.Fatalf("activate = %d %v", st, b)
	}
	if st, b := f.do(t, http.MethodPost, "/pms-interfaces/"+id+"/lifecycle", map[string]any{"state": "AUTH_DISABLED", "reason_code": "TEST", "password": f.password}); st != http.StatusOK {
		t.Fatalf("switch off = %d %v", st, b)
	}
	pv := f.removal(t, id)
	if pv["outcome"] != "RETIRE" || !pv["facts"].(map[string]any)["ever_activated"].(bool) {
		t.Fatalf("a connection that was switched on once = %v, want RETIRE", pv)
	}
}

func TestIntegration_API_AUsedConnectionIsRetiredAndItsHistoryIsKept(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces SET lifecycle_state='AUTH_DISABLED' WHERE id=$1::uuid`, iface); err != nil {
		t.Fatal(err)
	}
	f.seedStay(t, iface, "RES-KEEP-1", "301", "")

	pv := f.removal(t, iface)
	if pv["outcome"] != "RETIRE" {
		t.Fatalf("a connection with a stay = %v, want RETIRE: %v", pv["outcome"], pv)
	}
	if facts := pv["facts"].(map[string]any); facts["stays"].(float64) != 1 {
		t.Fatalf("facts = %v", facts)
	}
	if st, b := f.remove(t, iface, "DELETE"); st != http.StatusConflict || b["error"] != "removal_changed" {
		t.Fatalf("deleting a used connection = %d %v, want 409 removal_changed", st, b)
	}
	if n := f.count(t, `SELECT count(*) FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, iface); n != 1 {
		t.Fatal("a refused delete removed the connection")
	}

	if st, b := f.remove(t, iface, "RETIRE"); st != http.StatusOK {
		t.Fatalf("retire = %d %v", st, b)
	}
	if f.listedIDs(t, "")[iface] {
		t.Fatal("a retired connection is still in the ordinary list")
	}
	if !f.listedIDs(t, "?include=retired")[iface] {
		t.Fatal("a retired connection is missing from the list that includes retired ones")
	}
	if n := f.count(t, `SELECT count(*) FROM iam_v2.stays WHERE pms_interface_id=$1::uuid`, iface); n != 1 {
		t.Fatalf("retiring changed the stays: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM iam_v2.pms_interface_revisions WHERE pms_interface_id=$1::uuid`, iface); n < 2 {
		t.Fatalf("retiring changed the revisions: %d", n)
	}
	if got := f.removal(t, iface)["outcome"]; got != "RETIRED" {
		t.Fatalf("a retired connection assesses as %v", got)
	}
	if st, _ := f.activate(t, iface); st == http.StatusOK {
		t.Fatal("a retired connection was switched on again")
	}
	if n := f.auditTargetCount(t, "pms_interface.retired", iface); n != 1 {
		t.Fatalf("%d retirement audit entries, want 1", n)
	}
}

func TestIntegration_API_RemovalIsRefusedWhileTheConnectionIsOnOrRoutedTo(t *testing.T) {
	f := newAPI(t)
	live, _, _ := f.seedInterface(t)
	pv := f.removal(t, live)
	if pv["outcome"] != "BLOCKED" || !strings.Contains(blockerCodes(pv), "SWITCH_OFF_FIRST") {
		t.Fatalf("an active connection = %v", pv)
	}
	if st, _ := f.remove(t, live, "RETIRE"); st != http.StatusConflict {
		t.Fatalf("retiring an active connection = %d, want 409", st)
	}

	routed, _, _ := f.seedInterface(t)
	net := f.seedNet(t, "Serving network for removal")
	if st, b := f.setRouting(t, net, "DEDICATED", routed); st != http.StatusOK {
		t.Fatalf("route = %d %v", st, b)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces SET lifecycle_state='AUTH_DISABLED' WHERE id=$1::uuid`, routed); err != nil {
		t.Fatal(err)
	}
	pv = f.removal(t, routed)
	if pv["outcome"] != "BLOCKED" || !strings.Contains(blockerCodes(pv), "ROUTED") {
		t.Fatalf("a routed connection = %v", pv)
	}
	nets := pv["blockers"].([]any)[0].(map[string]any)["networks"].([]any)
	if len(nets) != 1 || nets[0].(map[string]any)["id"] != net {
		t.Fatalf("the blocker does not name the network: %v", nets)
	}
	if n := f.count(t, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE pms_interface_id=$1::uuid`, routed); n != 1 {
		t.Fatal("the assessment changed the routing")
	}
}
