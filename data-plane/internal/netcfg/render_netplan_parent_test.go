package netcfg

import (
	"strings"
	"testing"
)

// THE FIRST UNTAGGED GUEST NETWORK ON A FACTORY-CLEAN APPLIANCE.
//
// netplan refuses to generate when a bridge member is defined nowhere:
//
//	Error in network definition: br-g-xxxx: interface 'ens192' is not defined
//
// The renderer used to declare ethernets only for TRUNK parents, assuming an untagged parent was already
// configured by the base netplan. On a factory-clean appliance the base netplan declares the WAN interface
// and nothing else, so the first untagged network could never apply. Reverting the renderer to
// trunk-parents-only makes every case below fail.

// parentIsDeclared reports whether the rendered YAML declares iface under ethernets.
func parentIsDeclared(yaml, iface string) bool {
	in := false
	for _, line := range strings.Split(yaml, "\n") {
		switch {
		case line == "  ethernets:":
			in = true
			continue
		// any other two-space top-level key ends the ethernets block
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

// A parent shared by an untagged bridge and a tagged VLAN must be declared EXACTLY once: netplan rejects a
// duplicate mapping key, so de-duplication is correctness and not tidiness.
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

// THE RENDER IS FINGERPRINTED TO DECIDE WHETHER THE CONFIGURATION CHANGED, so identical input must produce
// byte-identical output. Map iteration order is random; without sorting, every render looked like a change.
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
	// and the parents must be in sorted order
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
