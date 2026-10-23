package netcfg

import (
	"fmt"
	"sort"
	"strings"
)

// RenderNetplan produces a netplan YAML defining, for each enabled guest network, the VLAN sub-interface (tagged) and
// the bridge carrying the L3 gateway. Management and WAN interfaces are never emitted, so applying it cannot disturb
// management connectivity.
// Untagged: the bridge enslaves the parent directly. Tagged: parent.VID vlan device -> bridge (the parent may also
// carry an untagged bridge). Parents are L2 here but are still declared, because netplan will not resolve a bridge
// member that nothing defines.
func RenderNetplan(nets []GuestNetwork) []byte {
	var b strings.Builder
	b.WriteString("# StayConnect generated — guest network L2/L3. Do not edit by hand.\n")
	b.WriteString("network:\n  version: 2\n  renderer: networkd\n")

	enabled := sortEnabled(nets)

	// Every parent this file references must also be defined in it: netplan refuses to generate when a bridge member is
	// defined nowhere, and a factory-clean appliance's base netplan declares only the WAN interface. An address-less,
	// optional ethernet per parent declares it without claiming an address (netplan merges per interface; optional keeps
	// an unplugged NIC from holding up boot).
	// Parents are sorted: map order is random and this file is fingerprinted to detect changes.
	parents := make([]string, 0, len(enabled))
	seen := map[string]bool{}
	for _, n := range enabled {
		if n.ParentInterface == "" || seen[n.ParentInterface] {
			continue
		}
		seen[n.ParentInterface] = true
		parents = append(parents, n.ParentInterface)
	}
	sort.Strings(parents)
	if len(parents) > 0 {
		b.WriteString("  ethernets:\n")
		for _, p := range parents {
			fmt.Fprintf(&b, "    %s:\n      dhcp4: no\n      dhcp6: no\n      optional: true\n", p)
		}
	}

	var vlans []GuestNetwork
	for _, n := range enabled {
		if n.NetworkType == "vlan" {
			vlans = append(vlans, n)
		}
	}
	if len(vlans) > 0 {
		b.WriteString("  vlans:\n")
		for _, n := range vlans {
			dev := VLANIfaceName(n.ParentInterface, n.VLANID)
			fmt.Fprintf(&b, "    %s:\n      id: %d\n      link: %s\n      dhcp4: no\n      dhcp6: no\n",
				dev, n.VLANID, n.ParentInterface)
		}
	}

	// Bridges, only when there is at least one: an empty "bridges:" mapping is invalid netplan YAML (the legacy-only case).
	if len(enabled) > 0 {
		b.WriteString("  bridges:\n")
		for _, n := range enabled {
			var member string
			if n.NetworkType == "vlan" {
				member = VLANIfaceName(n.ParentInterface, n.VLANID)
			} else {
				member = n.ParentInterface
			}
			fmt.Fprintf(&b, "    %s:\n", n.BridgeName)
			fmt.Fprintf(&b, "      interfaces: [%s]\n", member)
			fmt.Fprintf(&b, "      addresses:\n        - %s\n", gatewayCIDR(n))
			b.WriteString("      dhcp4: no\n      dhcp6: no\n")
			b.WriteString("      parameters:\n        stp: false\n        forward-delay: 0\n")
		}
	}
	return []byte(b.String())
}

// gatewayCIDR combines the gateway host address with the subnet prefix length (10.20.0.1 + 10.20.0.0/22 -> 10.20.0.1/22).
func gatewayCIDR(n GuestNetwork) string {
	if n.PrefixLen > 0 {
		return fmt.Sprintf("%s/%d", n.GatewayIP, n.PrefixLen)
	}
	if i := strings.IndexByte(n.SubnetCIDR, '/'); i >= 0 {
		return n.GatewayIP + n.SubnetCIDR[i:]
	}
	return n.GatewayIP + "/24"
}
