package netcfg

import (
	"strings"
	"testing"
)

// untaggedGuest is the shape an operator gets from the wizard when they do NOT enter a VLAN id: the bridge
// enslaves the parent interface directly.
func untaggedGuest() GuestNetwork {
	return GuestNetwork{
		ID: "bbbb", Name: "Guest", Enabled: true, NetworkType: "untagged",
		ParentInterface: "ens192", BridgeName: "br-g-f7d7d90c",
		GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22,
		DHCPMode: DHCPLocal, DNSMode: "appliance", DomainName: "guest.local",
		LeaseDefault: 3600, LeaseMin: 900, LeaseMax: 7200,
		CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
		Pools: []Pool{{StartIP: "10.20.0.100", EndIP: "10.20.3.250"}},
	}
}

// THE FAILURE THIS REPRODUCES.
//
// netplan resolves a bridge's `interfaces:` list against the merged configuration and refuses to generate
// when a member is defined nowhere:
//
//	/etc/netplan/50-stayconnect-guest.yaml:7:20: Error in network definition:
//	br-g-f7d7d90c: interface 'ens192' is not defined
//
// The renderer declared ethernets only for TRUNK parents, assuming an untagged parent was already
// configured by the base netplan. On a factory-clean appliance the base netplan declares the WAN interface
// and nothing else — the guest NIC has no configuration until an operator creates a guest network — so the
// FIRST untagged guest network on a new appliance could never apply.
func TestNetplanDeclaresUntaggedParent(t *testing.T) {
	out := string(RenderNetplan([]GuestNetwork{untaggedGuest()}))

	if !strings.Contains(out, "  ethernets:\n") {
		t.Fatalf("an untagged guest network must declare its parent interface:\n%s", out)
	}
	if !strings.Contains(out, "    ens192:\n") {
		t.Fatalf("parent ens192 must be defined, not just referenced:\n%s", out)
	}
	if !strings.Contains(out, "interfaces: [ens192]") {
		t.Fatalf("the bridge should still enslave the parent directly:\n%s", out)
	}
	// Declared, not addressed: the parent is L2 and must not claim an IP or a default route.
	eth := section(out, "  ethernets:\n", "  bridges:\n")
	if strings.Contains(eth, "addresses:") || strings.Contains(eth, "routes:") {
		t.Fatalf("the parent must be declared address-less:\n%s", eth)
	}
	if !strings.Contains(eth, "optional: true") {
		t.Fatalf("an unplugged guest NIC must not hold up boot; expected optional: true:\n%s", eth)
	}
}

// Every parent referenced anywhere in the file must be defined in it, whichever mix of tagged and untagged
// networks an operator ends up with.
func TestNetplanDeclaresEveryReferencedParent(t *testing.T) {
	second := untaggedGuest()
	second.ID, second.ParentInterface, second.BridgeName = "cccc", "ens224", "br-g-second"
	second.GatewayIP, second.SubnetCIDR = "10.30.0.1", "10.30.0.0/22"

	out := string(RenderNetplan([]GuestNetwork{untaggedGuest(), vlan20(), second}))
	for _, p := range []string{"    ens192:\n", "    ens224:\n"} {
		if !strings.Contains(out, p) {
			t.Fatalf("every referenced parent must be declared; missing %q in:\n%s", strings.TrimSpace(p), out)
		}
	}
	// One declaration each, even though ens192 parents both an untagged and a tagged network.
	if n := strings.Count(out, "    ens192:\n"); n != 1 {
		t.Fatalf("ens192 should be declared exactly once, got %d:\n%s", n, out)
	}
}

// The rendered file is fingerprinted to decide whether the configuration actually changed, so identical
// input must produce identical bytes. Map iteration order previously made this unstable.
func TestNetplanRenderIsDeterministic(t *testing.T) {
	second := untaggedGuest()
	second.ID, second.ParentInterface, second.BridgeName = "cccc", "ens224", "br-g-second"
	third := untaggedGuest()
	third.ID, third.ParentInterface, third.BridgeName = "dddd", "ens256", "br-g-third"
	nets := []GuestNetwork{untaggedGuest(), second, third, vlan20()}

	first := string(RenderNetplan(nets))
	for i := 0; i < 25; i++ {
		if got := string(RenderNetplan(nets)); got != first {
			t.Fatalf("render must be byte-identical for identical input; differed on run %d", i)
		}
	}
}

// A legacy-only apply still emits no ethernets section, because there is nothing to declare.
func TestNetplanEmptyDeclaresNothing(t *testing.T) {
	out := string(RenderNetplan(nil))
	if strings.Contains(out, "ethernets:") {
		t.Fatalf("no networks means no parents to declare:\n%s", out)
	}
}

// section returns the text between two markers, or from the first marker to the end.
func section(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	rest := s[i+len(from):]
	if j := strings.Index(rest, to); j >= 0 {
		return rest[:j]
	}
	return rest
}
