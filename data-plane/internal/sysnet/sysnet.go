// Package sysnet models, validates and renders netplan for the appliance's own WAN/management and LAN base
// networking (netcfg models the guest overlay). It is side-effect free; netd applies.
package sysnet

import (
	"fmt"
	"net"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// WANConfig is the appliance uplink + management interface.
type WANConfig struct {
	Interface string   `json:"interface"`  // ens160
	Mode      string   `json:"mode"`       // static | dhcp
	IP        string   `json:"ip"`         // e.g. 192.0.2.23
	PrefixLen int      `json:"prefix_len"` // 24
	Gateway   string   `json:"gateway"`    // 172.21.60.1
	DNS       []string `json:"dns"`        // resolvers
}

// LANConfig is the guest-facing LAN; the IP lives on the bridge, never the enslaved NIC.
type LANConfig struct {
	PhysicalInterface string   `json:"physical_interface"` // ens192 (bridge member)
	Bridge            string   `json:"bridge"`             // br-lan (carries the IP)
	IP                string   `json:"ip"`                 // 10.10.0.1
	PrefixLen         int      `json:"prefix_len"`         // 24
	DHCPEnabled       bool     `json:"dhcp_enabled"`
	DHCPStart         string   `json:"dhcp_start"` // 10.10.0.100
	DHCPEnd           string   `json:"dhcp_end"`   // 10.10.0.250
	DHCPLeaseSeconds  int      `json:"dhcp_lease_seconds"`
	DNS               []string `json:"dns"` // handed to clients
}

// Proposal is a partial change: only the sections present are being changed.
type Proposal struct {
	WAN *WANConfig `json:"wan,omitempty"`
	LAN *LANConfig `json:"lan,omitempty"`
}

type Issue struct {
	Field string `json:"field"`
	Code  string `json:"code"`
	Msg   string `json:"message"`
}

type ValidationResult struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues"`
}

func (r *ValidationResult) add(field, code, msg string) {
	r.Issues = append(r.Issues, Issue{Field: field, Code: code, Msg: msg})
	r.OK = false
}

// Merge overlays a proposal onto the current config so cross-section rules see the full intended state.
func Merge(cur WANConfig, curLAN LANConfig, p Proposal) (WANConfig, LANConfig) {
	w, l := cur, curLAN
	if p.WAN != nil {
		if p.WAN.Interface != "" {
			w.Interface = p.WAN.Interface
		}
		w.Mode = p.WAN.Mode
		w.IP = p.WAN.IP
		w.PrefixLen = p.WAN.PrefixLen
		w.Gateway = p.WAN.Gateway
		if p.WAN.DNS != nil {
			w.DNS = p.WAN.DNS
		}
	}
	if p.LAN != nil {
		if p.LAN.PhysicalInterface != "" {
			l.PhysicalInterface = p.LAN.PhysicalInterface
		}
		if p.LAN.Bridge != "" {
			l.Bridge = p.LAN.Bridge
		}
		l.IP = p.LAN.IP
		l.PrefixLen = p.LAN.PrefixLen
		l.DHCPEnabled = p.LAN.DHCPEnabled
		l.DHCPStart = p.LAN.DHCPStart
		l.DHCPEnd = p.LAN.DHCPEnd
		l.DHCPLeaseSeconds = p.LAN.DHCPLeaseSeconds
		if p.LAN.DNS != nil {
			l.DNS = p.LAN.DNS
		}
	}
	return w, l
}

