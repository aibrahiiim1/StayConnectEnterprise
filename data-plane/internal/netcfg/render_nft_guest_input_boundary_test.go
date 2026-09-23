package netcfg

// A GUEST NETWORK MAY ADDRESS THE APPLIANCE ONLY ON ITS OWN GATEWAY.
//
// The per-bridge input accepts used to be keyed on the INGRESS INTERFACE alone -- `iifname "br-g20" tcp
// dport 8380 accept` -- and the input hook fires for every packet aimed at ANY local address. So a guest on
// one network reached the appliance on another network's gateway, and on the management address, simply by
// addressing it. Measured on PRE-LIVE before the fix: from a client on VLAN 221, the other network's
// gateway and the portal on it were REACHABLE, and the management address answered ICMP.
//
// The forward chain was never the problem: it already drops guest -> mgmt CIDRs and guest -> @guest_subnets,
// which is why guest-to-guest client isolation held. These tests are about the INPUT path to the appliance
// itself, which those rules never see.
//
// EVERY CASE HERE IS PAIRED. A ruleset that opened nothing would satisfy "the other gateway is not
// reachable" perfectly and take guest internet down with it, so each restriction is asserted next to the
// same-network path it must not break.

import (
	"fmt"
	"strings"
	"testing"
)

// twoNetworks returns two enabled tagged networks on one trunk -- the configuration the boundary is about.
func twoNetworks() []GuestNetwork {
	a := vlan20()
	b := vlan20()
	b.ID, b.Name, b.VLANID = "bbbb", "Conference", 21
	b.BridgeName, b.GatewayIP, b.SubnetCIDR = "br-g21", "10.21.0.1", "10.21.0.0/22"
	b.Pools = []Pool{{StartIP: "10.21.0.100", EndIP: "10.21.3.250"}}
	return []GuestNetwork{a, b}
}

// inputChain returns just the input chain, so a rule in forward cannot satisfy an assertion about input.
func inputChain(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "chain input {")
	if i < 0 {
		t.Fatal("no input chain was rendered")
	}
	rest := out[i:]
	j := strings.Index(rest, "\n\t}")
	if j < 0 {
		t.Fatal("the input chain is not terminated")
	}
	return rest[:j]
}

func TestEveryGuestInputAcceptNamesItsOwnGateway(t *testing.T) {
	in := inputChain(t, string(RenderNftables(twoNetworks(), topoT())))
	for _, line := range strings.Split(in, "\n") {
		l := strings.TrimSpace(line)
		if !strings.Contains(l, "accept") || !strings.Contains(l, "iifname \"br-g") {
			continue
		}
		if !strings.Contains(l, "ip daddr") {
			t.Errorf("a guest-bridge accept with NO destination constraint is exactly the hole this closes:\n  %s", l)
			continue
		}
		// The destination must be that bridge's OWN gateway (broadcast is permitted for DHCP only).
		switch {
		case strings.Contains(l, "\"br-g20\""):
			if !strings.Contains(l, "10.20.0.1") {
				t.Errorf("br-g20 accept does not name 10.20.0.1:\n  %s", l)
			}
			if strings.Contains(l, "10.21.0.1") {
				t.Errorf("br-g20 accept names the OTHER network's gateway:\n  %s", l)
			}
		case strings.Contains(l, "\"br-g21\""):
			if !strings.Contains(l, "10.21.0.1") {
				t.Errorf("br-g21 accept does not name 10.21.0.1:\n  %s", l)
			}
			if strings.Contains(l, "10.20.0.1") {
				t.Errorf("br-g21 accept names the OTHER network's gateway:\n  %s", l)
			}
		}
	}
}

// THE PAIRED HALF: every same-network service a guest needs is still open on its own gateway.
func TestTheRequiredSameNetworkPathsAreStillOpen(t *testing.T) {
	in := inputChain(t, string(RenderNftables(twoNetworks(), topoT())))
	for _, gw := range []string{"10.20.0.1", "10.21.0.1"} {
		for _, want := range []struct{ what, frag string }{
			{"DNS over UDP", fmt.Sprintf("ip daddr %s udp dport 53 accept", gw)},
			{"DNS over TCP", fmt.Sprintf("ip daddr %s tcp dport 53 accept", gw)},
			{"ping of the own gateway", fmt.Sprintf("ip daddr %s icmp type echo-request accept", gw)},
		} {
			if !strings.Contains(in, want.frag) {
				t.Errorf("%s is not open on %s; guests on that network would lose it", want.what, gw)
			}
		}
		// The portal accept carries the configured port, so it is matched on the gateway + dport pair.
		if !strings.Contains(in, fmt.Sprintf("ip daddr %s tcp dport", gw)) {
			t.Errorf("no portal accept on %s", gw)
		}
	}
}

