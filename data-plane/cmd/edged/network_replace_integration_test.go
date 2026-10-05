//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// CLIENT NETWORKS: SEVERAL VLANS AT ONCE, AND A TOPOLOGY CHANGE THAT IS STAGED BEFORE IT IS LIVE.

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

func (f *apiFixture) networkState(t *testing.T, id string) (enabled bool, netType string, vlan *int, subnet string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT enabled, network_type, vlan_id, subnet_cidr::text FROM guest_networks WHERE id=$1`, id).
		Scan(&enabled, &netType, &vlan, &subnet); err != nil {
		t.Fatalf("read network %s: %v", id, err)
	}
	return
}

func (f *apiFixture) replacementState(t *testing.T, replID string) (state string, successor *string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT state, successor_network_id::text FROM iam_v2.guest_network_replacements WHERE id=$1`, replID).
		Scan(&state, &successor); err != nil {
		t.Fatalf("read replacement %s: %v", replID, err)
	}
	return
}

// seedReplaceable creates an untagged network with a pool, a reservation and a PMS route.
func (f *apiFixture) seedReplaceable(t *testing.T, parent, subnet, gw string) (id, iface string) {
	t.Helper()
	ctx := context.Background()
	st, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Lobby", "untagged", parent, 0, subnet, gw, strings.TrimSuffix(subnet, ".0/24")+".100", strings.TrimSuffix(subnet, ".0/24")+".200"))
	if st != 201 {
		t.Fatalf("create = %d %v", st, body)
	}
	id = body["id"].(string)
	if _, err := f.pool.Exec(ctx, `INSERT INTO dhcp_reservations (guest_network_id, mac, reserved_ip)
		VALUES ($1,'aa:bb:cc:00:00:01',$2::inet)`, id, strings.TrimSuffix(subnet, ".0/24")+".50"); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id::text`, f.tenant, f.site).Scan(&iface); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map (tenant_id,site_id,guest_network_id,pms_interface_id,is_default,routing_mode)
		VALUES ($1,$2,$3,$4,true,'MAPPED')`, f.tenant, f.site, id, iface); err != nil {
		t.Fatal(err)
	}
	return id, iface
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

	status, body = f.do(t, "POST", "/network/guest-networks/batch", map[string]any{"networks": []any{
		netBody("A", "vlan", parent, 120, "10.120.0.0/24", "10.120.0.1", "10.120.0.100", "10.120.0.200"),
		netBody("B", "vlan", parent, 120, "10.121.0.0/24", "10.121.0.1", "10.121.0.100", "10.121.0.200"),
	}})
	if status != 400 || body["error"] != "duplicate_vlan" {
		t.Fatalf("duplicate VLAN batch = %d %v", status, body)
	}
}