// ValidateFull runs every safety rule against the effective WAN + LAN config.
func ValidateFull(wan WANConfig, lan LANConfig) ValidationResult {
	res := ValidationResult{OK: true}

	// Interface names come from the request and are written into netplan YAML as keys: a closed character set keeps a
	// newline or quote from adding keys (addresses, routes) to the file (audit Phase 8 M9).
	for _, f := range []struct{ field, name string }{
		{"wan.interface", wan.Interface}, {"lan.physical_interface", lan.PhysicalInterface}, {"lan.bridge", lan.Bridge},
	} {
		if f.name == "" {
			continue // a missing name is reported by the rules below, or means "unchanged"
		}
		if msg := netcfg.CheckIfaceName(f.name); msg != "" {
			res.add(f.field, "invalid_interface_name", msg)
		}
	}

	if wan.Mode != "static" && wan.Mode != "dhcp" {
		res.add("wan.mode", "invalid_mode", `WAN mode must be "static" or "dhcp"`)
	}
	static := wan.Mode != "dhcp"
	var wanNet *net.IPNet
	if static {
		if wan.PrefixLen < 1 || wan.PrefixLen > 32 {
			res.add("wan.prefix_len", "invalid_prefix", "WAN prefix length must be 1–32")
		}
		wip := net.ParseIP(wan.IP)
		if wip == nil || wip.To4() == nil {
			res.add("wan.ip", "invalid_ip", "WAN IP is not a valid IPv4 address")
		} else if why := unusableHost(wip); why != "" {
			res.add("wan.ip", "unusable_address", "WAN IP is "+why)
		} else if wan.PrefixLen >= 1 && wan.PrefixLen <= 32 {
			_, wanNet, _ = net.ParseCIDR(fmt.Sprintf("%s/%d", wan.IP, wan.PrefixLen))
			if wanNet != nil {
				if isNetworkAddr(wip, wanNet) {
					res.add("wan.ip", "network_address", "WAN IP is the network address")
				}
				if isBroadcast(wip, wanNet) {
					res.add("wan.ip", "broadcast_address", "WAN IP is the broadcast address")
				}
			}
		}
		if wan.IP == "" {
			res.add("wan.ip", "no_management_ip", "WAN/management IP is required to keep the appliance reachable")
		}
		if wan.Gateway == "" {
			res.add("wan.gateway", "no_gateway", "WAN requires a default gateway")
		} else {
			gw := net.ParseIP(wan.Gateway)
			if gw == nil || gw.To4() == nil {
				res.add("wan.gateway", "invalid_gateway", "WAN gateway is not a valid IPv4 address")
			} else if why := unusableHost(gw); why != "" {
				res.add("wan.gateway", "unusable_address", "WAN gateway is "+why)
			} else if wanNet != nil && !wanNet.Contains(gw) {
				res.add("wan.gateway", "gateway_off_subnet", "WAN gateway is not in the WAN subnet (on-link route required)")
			}
		}
	}
	for _, d := range wan.DNS {
		if ip := net.ParseIP(strings.TrimSpace(d)); ip == nil {
			res.add("wan.dns", "invalid_dns", "invalid WAN DNS address: "+d)
		}
	}

	// The LAN base address is the address of the client network on the LAN port, so it follows the same size rule as any client network:
	// /19 (8190 addresses) at the widest, /30 at the narrowest.
	lanPrefixOK := lan.PrefixLen >= netcfg.MinSubnetPrefix && lan.PrefixLen <= netcfg.MaxSubnetPrefix
	if !lanPrefixOK {
		res.add("lan.prefix_len", "invalid_prefix", fmt.Sprintf("LAN prefix length must be %d-%d (a client network is at most /%d, %d addresses)",
			netcfg.MinSubnetPrefix, netcfg.MaxSubnetPrefix, netcfg.MinSubnetPrefix, netcfg.SubnetHosts(netcfg.MinSubnetPrefix)))
	}
	lip := net.ParseIP(lan.IP)
	var lanNet *net.IPNet
	if lip == nil || lip.To4() == nil {
		res.add("lan.ip", "invalid_ip", "LAN gateway IP is not a valid IPv4 address")
	} else if why := unusableHost(lip); why != "" {
		res.add("lan.ip", "unusable_address", "LAN gateway IP is "+why)
	} else if lanPrefixOK {
		_, lanNet, _ = net.ParseCIDR(fmt.Sprintf("%s/%d", lan.IP, lan.PrefixLen))
		if lanNet != nil {
			if isNetworkAddr(lip, lanNet) {
				res.add("lan.ip", "network_address", "LAN IP cannot be the network address")
			}
			if isBroadcast(lip, lanNet) {
				res.add("lan.ip", "broadcast_address", "LAN IP cannot be the broadcast address")
			}
		}
	}
	for _, d := range lan.DNS {
		if ip := net.ParseIP(strings.TrimSpace(d)); ip == nil {
			res.add("lan.dns", "invalid_dns", "invalid LAN DNS address: "+d)
		}
	}

	if wan.Interface != "" && (wan.Interface == lan.PhysicalInterface || wan.Interface == lan.Bridge) {
		res.add("lan.physical_interface", "iface_conflict", "WAN and LAN cannot share the same interface")
	}
	if lan.Bridge == "" {
		res.add("lan.bridge", "no_bridge", "LAN IP must live on a bridge, not directly on the physical NIC")
	}
	if lan.PhysicalInterface != "" && lan.PhysicalInterface == lan.Bridge {
		res.add("lan.physical_interface", "member_is_bridge", "LAN physical interface cannot equal the bridge")
	}

	// Overlap, not only equality: a wide LAN (up to /19) that contains the WAN/management subnet, or the reverse, would route the
	// client addresses into the uplink and take the management address with it.
	if wanNet != nil && lanNet != nil && (wanNet.Contains(lanNet.IP) || lanNet.Contains(wanNet.IP)) {
		res.add("lan.ip", "subnet_overlap", "WAN and LAN cannot use the same or overlapping subnets")
	}
	if wan.IP != "" && wan.IP == lan.IP {
		res.add("lan.ip", "duplicate_ip", "WAN IP and LAN IP must be different")
	}

	if lan.DHCPEnabled {
		s := net.ParseIP(lan.DHCPStart)
		e := net.ParseIP(lan.DHCPEnd)
		if s == nil || s.To4() == nil {
			res.add("lan.dhcp_start", "invalid_ip", "DHCP start is not a valid IPv4 address")
		}
		if e == nil || e.To4() == nil {
			res.add("lan.dhcp_end", "invalid_ip", "DHCP end is not a valid IPv4 address")
		}
		if lan.DHCPLeaseSeconds < 60 {
			res.add("lan.dhcp_lease_seconds", "lease_too_short", "DHCP lease must be at least 60 seconds")
		}
		if lanNet != nil && s != nil && e != nil {
			if !lanNet.Contains(s) {
				res.add("lan.dhcp_start", "out_of_subnet", "DHCP start is outside the LAN subnet")
			}
			if !lanNet.Contains(e) {
				res.add("lan.dhcp_end", "out_of_subnet", "DHCP end is outside the LAN subnet")
			}
			if cmpIP(s, e) > 0 {
				res.add("lan.dhcp_end", "range_reversed", "DHCP end must be >= DHCP start")
			}
			if lip != nil && inRange(lip, s, e) {
				res.add("lan.dhcp_start", "pool_contains_gateway", "DHCP range must not include the LAN gateway")
			}
			if netIP := lanNet.IP; inRange(netIP.To4(), s, e) {
				res.add("lan.dhcp_start", "pool_contains_network", "DHCP range must not include the network address")
			}
			if bc := broadcastOf(lanNet); inRange(bc, s, e) {
				res.add("lan.dhcp_end", "pool_contains_broadcast", "DHCP range must not include the broadcast address")
			}
		}
	}

	return res
}

