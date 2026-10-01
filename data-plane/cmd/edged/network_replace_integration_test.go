//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// CLIENT NETWORKS: SEVERAL VLANS AT ONCE, A SAFE TOPOLOGY CHANGE, AND HISTORY THAT IS KEPT.

func netBody(name, typ, parent string, vlan int, subnet, gw, poolStart, poolEnd string) map[string]any {
	b := map[string]any{
		"name": name, "network_type": typ, "parent_interface": parent,
		"subnet_cidr": subnet, "gateway_ip": gw,
		"pools": []map[string]string{{"start_ip": poolStart, "end_ip": poolEnd}},
	}
	if vlan > 0 {
		b["vlan_id"] = vlan
	}
	return b
}

func uniqueParent() string { return fmt.Sprintf("t%d", time.Now().UnixNano()%1_000_000_000) }

func (f *apiFixture) networkCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM guest_networks WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegration_Network_BatchCreatesATrunkOfVLANsAllOrNothing(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	status, body := f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []any{
		netBody("Ext1", "vlan", parent, 111, "10.111.0.0/24", "10.111.0.1", "10.111.0.100", "10.111.0.200"),
		netBody("Ext2", "vlan", parent, 112, "10.112.0.0/24", "10.112.0.1", "10.112.0.100", "10.112.0.200"),
		netBody("Staff", "vlan", parent, 113, "10.113.0.0/24", "10.113.0.1", "10.113.0.100", "10.113.0.200"),
	}})
	if status != 201 {
		t.Fatalf("batch = %d %v", status, body)
	}
	if got := len(body["networks"].([]any)); got != 3 || f.networkCount(t) != 3 {
		t.Fatalf("created %d networks (rows %d), want 3", got, f.networkCount(t))
	}

	// One overlapping subnet refuses the whole batch: nothing from it is created.
	before := f.networkCount(t)
	status, body = f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []any{
		netBody("Ext3", "vlan", parent, 114, "10.114.0.0/24", "10.114.0.1", "10.114.0.100", "10.114.0.200"),
		netBody("Clash", "vlan", parent, 115, "10.111.0.0/25", "10.111.0.1", "10.111.0.10", "10.111.0.20"),
	}})
	if status != 409 || body["error"] != "subnet_overlap" {
		t.Fatalf("overlapping batch = %d %v, want 409 subnet_overlap", status, body)
	}
	if f.networkCount(t) != before {
		t.Fatal("a refused batch left networks behind; it must be all or nothing")
	}

	// The same VLAN twice on one port is refused before anything is written.
	status, body = f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []any{
		netBody("A", "vlan", parent, 120, "10.120.0.0/24", "10.120.0.1", "10.120.0.100", "10.120.0.200"),
		netBody("B", "vlan", parent, 120, "10.121.0.0/24", "10.121.0.1", "10.121.0.100", "10.121.0.200"),
	}})
	if status != 400 || body["error"] != "duplicate_vlan" {
		t.Fatalf("duplicate VLAN batch = %d %v", status, body)
	}
}

