package netcfg

import (
	"strings"
	"testing"
)

func vlan20() GuestNetwork {
	return GuestNetwork{
		ID: "aaaa", Name: "Rooms", Enabled: true, NetworkType: "vlan",
		ParentInterface: "ens192", VLANID: 20, BridgeName: "br-g20",
		GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22,
		DHCPMode: DHCPLocal, DNSMode: "appliance", DomainName: "guest.local",
		LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200,
		CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools: []Pool{{StartIP: "10.20.0.100", EndIP: "10.20.3.250"}},
	}
}

func availAll() map[string]bool { return map[string]bool{"ens192": true, "ens160": true} }

func topoT() Topology {
	t := DefaultTopology()
	t.MgmtInterface = "ens160"
	t.WANInterface = "ens160"
	return t
}

func hasCode(iss []Issue, code string) bool {
	for _, i := range iss {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestValidValid(t *testing.T) {
	iss := ValidateOne(vlan20(), topoT(), availAll(), "")
	if len(iss) != 0 {
		t.Fatalf("expected no issues, got %+v", iss)
	}
}

func TestVLANRange(t *testing.T) {
	n := vlan20()
	n.VLANID = 5000
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "vlan_out_of_range") {
		t.Fatal("expected vlan_out_of_range")
	}
	n.VLANID = 0
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "vlan_out_of_range") {
		t.Fatal("expected vlan_out_of_range for 0 on a vlan network")
	}
}

func TestPoolOutsideSubnet(t *testing.T) {
	n := vlan20()
	n.Pools = []Pool{{StartIP: "10.99.0.100", EndIP: "10.99.0.200"}}
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "pool_outside_subnet") {
		t.Fatal("expected pool_outside_subnet")
	}
}

func TestGatewayInPool(t *testing.T) {
	n := vlan20()
	n.Pools = []Pool{{StartIP: "10.20.0.1", EndIP: "10.20.0.200"}}
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "pool_contains_gateway") {
		t.Fatal("expected pool_contains_gateway")
	}
}

func TestPoolReversed(t *testing.T) {
	n := vlan20()
	n.Pools = []Pool{{StartIP: "10.20.0.200", EndIP: "10.20.0.100"}}
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "pool_reversed") {
		t.Fatal("expected pool_reversed")
	}
}

func TestPoolOverlap(t *testing.T) {
	n := vlan20()
	n.Pools = []Pool{
		{StartIP: "10.20.0.100", EndIP: "10.20.1.100"},
		{StartIP: "10.20.1.50", EndIP: "10.20.2.0"},
	}
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "pool_overlap") {
		t.Fatal("expected pool_overlap")
	}
}

func TestGatewayOutsideSubnet(t *testing.T) {
	n := vlan20()
	n.GatewayIP = "10.99.0.1"
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "gateway_outside_subnet") {
		t.Fatal("expected gateway_outside_subnet")
	}
}

func TestProtectedInterface(t *testing.T) {
	n := vlan20()
	n.ParentInterface = "ens160" // WAN/mgmt
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "protected_interface") {
		t.Fatal("expected protected_interface")
	}
}

func TestMissingParent(t *testing.T) {
	n := vlan20()
	n.ParentInterface = "eth-nope"
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "interface_not_found") {
		t.Fatal("expected interface_not_found")
	}
}

func TestReservationInPool(t *testing.T) {
	n := vlan20()
	n.Reservations = []Reservation{{MAC: "aa:bb:cc:dd:ee:ff", ReservedIP: "10.20.0.150", Enabled: true}}
	if !hasCode(ValidateOne(n, topoT(), availAll(), ""), "reservation_in_pool") {
		t.Fatal("expected reservation_in_pool")
	}
}

func TestReservationOK(t *testing.T) {
	n := vlan20()
	n.Reservations = []Reservation{{MAC: "aa:bb:cc:dd:ee:ff", ReservedIP: "10.20.0.10", Enabled: true}}
	if len(ValidateOne(n, topoT(), availAll(), "")) != 0 {
		t.Fatal("reservation outside pool should be valid")
	}
}

func TestDuplicateVLAN(t *testing.T) {
	a := vlan20()
	bnet := vlan20()
	bnet.Name = "Other"
	bnet.BridgeName = "br-g20b"
	bnet.SubnetCIDR = "10.21.0.0/22"
	bnet.GatewayIP = "10.21.0.1"
	res := ValidateSet([]GuestNetwork{a, bnet}, topoT(), availAll())
	if !hasCode(res.Issues, "duplicate_vlan") {
		t.Fatalf("expected duplicate_vlan, got %+v", res.Issues)
	}
}

func TestSubnetOverlap(t *testing.T) {
	a := vlan20()
	bnet := vlan20()
	bnet.Name = "Other"
	bnet.VLANID = 30
	bnet.BridgeName = "br-g30"
	// overlapping subnet
	res := ValidateSet([]GuestNetwork{a, bnet}, topoT(), availAll())
	if !hasCode(res.Issues, "subnet_overlap") {
		t.Fatalf("expected subnet_overlap, got %+v", res.Issues)
	}
}

func TestMultiVLANValid(t *testing.T) {
	a := vlan20()
	bnet := vlan20()
	bnet.Name = "Conf"
	bnet.VLANID = 40
	bnet.BridgeName = "br-g40"
	bnet.GatewayIP = "10.40.0.1"
	bnet.SubnetCIDR = "10.40.0.0/24"
	bnet.PrefixLen = 24
	bnet.Pools = []Pool{{StartIP: "10.40.0.50", EndIP: "10.40.0.220"}}
	res := ValidateSet([]GuestNetwork{a, bnet}, topoT(), availAll())
	if !res.OK {
		t.Fatalf("two disjoint VLANs should validate, got %+v", res.Issues)
	}
}