// THE CENTRAL PROPERTY: staging changes nothing at all.
func TestIntegration_Network_StagingAReplacementChangesNothing(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	id, iface := f.seedReplaceable(t, parent, "10.130.0.0/24", "10.130.0.1")
	before := f.networkCount(t)

	status, body := f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 30, "reason": "port moved to a trunk"})
	if status != 201 {
		t.Fatalf("stage = %d %v", status, body)
	}
	replID := body["replacement_id"].(string)
	if body["state"] != "PENDING" {
		t.Fatalf("staged state = %v, want PENDING", body["state"])
	}
	carries := body["carries"].(map[string]any)
	if carries["pools"] != float64(1) || carries["reservations"] != float64(1) || carries["pms_routes"] != float64(1) {
		t.Fatalf("carries = %v, want 1/1/1", carries)
	}

	// Nothing moved: the network is still enabled, still untagged, and no successor exists.
	enabled, netType, vlan, _ := f.networkState(t, id)
	if !enabled || netType != "untagged" || vlan != nil {
		t.Fatalf("staging changed the live network: enabled=%v type=%s vlan=%v", enabled, netType, vlan)
	}
	if f.networkCount(t) != before {
		t.Fatal("staging created a network row; it must create nothing until the apply")
	}
	var routed string
	_ = f.pool.QueryRow(context.Background(), `SELECT pms_interface_id::text FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, id).Scan(&routed)
	if routed != iface {
		t.Fatal("staging disturbed the PMS route")
	}

	// A second request for the same network is refused.
	if st, b := f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 31, "reason": "second attempt"}); st != 409 || b["error"] != "replacement_exists" {
		t.Fatalf("second staged replacement = %d %v", st, b)
	}

	// It is listed, and cancelling it leaves nothing behind.
	st, list := f.do(t, "GET", "/network/guest-network-replacements", nil)
	if st != 200 || len(list["data"].([]any)) != 1 {
		t.Fatalf("list = %d %v", st, list)
	}
	if st, b := f.do(t, "DELETE", "/network/guest-network-replacements/"+replID, nil); st != 200 {
		t.Fatalf("cancel = %d %v", st, b)
	}
	if state, _ := f.replacementState(t, replID); state != "CANCELLED" {
		t.Fatalf("after cancel state = %s", state)
	}
}

func TestIntegration_Network_StagingRefusesAConflictUpFront(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	id, _ := f.seedReplaceable(t, parent, "10.131.0.0/24", "10.131.0.1")
	// A second, tagged network already on that trunk.
	if st, b := f.do(t, "POST", "/network/guest-networks",
		netBody("Ext2", "vlan", parent, 40, "10.132.0.0/24", "10.132.0.1", "10.132.0.100", "10.132.0.200")); st != 201 {
		t.Fatalf("seed vlan = %d %v", st, b)
	}
	// ...so moving the first onto VLAN 40 of the same trunk is refused before anything is staged.
	st, b := f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 40, "reason": "clashing vlan"})
	if st != 409 || b["error"] != "topology_conflict" || !strings.Contains(fmt.Sprint(b["message"]), "VLAN 40") {
		t.Fatalf("clashing VLAN = %d %v", st, b)
	}
	// An overlapping new subnet is refused the same way.
	st, b = f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 41, "reason": "clashing subnet",
		"subnet_cidr": "10.132.0.0/25", "gateway_ip": "10.132.0.1",
		"pools": []map[string]string{{"start_ip": "10.132.0.10", "end_ip": "10.132.0.20"}}})
	if st != 409 || b["error"] != "topology_conflict" {
		t.Fatalf("overlapping subnet = %d %v", st, b)
	}
	// The same topology it already has is not a replacement.
	st, b = f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "untagged", "parent_interface": parent, "reason": "no change at all"})
	if st != 400 || b["error"] != "no_topology_change" {
		t.Fatalf("same topology = %d %v", st, b)
	}
	// And a reason is required.
	st, b = f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 42})
	if st != 400 || b["error"] != "reason_required" {
		t.Fatalf("no reason = %d %v", st, b)
	}
}

// THE APPLY MOMENT, AND ITS UNDO. Materialising is what moves the rows; reverting puts them all back.
func TestIntegration_Network_MaterialiseCarriesEverythingAndRevertRestoresIt(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parent := uniqueParent()
	id, iface := f.seedReplaceable(t, parent, "10.133.0.0/24", "10.133.0.1")

	st, body := f.do(t, "POST", "/network/guest-networks/"+id+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 50, "reason": "moved to a trunk"})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	replID := body["replacement_id"].(string)

	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mats, err := f.app.materialisePendingReplacements(ctx, tx, "operator@test")
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mats) != 1 {
		t.Fatalf("materialised %d replacements, want 1", len(mats))
	}
	succ := mats[0].SuccessorID

	// The successor is live with the new topology, the original is retired, and everything followed.
	if enabled, _, _, _ := f.networkState(t, id); enabled {
		t.Fatal("the original is still enabled after materialisation")
	}
	enabled, netType, vlan, subnet := f.networkState(t, succ)
	if !enabled || netType != "vlan" || vlan == nil || *vlan != 50 || subnet != "10.133.0.0/24" {
		t.Fatalf("successor: enabled=%v type=%s vlan=%v subnet=%s", enabled, netType, vlan, subnet)
	}
	var pools, reservations int
	var routed string
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM dhcp_pools WHERE guest_network_id=$1`, succ).Scan(&pools)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM dhcp_reservations WHERE guest_network_id=$1`, succ).Scan(&reservations)
	_ = f.pool.QueryRow(ctx, `SELECT pms_interface_id::text FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, succ).Scan(&routed)
	if pools != 1 || reservations != 1 || routed != iface {
		t.Fatalf("carried pools=%d reservations=%d route=%q", pools, reservations, routed)
	}
	if state, s := f.replacementState(t, replID); state != "APPLIED" || s == nil || *s != succ {
		t.Fatalf("replacement state=%s successor=%v", state, s)
	}

	// ...and the undo is complete.
	if err := f.app.revertMaterialised(ctx, []string{replID}, "test rollback"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if enabled, netType, vlan, _ := f.networkState(t, id); !enabled || netType != "untagged" || vlan != nil {
		t.Fatalf("the original did not come back: enabled=%v type=%s vlan=%v", enabled, netType, vlan)
	}
	var left int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM guest_networks WHERE id=$1`, succ).Scan(&left)
	if left != 0 {
		t.Fatal("the successor row survived the revert")
	}
	if state, s := f.replacementState(t, replID); state != "PENDING" || s != nil {
		t.Fatalf("after revert state=%s successor=%v, want PENDING/nil so it can be retried", state, s)
	}
	// The original keeps its own reservation and route.
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM dhcp_reservations WHERE guest_network_id=$1`, id).Scan(&reservations)
	_ = f.pool.QueryRow(ctx, `SELECT pms_interface_id::text FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, id).Scan(&routed)
	if reservations != 1 || routed != iface {
		t.Fatalf("the original lost something: reservations=%d route=%q", reservations, routed)
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
