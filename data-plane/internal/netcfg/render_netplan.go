package netcfg

import (
	"fmt"
	"strings"
)

// RenderNetplan produces a netplan YAML that defines, for each enabled guest
// network, the VLAN sub-interface (tagged) and the bridge carrying the L3
// gateway. The management and WAN interfaces are NEVER emitted here — this
// file only ever adds guest bridges/VLANs, so applying it cannot disturb
// management connectivity.
//
// Untagged network:   bridge <br> enslaves the parent interface directly.
// Tagged VLAN:         parent.<vid> vlan device -> bridge <br> (parent stays a
//
//	trunk and may also carry an untagged bridge).
//
// The renderer intentionally does not touch the parent's own address; parents
// are expected to be L2 trunks (no IP) or already-configured by the base
// netplan for the legacy untagged case.
func RenderNetplan(nets []GuestNetwork) []byte {
	var b strings.Builder
	b.WriteString("# StayConnect generated — guest network L2/L3. Do not edit by hand.\n")
	b.WriteString("network:\n  version: 2\n  renderer: networkd\n")

	enabled := sortEnabled(nets)

	// Collect trunk parents (parents that carry at least one VLAN) so we emit
	// them as address-less ethernets set UP.
	trunkParents := map[string]bool{}
	for _, n := range enabled {
		if n.NetworkType == "vlan" {
			trunkParents[n.ParentInterface] = true
		}
	}
	if len(trunkParents) > 0 {
		b.WriteString("  ethernets:\n")
		for p := range trunkParents {
			fmt.Fprintf(&b, "    %s:\n      dhcp4: no\n      dhcp6: no\n      optional: true\n", p)
		}
	}

	// VLAN devices
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

	// Bridges (one per network). Only emit the section when there is at least
	// one — an empty "bridges:" mapping is invalid netplan YAML (this is the
	// legacy-only case, where the single legacy bridge is not netd-managed).
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

// gatewayCIDR combines the gateway host address with the subnet prefix length,
// e.g. gateway 10.20.0.1 + subnet 10.20.0.0/22 -> 10.20.0.1/22.
func gatewayCIDR(n GuestNetwork) string {
	if n.PrefixLen > 0 {
		return fmt.Sprintf("%s/%d", n.GatewayIP, n.PrefixLen)
	}
	// derive from subnet_cidr
	if i := strings.IndexByte(n.SubnetCIDR, '/'); i >= 0 {
		return n.GatewayIP + n.SubnetCIDR[i:]
	}
	return n.GatewayIP + "/24"
}
