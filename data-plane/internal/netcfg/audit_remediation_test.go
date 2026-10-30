package netcfg

import (
	"strings"
	"testing"
)

func goodNet(id, name, br, cidr, gw string, vlan int) GuestNetwork {
	return GuestNetwork{ID: id, Name: name, Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: vlan,
		BridgeName: br, GatewayIP: gw, SubnetCIDR: cidr, DHCPMode: DHCPLocal, DNSMode: "appliance",
		LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200, CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools: []Pool{{StartIP: strings.TrimSuffix(gw, ".1") + ".100", EndIP: strings.TrimSuffix(gw, ".1") + ".200"}}}
}

func issueCodes(iss []Issue) map[string]bool {
	out := map[string]bool{}
	for _, i := range iss {
		out[i.Code] = true
	}
	return out
}

// Wider than /19 is refused (the shaper names a class per host, up to /17; the product stops at /19).
func TestSubnetWiderThan19IsRefused(t *testing.T) {
	n := goodNet("a", "A", "br-g20", "10.20.0.0/18", "10.20.0.1", 20)
	if !issueCodes(ValidateOne(n, DefaultTopology(), nil, nil, ""))["subnet_too_large"] {
		t.Fatal("a /18 was accepted")
	}
	for _, cidr := range []string{"10.20.0.0/19", "10.20.0.0/20"} {
		n.SubnetCIDR = cidr
		if issueCodes(ValidateOne(n, DefaultTopology(), nil, nil, ""))["subnet_too_large"] {
			t.Fatalf("a %s was refused", cidr)
		}
	}
}

func TestLeaseTimersAreBounded(t *testing.T) {
	for _, tc := range []struct{ def, min, max int }{{-5, 0, 0}, {30, 0, 0}, {3600, 0, 9_000_000}, {3600, -1, 0}} {
		n := goodNet("a", "A", "br-g20", "10.20.0.0/24", "10.20.0.1", 20)
		n.LeaseDefault, n.LeaseMin, n.LeaseMax = tc.def, tc.min, tc.max
		if !issueCodes(ValidateOne(n, DefaultTopology(), nil, nil, ""))["lease_out_of_range"] {
			t.Errorf("lease %+v was accepted", tc)
		}
	}
	n := goodNet("a", "A", "br-g20", "10.20.0.0/24", "10.20.0.1", 20)
	if issueCodes(ValidateOne(n, DefaultTopology(), nil, nil, ""))["lease_out_of_range"] {
		t.Fatal("ordinary lease timers were refused")
	}
}

func TestCustomDNSServersAreOpenedPreAuth(t *testing.T) {
	n := goodNet("a", "A", "br-g20", "10.20.0.0/24", "10.20.0.1", 20)
	n.DNSMode, n.DNSServers = "custom", []string{"172.21.1.10", "9.9.9.9", "garbage", "9.9.9.9"}
	other := goodNet("b", "B", "br-g30", "10.30.0.0/24", "10.30.0.1", 30)
	out := string(RenderNftables([]GuestNetwork{n, other}, DefaultTopology()))
	want := `iifname "br-g20" ip daddr { 172.21.1.10, 9.9.9.9 } meta l4proto { tcp, udp } th dport 53 accept`
	i := strings.Index(out, want)
	if i < 0 {
		t.Fatalf("custom DNS not opened:\n%s", out)
	}
	if j := strings.Index(out, `drop comment "guest -> mgmt blocked"`); j < 0 || j < i {
		t.Fatal("the custom DNS accept must precede the management drop (the hotel resolver may be in a management range)")
	}
	if strings.Count(out, `comment "custom DNS (pre-auth)"`) != 1 {
		t.Fatal("a network on appliance DNS was opened to custom resolvers")
	}
}

func TestClientIsolationIsRendered(t *testing.T) {
	n := goodNet("a", "A", "br-g20", "10.20.0.0/24", "10.20.0.1", 20)
	out := string(RenderNftables([]GuestNetwork{n}, DefaultTopology()))
	if strings.Contains(out, `"client isolation"`) {
		t.Fatal("isolation rendered while off")
	}
	n.ClientIsolation = true
	out = string(RenderNftables([]GuestNetwork{n}, DefaultTopology()))
	rule := `iifname "br-g20" oifname "br-g20" drop comment "client isolation"`
	fwd := out[strings.Index(out, "chain forward"):]
	i := strings.Index(fwd, rule)
	if i < 0 || i > strings.Index(fwd, "ct state established,related accept") {
		t.Fatalf("isolation missing or after the established accept:\n%s", fwd)
	}
}

func TestUnboundAnswersPortalNamesPerNetwork(t *testing.T) {
	a := goodNet("a", "A", "br-g20", "10.20.0.0/24", "10.20.0.1", 20)
	b := goodNet("b", "B", "br-g30", "10.30.0.0/24", "10.30.0.1", 30)
	out := string(RenderUnbound([]GuestNetwork{a, b}))
	for _, want := range []string{
		"access-control-view: 10.20.0.0/24 sc-br-g20",
		"access-control-view: 10.30.0.0/24 sc-br-g30",
		"view:\n    name: \"sc-br-g20\"\n    view-first: yes\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	view20 := out[strings.Index(out, `name: "sc-br-g20"`):strings.Index(out, `name: "sc-br-g30"`)]
	if !strings.Contains(view20, "IN A 10.20.0.1") || strings.Contains(view20, "10.30.0.1") {
		t.Fatalf("VLAN 20's view answers with another gateway:\n%s", view20)
	}
	server := out[:strings.Index(out, "view:")]
	if strings.Contains(server, "local-data: \"portal.stayconnect.local.") {
		t.Fatal("the global zone still publishes every gateway")
	}
}

func TestKeaSubnetIDsAreStable(t *testing.T) {
	a := goodNet("6f1c0d8e-1111-4222-8333-000000000001", "A", "br-g20", "10.20.0.0/24", "10.20.0.1", 20)
	b := goodNet("6f1c0d8e-1111-4222-8333-000000000002", "B", "br-g30", "10.30.0.0/24", "10.30.0.1", 30)
	c := goodNet("6f1c0d8e-1111-4222-8333-000000000003", "C", "br-g40", "10.40.0.0/24", "10.40.0.1", 40)
	id := func(nets []GuestNetwork, net GuestNetwork) any {
		for _, s := range RenderKeaDhcp4(nets, DefaultTopology(), "/x", "/y")["subnet4"].([]map[string]any) {
			if s["subnet"] == net.SubnetCIDR {
				return s["id"]
			}
		}
		return nil
	}
	before := id([]GuestNetwork{a, b, c}, c)
	after := id([]GuestNetwork{b, c}, c)
	if before == nil || before != after {
		t.Fatalf("removing a network renumbered another: %v -> %v", before, after)
	}
	ids := StableSubnetIDs([]GuestNetwork{a, b, c})
	seen := map[int]bool{}
	for _, v := range ids {
		if v < 1 || seen[v] {
			t.Fatalf("ids not unique and positive: %v", ids)
		}
		seen[v] = true
	}
}