// ValidateProposal validates the resulting configuration plus two rules that depend on what the proposal touches:
//   - A WAN-only change is not blocked by an unaddressed base LAN bridge; the LAN is validated in full when
//     the proposal changes it or it is configured.
//   - DHCP on the base LAN is edited on the client-network pages, so a differing DHCP value is refused rather
//     than silently discarded.
func ValidateProposal(wan WANConfig, lan LANConfig, cur LANConfig, p Proposal) ValidationResult {
	res := ValidateFull(wan, lan)
	if p.LAN == nil && strings.TrimSpace(cur.IP) == "" {
		out := ValidationResult{OK: true}
		for _, is := range res.Issues {
			if !strings.HasPrefix(is.Field, "lan.") {
				out.add(is.Field, is.Code, is.Msg)
			}
		}
		res = out
	}
	if p.LAN != nil {
		if p.LAN.DHCPEnabled != cur.DHCPEnabled || p.LAN.DHCPStart != cur.DHCPStart || p.LAN.DHCPEnd != cur.DHCPEnd ||
			(p.LAN.DHCPLeaseSeconds != 0 && p.LAN.DHCPLeaseSeconds != cur.DHCPLeaseSeconds) {
			res.add("lan.dhcp_enabled", "dhcp_not_edited_here",
				"DHCP is configured on the client-network pages (Client networks, DHCP & leases), not on this screen; "+
					"this change would not be applied")
		}
	}
	return res
}

