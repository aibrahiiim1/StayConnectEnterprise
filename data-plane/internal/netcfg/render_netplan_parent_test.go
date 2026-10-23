package netcfg

import (
	"strings"
	"testing"
)

// The first untagged guest network on a factory-clean appliance: netplan refuses to generate when a bridge member is
// defined nowhere, and the base netplan declares only the WAN interface. A trunk-parents-only renderer fails every
// case below.

// parentIsDeclared reports whether the rendered YAML declares iface under ethernets.
func parentIsDeclared(yaml, iface string) bool {
	in := false
	for _, line := range strings.Split(yaml, "\n") {
		switch {
		case line == "  ethernets:":
			in = true
			continue
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(line, ":"):
			in = false
		}
		if in && strings.TrimSpace(line) == iface+":" {
			return true
		}
	}
	return false
}

func TestUntaggedParentIsDeclared(t *testing.T) {
	got := string(RenderNetplan([]GuestNetwork{{
		Name: "guest", NetworkType: "untagged", ParentInterface: "ens192",
		BridgeName: "br-g-00d1fa1a", GatewayIP: "192.168.77.1", SubnetCIDR: "192.168.77.0/24",
		PrefixLen: 24, Enabled: true,
	}}))

	if !parentIsDeclared(got, "ens192") {
		t.Fatalf("untagged parent ens192 is referenced by the bridge but never declared;\nnetplan would refuse this file.\n%s", got)
	}
	if !strings.Contains(got, "interfaces: [ens192]") {
		t.Fatalf("bridge does not enslave the parent directly:\n%s", got)
	}
	// Declaring it must not claim an address on the parent, and must not block boot.
	if !strings.Contains(got, "optional: true") {
		t.Fatalf("parent declaration must be optional so an unplugged guest NIC cannot hold up boot:\n%s", got)
	}
}

func TestTrunkParentStillDeclared(t *testing.T) {
	got := string(RenderNetplan([]GuestNetwork{{
		Name: "vlan-guest", NetworkType: "vlan", ParentInterface: "ens192", VLANID: 30,
		BridgeName: "br-g-vlan30", GatewayIP: "10.30.0.1", SubnetCIDR: "10.30.0.0/24",
		PrefixLen: 24, Enabled: true,
	}}))
	if !parentIsDeclared(got, "ens192") {
		t.Fatalf("trunk parent regressed - it must still be declared:\n%s", got)
	}
	if !strings.Contains(got, "ens192.30:") {
		t.Fatalf("expected the VLAN device to be rendered:\n%s", got)
	}
}

// A parent shared by an untagged bridge and a tagged VLAN is declared exactly once: netplan rejects duplicate mapping keys.
func TestSharedParentDeclaredOnce(t *testing.T) {
	got := string(RenderNetplan([]GuestNetwork{
		{Name: "a", NetworkType: "untagged", ParentInterface: "ens192",
			BridgeName: "br-a", GatewayIP: "192.168.77.1", SubnetCIDR: "192.168.77.0/24", PrefixLen: 24, Enabled: true},
		{Name: "b", NetworkType: "vlan", ParentInterface: "ens192", VLANID: 40,
			BridgeName: "br-b", GatewayIP: "10.40.0.1", SubnetCIDR: "10.40.0.0/24", PrefixLen: 24, Enabled: true},
	}))
	if n := strings.Count(got, "\n    ens192:\n"); n != 1 {
		t.Fatalf("parent declared %d times, want exactly 1 (netplan rejects duplicate keys):\n%s", n, got)
	}
}

// The render is fingerprinted, so identical input must give byte-identical output; map order is random without sorting.
func TestRenderIsDeterministic(t *testing.T) {
	nets := []GuestNetwork{
		{Name: "c", NetworkType: "untagged", ParentInterface: "ens224",
			BridgeName: "br-c", GatewayIP: "192.168.79.1", SubnetCIDR: "192.168.79.0/24", PrefixLen: 24, Enabled: true},
		{Name: "a", NetworkType: "untagged", ParentInterface: "ens192",
			BridgeName: "br-a", GatewayIP: "192.168.77.1", SubnetCIDR: "192.168.77.0/24", PrefixLen: 24, Enabled: true},
		{Name: "b", NetworkType: "vlan", ParentInterface: "ens256", VLANID: 50,
			BridgeName: "br-b", GatewayIP: "10.50.0.1", SubnetCIDR: "10.50.0.0/24", PrefixLen: 24, Enabled: true},
	}
	first := string(RenderNetplan(nets))
	for i := 0; i < 40; i++ {
		if got := string(RenderNetplan(nets)); got != first {
			t.Fatalf("render is not deterministic; the change fingerprint would be meaningless\n--- first ---\n%s\n--- got ---\n%s", first, got)
		}
	}
	iEns192 := strings.Index(first, "\n    ens192:\n")
	iEns224 := strings.Index(first, "\n    ens224:\n")
	iEns256 := strings.Index(first, "\n    ens256:\n")
	if !(iEns192 >= 0 && iEns192 < iEns224 && iEns224 < iEns256) {
		t.Fatalf("parents are not sorted (ens192 < ens224 < ens256):\n%s", first)
	}
}

// A disabled network contributes no bridge, so its parent must not be declared either.
func TestDisabledNetworkDeclaresNoParent(t *testing.T) {
	got := string(RenderNetplan([]GuestNetwork{{
		Name: "off", NetworkType: "untagged", ParentInterface: "ens192",
		BridgeName: "br-off", GatewayIP: "192.168.77.1", SubnetCIDR: "192.168.77.0/24",
		PrefixLen: 24, Enabled: false,
	}}))
	if parentIsDeclared(got, "ens192") {
		t.Fatalf("disabled network must not declare its parent:\n%s", got)
	}
}