func TestKeaRenderOption114(t *testing.T) {
	dhcp4 := RenderKeaDhcp4([]GuestNetwork{vlan20()}, topoT(), "/var/lib/kea/leases.csv", "/run/kea/sock")
	subnets := dhcp4["subnet4"].([]map[string]any)
	if len(subnets) != 1 {
		t.Fatalf("want 1 subnet, got %d", len(subnets))
	}
	opts := subnets[0]["option-data"].([]map[string]any)
	var found string
	for _, o := range opts {
		if o["name"] == "v4-captive-portal" {
			found = o["data"].(string)
		}
	}
	if found != "http://10.20.0.1:8380/" {
		t.Fatalf("option 114 wrong: %q", found)
	}
}

func TestKeaExcludesExternal(t *testing.T) {
	n := vlan20()
	n.DHCPMode = DHCPExternal
	dhcp4 := RenderKeaDhcp4([]GuestNetwork{n}, topoT(), "/x", "/y")
	if len(dhcp4["subnet4"].([]map[string]any)) != 0 {
		t.Fatal("external DHCP network must not be served by Kea")
	}
}

func TestNftRenderConcatSet(t *testing.T) {
	out := string(RenderNftables([]GuestNetwork{vlan20()}, topoT()))
	if !strings.Contains(out, "type ifname . ipv4_addr") {
		t.Fatal("auth set must be concatenated ifname . ipv4_addr")
	}
	if !strings.Contains(out, "iifname . ip saddr @auth_ipv4 accept") {
		t.Fatal("forward auth rule must use the concat match")
	}
	if !strings.Contains(out, "dnat ip to 10.20.0.1:8380") {
		t.Fatal("captive DNAT must target the network gateway")
	}
	if !strings.Contains(out, "ip saddr 10.20.0.0/22 oifname \"ens160\" masquerade") {
		t.Fatal("per-network masquerade missing")
	}
}

func TestNetplanRenderVLAN(t *testing.T) {
	out := string(RenderNetplan([]GuestNetwork{vlan20()}))
	for _, want := range []string{"ens192.20:", "id: 20", "link: ens192", "br-g20:", "10.20.0.1/22"} {
		if !strings.Contains(out, want) {
			t.Fatalf("netplan missing %q in:\n%s", want, out)
		}
	}
}

func TestNetplanEmptyIsValid(t *testing.T) {
	// legacy-only apply => zero netd-managed networks => must NOT emit an
	// empty "bridges:" (invalid netplan YAML).
	out := string(RenderNetplan(nil))
	if strings.Contains(out, "bridges:") {
		t.Fatalf("empty netplan must not contain a bridges section:\n%s", out)
	}
	if !strings.Contains(out, "version: 2") {
		t.Fatal("netplan must still be a valid document")
	}
}

func TestKeaHasLeaseHook(t *testing.T) {
	dhcp4 := RenderKeaDhcp4([]GuestNetwork{vlan20()}, topoT(), "/x", "/y")
	hooks, ok := dhcp4["hooks-libraries"].([]map[string]any)
	if !ok || len(hooks) == 0 {
		t.Fatal("kea config must load the lease_cmds hook for lease4-get-all")
	}
}

func TestBridgeNameLength(t *testing.T) {
	// IFNAMSIZ is 16 (15 usable). Every generated name must fit.
	if n := BridgeNameFor("vlan", 4094, "id"); len(n) > 15 {
		t.Fatalf("vlan bridge name too long: %q", n)
	}
	if n := BridgeNameFor("untagged", 0, "some-long-uuid-value"); len(n) > 15 {
		t.Fatalf("untagged bridge name too long: %q", n)
	}
	if n := VLANIfaceName("ens192", 20); len(n) > 15 {
		t.Fatalf("vlan iface name too long: %q", n)
	}
}

func TestAdminBindsToMgmtAddrWhenSet(t *testing.T) {
	topo := DefaultTopology()
	topo.MgmtInterface = "ens160"
	// Shared mgmt/WAN NIC: pin admin 443 to the mgmt IP.
	topo.MgmtAddr = "172.21.60.23"
	out := string(RenderNftables(nil, topo))
	if !strings.Contains(out, `iifname "ens160" ip daddr 172.21.60.23 tcp dport 443 accept`) {
		t.Fatalf("expected admin 443 pinned to mgmt addr, got:\n%s", out)
	}
	if strings.Contains(out, `iifname "ens160" tcp dport 443 accept comment "Caddy TLS (admin)"`) {
		t.Fatalf("interface-wide admin 443 must not appear when MgmtAddr set:\n%s", out)
	}
	// Empty MgmtAddr keeps the interface-wide accept (separate-NIC installs).
	topo.MgmtAddr = ""
	out = string(RenderNftables(nil, topo))
	if !strings.Contains(out, `iifname "ens160" tcp dport 443 accept`) {
		t.Fatalf("expected interface-wide admin 443 when MgmtAddr empty:\n%s", out)
	}
}

func TestNftReplacesOnlyOwnTableNotWholeRuleset(t *testing.T) {
	out := string(RenderNftables(nil, DefaultTopology()))
	if strings.Contains(out, "flush ruleset") {
		t.Fatalf("generated ruleset must NOT `flush ruleset` (would wipe Docker's nft chains):\n%s", out)
	}
	// atomic replace of only our table
	if !strings.Contains(out, "table inet stayconnect\ndelete table inet stayconnect") {
		t.Fatalf("expected scoped `table …; delete table …` replace of only the stayconnect table:\n%s", out)
	}
}