func TestIntegration_Network_ReplaceMovesAnUntaggedLANOntoAVLANAndCarriesItsSetup(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parent := uniqueParent()
	status, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Lobby", "untagged", parent, 0, "10.130.0.0/24", "10.130.0.1", "10.130.0.100", "10.130.0.200"))
	if status != 201 {
		t.Fatalf("create = %d %v", status, body)
	}
	oldID := body["id"].(string)
	// A MAC reservation and a PMS route that must follow the network.
	if _, err := f.pool.Exec(ctx, `INSERT INTO dhcp_reservations (guest_network_id, mac, reserved_ip) VALUES ($1,'aa:bb:cc:00:00:01','10.130.0.50')`, oldID); err != nil {
		t.Fatal(err)
	}
	var iface string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id::text`, f.tenant, f.site).Scan(&iface); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map (tenant_id,site_id,guest_network_id,pms_interface_id,is_default,routing_mode)
		VALUES ($1,$2,$3,$4,true,'MAPPED')`, f.tenant, f.site, oldID, iface); err != nil {
		t.Fatal(err)
	}

	// In-place topology edits are still refused.
	if st, b := f.do(t, "PUT", "/network/guest-networks/"+oldID, map[string]any{"name": "Lobby", "network_type": "vlan", "vlan_id": 30,
		"subnet_cidr": "10.130.0.0/24", "gateway_ip": "10.130.0.1"}); st != 409 {
		t.Fatalf("in-place topology change = %d %v, want 409", st, b)
	}

	// No reason, no replacement.
	if st, b := f.do(t, "POST", "/network/guest-networks/"+oldID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 30}); st != 400 || b["error"] != "reason_required" {
		t.Fatalf("replace without reason = %d %v", st, b)
	}

	status, body = f.do(t, "POST", "/network/guest-networks/"+oldID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 30, "reason": "switch port moved to a trunk"})
	if status != 201 {
		t.Fatalf("replace = %d %v", status, body)
	}
	newID := body["id"].(string)
	carried := body["carried"].(map[string]any)
	if carried["pools"] != float64(1) || carried["reservations"] != float64(1) || carried["pms_routes"] != float64(1) {
		t.Fatalf("carried = %v, want pools 1, reservations 1, pms_routes 1", carried)
	}

	var oldEnabled, newEnabled bool
	var newType, newSubnet string
	var newVLAN int
	_ = f.pool.QueryRow(ctx, `SELECT enabled FROM guest_networks WHERE id=$1`, oldID).Scan(&oldEnabled)
	_ = f.pool.QueryRow(ctx, `SELECT enabled, network_type, vlan_id, subnet_cidr::text FROM guest_networks WHERE id=$1`, newID).
		Scan(&newEnabled, &newType, &newVLAN, &newSubnet)
	if oldEnabled || !newEnabled || newType != "vlan" || newVLAN != 30 || newSubnet != "10.130.0.0/24" {
		t.Fatalf("after replace: old enabled=%v, new enabled=%v %s vlan %d %s", oldEnabled, newEnabled, newType, newVLAN, newSubnet)
	}
	var routed string
	_ = f.pool.QueryRow(ctx, `SELECT pms_interface_id::text FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, newID).Scan(&routed)
	if routed != iface {
		t.Fatal("the PMS route did not follow the network")
	}
	var audited int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='network.guest.replaced' AND target_id=$1`, newID).Scan(&audited)
	if audited != 1 {
		t.Fatalf("replacement audited %d times, want 1", audited)
	}

	// Replacing with the same topology is not a replacement.
	if st, b := f.do(t, "POST", "/network/guest-networks/"+newID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 30, "reason": "no change"}); st != 400 || b["error"] != "no_topology_change" {
		t.Fatalf("same-topology replace = %d %v", st, b)
	}
}

func TestIntegration_Network_TheSameVLANOnAnotherPortGetsItsOwnBridge(t *testing.T) {
	f := newAPI(t)
	p1, p2 := uniqueParent(), uniqueParent()+"b"
	vlan := 3000 + int(time.Now().UnixNano()%1000)
	st, b1 := f.do(t, "POST", "/network/guest-networks",
		netBody("Ext1", "vlan", p1, vlan, "10.140.0.0/24", "10.140.0.1", "10.140.0.100", "10.140.0.200"))
	if st != 201 {
		t.Fatalf("first = %d %v", st, b1)
	}
	st, b2 := f.do(t, "POST", "/network/guest-networks",
		netBody("Ext2", "vlan", p2, vlan, "10.141.0.0/24", "10.141.0.1", "10.141.0.100", "10.141.0.200"))
	if st != 201 {
		t.Fatalf("the same VLAN on another port was refused: %d %v", st, b2)
	}
	br1, br2 := b1["bridge_name"].(string), b2["bridge_name"].(string)
	if br1 == br2 || !strings.HasPrefix(br2, fmt.Sprintf("br-g%d-", vlan)) || len(br2) > 15 {
		t.Fatalf("bridges %q and %q: the second must be a distinct, <=15-char br-g%d-xxxx", br1, br2, vlan)
	}
}

func TestIntegration_Network_ANetworkWithClientHistoryIsKeptNotDeleted(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	st, b := f.do(t, "POST", "/network/guest-networks",
		netBody("Old", "untagged", uniqueParent(), 0, "10.150.0.0/24", "10.150.0.1", "10.150.0.100", "10.150.0.200"))
	if st != 201 {
		t.Fatalf("create = %d %v", st, b)
	}
	id := b["id"].(string)
	if _, err := f.pool.Exec(ctx, `WITH d AS (INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac)
		VALUES ($1,$2,gen_random_uuid(),'aa:bb:cc:00:00:99') RETURNING id)
		INSERT INTO iam_v2.device_network_appearances (tenant_id,site_id,device_id,guest_network_id)
		SELECT $1,$2,d.id,$3 FROM d`, f.tenant, f.site, id); err != nil {
		t.Fatal(err)
	}
	if st, b := f.do(t, "POST", "/network/guest-networks/"+id+"/disable", nil); st != 200 {
		t.Fatalf("disable = %d %v", st, b)
	}
	st, b = f.do(t, "DELETE", "/network/guest-networks/"+id, nil)
	if st != 409 || b["error"] != "kept_as_history" {
		t.Fatalf("delete of a network with history = %d %v, want 409 kept_as_history (was a bare 500)", st, b)
	}
}
