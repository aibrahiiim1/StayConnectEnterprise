//go:build integration

package main

import (
	"context"
	"testing"
)

// Two live client networks never share a subnet or a name (create, edit, enable); a disabled one blocks nothing.
func TestIntegration_Network_EditAndEnableRefuseOverlapAndDuplicateNames(t *testing.T) {
	f := newAPI(t)
	parentA, parentB := uniqueParent(), uniqueParent()+"b"

	st, a := f.do(t, "POST", "/network/guest-networks",
		netBody("Lobby", "untagged", parentA, 0, "10.140.0.0/24", "10.140.0.1", "10.140.0.100", "10.140.0.200"))
	if st != 201 {
		t.Fatalf("create A = %d %v", st, a)
	}
	st, b := f.do(t, "POST", "/network/guest-networks",
		netBody("Pool", "untagged", parentB, 0, "10.141.0.0/24", "10.141.0.1", "10.141.0.100", "10.141.0.200"))
	if st != 201 {
		t.Fatalf("create B = %d %v", st, b)
	}
	aID, bID := a["id"].(string), b["id"].(string)

	if st, body := f.do(t, "POST", "/network/guest-networks",
		netBody(" lobby ", "untagged", uniqueParent()+"c", 0, "10.142.0.0/24", "10.142.0.1", "10.142.0.100", "10.142.0.200")); st != 409 || body["error"] != "duplicate_name" {
		t.Fatalf("create with a duplicate name = %d %v", st, body)
	}
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+bID, map[string]any{
		"subnet_cidr": "10.140.0.0/24", "gateway_ip": "10.140.0.1",
		"pools": []map[string]string{{"start_ip": "10.140.0.100", "end_ip": "10.140.0.200"}}}); st != 409 || body["error"] != "subnet_overlap" {
		t.Fatalf("edit onto a live subnet = %d %v", st, body)
	}
	if _, _, _, subnet := f.networkState(t, bID); subnet != "10.141.0.0/24" {
		t.Fatalf("a refused edit changed the subnet to %s", subnet)
	}
	// rename B to A's name: refused
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+bID, map[string]any{"name": "LOBBY"}); st != 409 || body["error"] != "duplicate_name" {
		t.Fatalf("rename to a live name = %d %v", st, body)
	}

	// Disable B, move it onto A's subnet and name while disabled (allowed: it is not live), then Enable: refused.
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+bID+"/disable", nil); st != 200 {
		t.Fatalf("disable = %d", st)
	}
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+bID, map[string]any{
		"name": "Lobby", "subnet_cidr": "10.140.0.0/24", "gateway_ip": "10.140.0.1",
		"pools": []map[string]string{{"start_ip": "10.140.0.100", "end_ip": "10.140.0.200"}}}); st != 200 {
		t.Fatalf("editing a disabled network = %d %v", st, body)
	}
	if st, body := f.do(t, "POST", "/network/guest-networks/"+bID+"/enable", nil); st != 409 || body["error"] != "subnet_overlap" {
		t.Fatalf("enable onto a live subnet = %d %v", st, body)
	}
	if enabled, _, _, _ := f.networkState(t, bID); enabled {
		t.Fatal("a refused enable left the network enabled")
	}
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+aID+"/disable", nil); st != 200 {
		t.Fatalf("disable A = %d", st)
	}
	if st, body := f.do(t, "POST", "/network/guest-networks/"+bID+"/enable", nil); st != 200 {
		t.Fatalf("enable with no live conflict = %d %v", st, body)
	}
}

// The status counts the sessions on THIS network now, not every device that ever appeared on it.
func TestIntegration_Network_StatusCountsOnlySessionsOnThisNetwork(t *testing.T) {
	f := newAPI(t)
	st, a := f.do(t, "POST", "/network/guest-networks",
		netBody("Status", "untagged", uniqueParent(), 0, "10.143.0.0/24", "10.143.0.1", "10.143.0.100", "10.143.0.200"))
	if st != 201 {
		t.Fatalf("create = %d %v", st, a)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), activeOnNetworkSQL, a["id"].(string)).Scan(&n); err != nil {
		t.Fatalf("the active-session count does not run against the real schema: %v", err)
	}
	if n != 0 {
		t.Fatalf("a new network reports %d active sessions", n)
	}
}