// DHCP IS THE ONE PATH THAT CANNOT BE GATEWAY-ONLY. A client with no address yet sends DISCOVER and REQUEST
// to 255.255.255.255; a gateway-only rule would break address assignment outright, which is the one mistake
// here that takes guest networking down rather than hardening it.
func TestDHCPKeepsTheBroadcastDestination(t *testing.T) {
	in := inputChain(t, string(RenderNftables(twoNetworks(), topoT())))
	for _, gw := range []string{"10.20.0.1", "10.21.0.1"} {
		frag := fmt.Sprintf("ip daddr { %s, 255.255.255.255 } udp dport { 67, 68 } accept", gw)
		if !strings.Contains(in, frag) {
			t.Errorf("DHCP on %s does not accept the broadcast destination; clients could never get a lease", gw)
		}
	}
}

// A NETWORK WITH NO GATEWAY OPENS NOTHING, and that is fail-safe rather than tidy. An empty address would
// render `ip daddr  udp dport ...`, a syntax error -- and this ruleset is applied as ONE atomic `nft -f`, so
// a single malformed line does not break one network, it breaks the whole table and takes every guest
// network with it.
func TestANetworkWithoutAGatewayOpensNothing(t *testing.T) {
	nets := twoNetworks()
	nets[1].GatewayIP = ""
	out := string(RenderNftables(nets, topoT()))
	in := inputChain(t, out)

	if strings.Contains(in, "ip daddr  ") || strings.Contains(in, "ip daddr {  ,") {
		t.Error("an empty gateway rendered a malformed rule; one bad line breaks the entire atomic apply")
	}
	for _, line := range strings.Split(in, "\n") {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "\"br-g21\"") && strings.Contains(l, "accept") {
			t.Errorf("a network with no gateway still opened a service:\n  %s", l)
		}
	}
	if !strings.Contains(in, "br-g21 has no gateway address") {
		t.Error("the installed ruleset does not say why br-g21 has no services; an operator reading it " +
			"would see an absence with no explanation")
	}
	// ...and the healthy network is untouched by its neighbour's incomplete configuration.
	if !strings.Contains(in, "ip daddr 10.20.0.1 udp dport 53 accept") {
		t.Error("one network's missing gateway suppressed another network's services")
	}
}

func TestTheGuestInputBoundaryIsStatedAsARule(t *testing.T) {
	in := inputChain(t, string(RenderNftables(twoNetworks(), topoT())))
	if !strings.Contains(in, "iifname @guest_interfaces drop comment \"guest input is limited to its own gateway\"") {
		t.Error("the boundary is left implicit in the chain policy; it is a decision and belongs in the " +
			"ruleset, where it also makes a later unconstrained accept unreachable for guest traffic")
	}
	// It must come AFTER the accepts, or it would drop the services it is meant to leave alone.
	di := strings.Index(in, "guest input is limited to its own gateway")
	ai := strings.LastIndex(in, "icmp type echo-request accept")
	if di >= 0 && ai >= 0 && di < ai {
		t.Error("the boundary drop precedes the per-network accepts, so it would drop DHCP, DNS and the portal")
	}
}

// WHAT THIS CHANGE MUST NOT HAVE TOUCHED. The two-NIC management path and the forward-chain protections are
// accepted behaviour; a hardening that quietly altered either would be a regression wearing a fix's clothes.
func TestTheManagementAndForwardProtectionsAreUnchanged(t *testing.T) {
	topo := topoT()
	topo.MgmtAddr = "172.21.60.25"
	topo.MgmtCIDRs = []string{"172.21.60.0/24"}
	out := string(RenderNftables(twoNetworks(), topo))

	for _, want := range []string{
		"iifname \"ens160\" tcp dport 22 accept",
		"iifname \"ens160\" ip daddr 172.21.60.25 tcp dport 443 accept",
		"iifname @guest_interfaces ip daddr 172.21.60.0/24 drop comment \"guest -> mgmt blocked\"",
		"iifname @guest_interfaces ip daddr @guest_subnets drop comment \"inter-guest isolation\"",
		"iifname @guest_interfaces meta nfproto ipv6 drop comment \"IPv6 guest bypass guard\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("an accepted protection is missing after the input-chain change:\n  %s", want)
		}
	}
}
