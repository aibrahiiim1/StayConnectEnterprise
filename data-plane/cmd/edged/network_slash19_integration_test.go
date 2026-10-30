//go:build integration

package main

import (
	"fmt"
	"testing"
)

// /19 at the Admin API against a real database: the size rule on every write path, overlap with a /20 or /24 inside a /19, and the decision
// that a switched-on network is never widened in place while a switched-off one may be edited.
func TestIntegration_Network_Slash19SizeOverlapAndNoInPlaceWidening(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()

	for _, bad := range []string{"10.150.0.0/18", "10.150.0.0/17", "10.150.0.0/8"} {
		if st, body := f.do(t, "POST", "/network/guest-networks",
			netBody("Too big "+bad, "untagged", uniqueParent(), 0, bad, "10.150.0.1", "10.150.0.100", "10.150.0.200")); st != 400 || body["error"] != "subnet_too_large" {
			t.Fatalf("create %s = %d %v, want 400 subnet_too_large", bad, st, body)
		}
	}
	if st, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Too small", "untagged", uniqueParent(), 0, "10.150.0.0/31", "10.150.0.1", "10.150.0.0", "10.150.0.1")); st != 400 || body["error"] != "subnet_too_small" {
		t.Fatalf("create /31 = %d %v", st, body)
	}
	// the batch endpoint applies the same rule and creates nothing
	st, body := f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []map[string]any{
		netBody("Fine", "vlan", parent, 21, "10.151.0.0/24", "10.151.0.1", "10.151.0.100", "10.151.0.200"),
		netBody("Huge", "vlan", parent, 22, "10.152.0.0/17", "10.152.0.1", "10.152.0.100", "10.152.0.200"),
	}})
	if st != 400 || body["error"] != "subnet_too_large" {
		t.Fatalf("batch with a /17 = %d %v", st, body)
	}
	var n int
	_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_networks WHERE name IN ('Fine','Huge')`).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused batch created %d networks", n)
	}

	// a /19 is accepted, anywhere in its range at the edges
	st, a := f.do(t, "POST", "/network/guest-networks",
		netBody("Big", "untagged", parent, 0, "10.160.0.0/19", "10.160.0.1", "10.160.0.10", "10.160.31.250"))
	if st != 201 {
		t.Fatalf("create /19 = %d %v", st, a)
	}
	bigID := a["id"].(string)
	// a /24 or /20 inside it is an overlap, from either side
	for _, inner := range []string{"10.160.31.0/24", "10.160.16.0/20"} {
		if st, body := f.do(t, "POST", "/network/guest-networks",
			netBody("Inner "+inner, "vlan", uniqueParent(), 30, inner, fmt.Sprintf("%s1", inner[:len(inner)-4]), inner[:len(inner)-4]+"100", inner[:len(inner)-4]+"200")); st != 409 || body["error"] != "subnet_overlap" {
			t.Fatalf("create %s inside the /19 = %d %v", inner, st, body)
		}
	}
	// an adjacent /19 is fine
	if st, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Next", "untagged", uniqueParent(), 0, "10.160.32.0/19", "10.160.32.1", "10.160.32.10", "10.160.63.250")); st != 201 {
		t.Fatalf("adjacent /19 = %d %v", st, body)
	}

	// an existing /20 that is switched on is NOT widened to the enclosing /19
	st, b := f.do(t, "POST", "/network/guest-networks",
		netBody("Twenty", "untagged", uniqueParent(), 0, "10.170.0.0/20", "10.170.0.1", "10.170.0.10", "10.170.15.250"))
	if st != 201 {
		t.Fatalf("create /20 = %d %v", st, b)
	}
	twID := b["id"].(string)
	st, body = f.do(t, "PUT", "/network/guest-networks/"+twID, map[string]any{"subnet_cidr": "10.170.0.0/19", "gateway_ip": "10.170.0.1",
		"pools": []map[string]string{{"start_ip": "10.170.0.10", "end_ip": "10.170.31.250"}}})
	if st != 409 || body["error"] != "subnet_widen_refused" {
		t.Fatalf("widening a live /20 to /19 = %d %v, want 409 subnet_widen_refused", st, body)
	}
	if _, _, _, subnet := f.networkState(t, twID); subnet != "10.170.0.0/20" {
		t.Fatalf("a refused widening changed the subnet to %s", subnet)
	}
	// an unchanged echo, and a PUT that does not mention the subnet, still work
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+twID, map[string]any{"subnet_cidr": "10.170.0.0/20", "gateway_ip": "10.170.0.1", "description": "echo"}); st != 200 {
		t.Fatalf("an unchanged echo = %d %v", st, body)
	}
	// a wider-than-/19 edit is refused by size, not only by the widening rule
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+twID, map[string]any{"subnet_cidr": "10.170.0.0/18", "gateway_ip": "10.170.0.1"}); st != 400 || body["error"] != "subnet_too_large" {
		t.Fatalf("edit to /18 = %d %v", st, body)
	}
	// switched OFF, the same edit is allowed (it is not live); enabling it is then an ordinary staged change
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+twID+"/disable", nil); st != 200 {
		t.Fatalf("disable = %d", st)
	}
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+twID, map[string]any{"subnet_cidr": "10.170.0.0/19", "gateway_ip": "10.170.0.1",
		"pools": []map[string]string{{"start_ip": "10.170.0.10", "end_ip": "10.170.31.250"}}}); st != 200 {
		t.Fatalf("widening a switched-off /20 = %d %v", st, body)
	}
	_ = bigID
}
