package netcfg

import (
	"fmt"
	"sort"
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
// The renderer does not touch a parent's own address — parents are L2 (no IP here) — but it DOES declare
// every parent it references, because netplan will not resolve a bridge member that nothing defines.
func RenderNetplan(nets []GuestNetwork) []byte {
	var b strings.Builder
	b.WriteString("# StayConnect generated — guest network L2/L3. Do not edit by hand.\n")
	b.WriteString("network:\n  version: 2\n  renderer: networkd\n")

	enabled := sortEnabled(nets)

	// EVERY PARENT THIS FILE REFERENCES MUST ALSO BE DEFINED IN IT.
	//
	// netplan resolves a bridge's `interfaces:` list against the merged configuration, and refuses to
	// generate if a member is not defined anywhere:
	//
	//	Error in network definition: br-g-xxxx: interface 'ens192' is not defined
	//
	// This used to emit ethernets only for TRUNK parents — parents carrying a tagged VLAN — on the
	// assumption, written into the comment above, that an untagged parent was "already-configured by the
	// base netplan". On a factory-clean appliance it is not: the base netplan declares the WAN interface
	// and nothing else, because the guest NIC has no configuration until an operator creates a guest
	// network. So the first untagged guest network on a new appliance always failed to apply, and the
	// error named the operator's interface rather than the file that failed to declare it.
	//
	// A generated file has to stand on its own. Emitting an address-less, optional ethernet for every
	// parent is enough: it declares the interface without claiming an address or disturbing anything the
	// base netplan already says about it (netplan merges per interface).
	//
	// The management and WAN interfaces are still never emitted — validation refuses them as guest
	// parents before rendering is reached.
	parents := make([]string, 0, len(enabled))
	seen := map[string]bool{}
	for _, n := range enabled {
		if n.ParentInterface == "" || seen[n.ParentInterface] {
			continue
		}
		seen[n.ParentInterface] = true
		parents = append(parents, n.ParentInterface)
	}
	// Sorted, because map iteration order is random and this file is fingerprinted to decide whether the
	// configuration actually changed. Unstable output would make every render look like a change.
	sort.Strings(parents)
	if len(parents) > 0 {
		b.WriteString("  ethernets:\n")
		for _, p := range parents {
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