// ManagementURL is where an operator reaches Hotel Admin after a WAN change (Caddy serves TLS on the WAN IP).
func ManagementURL(wan WANConfig) string {
	if wan.IP != "" {
		return "https://" + wan.IP
	}
	return ""
}

// unusableHost says why an address cannot be an interface address or a gateway (audit Phase 8 L14), or "".
func unusableHost(ip net.IP) string {
	switch {
	case ip.IsUnspecified():
		return "the unspecified address"
	case ip.IsLoopback():
		return "a loopback address"
	case ip.IsMulticast():
		return "a multicast address"
	case ip.IsLinkLocalUnicast():
		return "a link-local address"
	case ip.Equal(net.IPv4bcast):
		return "the broadcast address"
	}
	return ""
}

func isNetworkAddr(ip net.IP, n *net.IPNet) bool {
	return ip.Mask(n.Mask).Equal(ip.To4()) && ip.To4().Equal(n.IP.To4())
}

func broadcastOf(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	if ip == nil {
		return nil
	}
	bc := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bc[i] = ip[i] | ^n.Mask[i]
	}
	return bc
}

func isBroadcast(ip net.IP, n *net.IPNet) bool {
	bc := broadcastOf(n)
	return bc != nil && ip.To4() != nil && ip.To4().Equal(bc)
}

func cmpIP(a, b net.IP) int {
	a4, b4 := a.To4(), b.To4()
	for i := 0; i < 4; i++ {
		if a4[i] != b4[i] {
			if a4[i] < b4[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func inRange(ip, lo, hi net.IP) bool {
	if ip == nil || ip.To4() == nil {
		return false
	}
	return cmpIP(ip, lo) >= 0 && cmpIP(ip, hi) <= 0
}

// RenderWANNetplan serializes the WAN interface to a netplan file body.
func RenderWANNetplan(w WANConfig) string {
	var b strings.Builder
	b.WriteString("# StayConnect Hotel Appliance — WAN + Management (managed by the\n")
	b.WriteString("# Hotel Admin network service). Hand edits are overwritten on apply.\n")
	b.WriteString("network:\n  version: 2\n  ethernets:\n")
	fmt.Fprintf(&b, "    %s:\n", yamlName(w.Interface))
	if w.Mode == "dhcp" {
		b.WriteString("      dhcp4: yes\n      dhcp6: no\n")
		return b.String()
	}
	b.WriteString("      dhcp4: no\n      dhcp6: no\n")
	fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", w.IP, w.PrefixLen)
	if w.Gateway != "" {
		b.WriteString("      routes:\n        - to: default\n")
		fmt.Fprintf(&b, "          via: %s\n", w.Gateway)
	}
	if len(w.DNS) > 0 {
		b.WriteString("      nameservers:\n        addresses:\n")
		for _, d := range w.DNS {
			fmt.Fprintf(&b, "          - %s\n", strings.TrimSpace(d))
		}
	}
	return b.String()
}

// RenderLANNetplan serializes the LAN bridge.
func RenderLANNetplan(l LANConfig) string {
	var b strings.Builder
	b.WriteString("# StayConnect Hotel Appliance — LAN bridge (managed by the Hotel Admin\n")
	b.WriteString("# network service). The IP lives on the bridge, never the member NIC.\n")
	b.WriteString("network:\n  version: 2\n  ethernets:\n")
	fmt.Fprintf(&b, "    %s:\n      dhcp4: no\n      dhcp6: no\n", yamlName(l.PhysicalInterface))
	b.WriteString("  bridges:\n")
	fmt.Fprintf(&b, "    %s:\n", yamlName(l.Bridge))
	fmt.Fprintf(&b, "      interfaces: [%s]\n", yamlName(l.PhysicalInterface))
	fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", l.IP, l.PrefixLen)
	b.WriteString("      dhcp4: no\n      dhcp6: no\n")
	b.WriteString("      parameters:\n        stp: false\n        forward-delay: 0\n")
	return b.String()
}

// yamlName writes an interface name as a double-quoted YAML scalar. Validation already restricts the name to
// letters, digits, '.', '_' and '-'; the reduction and the quotes make a value that bypassed it still one scalar.
func yamlName(s string) string {
	return `"` + netcfg.SafeIface(s) + `"`
}
