package netcfg

// The portal address and the portal's public names. The portal is served over HTTPS under <label>.gw.onegate.work or
// the hotel's verified domain, whose public A record is one private /32 that exists only inside this appliance.
// Everything here is pure; netd owns the side effects.

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// HostAddr is one IPv4 address the appliance carries on any interface; the portal address must not fall inside any
// of their subnets.
type HostAddr struct {
	Iface string `json:"iface"`
	CIDR  string `json:"cidr"`
}

// rfc1918 are the only blocks the portal address may come from: documentation blocks (RFC 5737) are not for local
// use, and a public address would be routed off the appliance.
var rfc1918 = []*net.IPNet{mustCIDR("10.0.0.0/8"), mustCIDR("172.16.0.0/12"), mustCIDR("192.168.0.0/16")}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// ParsePortalIP accepts "10.255.255.1" or "10.255.255.1/32" and returns the bare IPv4 address, or nil.
func ParsePortalIP(s string) net.IP {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		ip, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil
		}
		if ones, bits := n.Mask.Size(); ones != 32 || bits != 32 {
			return nil
		}
		return ip.To4()
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	return ip.To4()
}

// ValidatePortalIP decides whether vip may be the portal address given these guest networks and host addresses;
// "" is valid (HTTP fallback only). Rules:
//   - IPv4 /32 only (the A record has no AAAA; guest IPv6 is refused).
//   - RFC 1918 only.
//   - Not inside any guest network subnet, enabled or not: a disabled network is one click from enabled, and the
//     address would be handed out by DHCP or answered by ARP.
//   - Not inside any subnet on any interface (WAN, management, Docker, guest bridge): a /32 on the dummy interface
//     wins every local route and would steal an address from a real network. The dummy interface itself is excluded.
//
// Every failure carries code portal_ip_* on field portal_ip.
func ValidatePortalIP(vip string, nets []GuestNetwork, host []HostAddr) []Issue {
	var r ValidationResult
	r.OK = true
	vip = strings.TrimSpace(vip)
	if vip == "" {
		return nil
	}
	ip := ParsePortalIP(vip)
	if ip == nil {
		r.add("portal_ip", "portal_ip_invalid", fmt.Sprintf("portal address %q is not a single IPv4 address (/32)", vip))
		return r.Issues
	}
	private := false
	for _, b := range rfc1918 {
		if b.Contains(ip) {
			private = true
		}
	}
	if !private {
		r.add("portal_ip", "portal_ip_not_private",
			fmt.Sprintf("portal address %s must be a private (RFC 1918) address: 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16", ip))
		return r.Issues
	}
	if ip[3] == 0 || ip[3] == 255 {
		// A .0 or .255 reads as a network or broadcast address to resolvers and humans; refusing it is free.
		r.add("portal_ip", "portal_ip_invalid", fmt.Sprintf("portal address %s ends in .0 or .255; choose a host address", ip))
	}
	for _, n := range nets {
		_, sub, err := net.ParseCIDR(n.SubnetCIDR)
		if err != nil {
			continue
		}
		if sub.Contains(ip) {
			r.add("portal_ip", "portal_ip_overlap",
				fmt.Sprintf("portal address %s is inside client network %q (%s); it must be outside every client network", ip, n.Name, n.SubnetCIDR))
		}
	}
	for _, h := range host {
		if h.Iface == PortalIface {
			continue
		}
		hip, sub, err := net.ParseCIDR(h.CIDR)
		if err != nil || hip.To4() == nil {
			continue
		}
		if sub.Contains(ip) {
			r.add("portal_ip", "portal_ip_overlap",
				fmt.Sprintf("portal address %s is inside %s, which interface %s carries; it must not overlap any network on this appliance", ip, sub, h.Iface))
		}
	}
	return r.Issues
}

// hostnameRE is a lower-case DNS name with at least two labels and an alphabetic TLD. Names are rendered into a URL
// handed to every guest and into the resolver config, so anything not plainly a hostname is treated as absent.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func ValidHostname(s string) bool {
	return len(s) >= 4 && len(s) <= 253 && hostnameRE.MatchString(s)
}

// captiveAPIURL is the RFC 8908 captive API URL while HTTPS is active. It requires scd saying HTTPS is active, a real
// canonical hostname, and a portal address in this render (the name resolves to it).
func captiveAPIURL(topo Topology) (string, bool) {
	p := topo.PortalTLS
	if !p.HTTPSActive || !ValidHostname(p.CanonicalHostname) || ParsePortalIP(topo.PortalVIP) == nil {
		return "", false
	}
	return "https://" + p.CanonicalHostname + "/captive-portal/api", true
}

// CaptivePortalURL is what DHCP option 114 carries: the RFC 8908 captive API on the canonical hostname while HTTPS is
// active, and nothing otherwise, so the option is left out. Option 114 used to carry the plain-HTTP portal URL on the
// HTTP fallback, and that stopped devices signing in by themselves: a phone that receives option 114 trusts it instead
// of its own connectivity probes, RFC 8908 requires HTTPS, so a plain-HTTP URL is discarded and the phone neither opens
// it nor probes. Captured on the test appliance on 2026-11-01, VLAN 90: an iPhone and an Android 11 phone both asked
// for option 114, received http://10.90.0.1:8380/ and then sent no captive request of any kind. Without the option they
// fall back to their probes (captive.apple.com, generate_204, connecttest), which the gateway redirect answers.
func CaptivePortalURL(gatewayIP string, topo Topology) string {
	_ = gatewayIP
	if u, ok := captiveAPIURL(topo); ok {
		return u
	}
	return ""
}

// portalHTTPPort is the HTTP portal port the portal address redirects 80 to.
func portalHTTPPort(topo Topology) int {
	if topo.PortalHTTPPort > 0 {
		return topo.PortalHTTPPort
	}
	return 8380
}

// PortalHostnames returns the names the guest resolver answers with the portal address: the OneGate hostname
// and, only while it is VERIFIED, the hotel's domain. Invalid names are dropped, duplicates collapse.
func PortalHostnames(p PortalTLS) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range []string{p.SharedHostname, p.OneGateHostname, p.CustomDomain} {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if !ValidHostname(h) || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// RenderUnboundPortal produces the separate resolver fragment (stayconnect-portal.conf) answering the portal's public
// names with the portal address. The appliance answers locally because a public resolver's private answer can be
// withheld by DNS-rebinding protection (contract section 10).
// `static` makes the zone authoritative for these names: A gets the portal address, AAAA gets NODATA (NXDOMAIN would
// say the name does not exist), and nothing is forwarded upstream.
// It is a separate file because it changes at runtime without a network revision and must not disturb the guest
// fragment's listener and access-control config. With no portal address or names it is a header only.
func RenderUnboundPortal(vip string, p PortalTLS) []byte {
	var b strings.Builder
	b.WriteString("# StayConnect generated -- portal hostnames -> portal address. Do not edit by hand.\n")
	ip := ParsePortalIP(vip)
	names := PortalHostnames(p)
	if ip == nil || len(names) == 0 {
		return []byte(b.String())
	}
	b.WriteString("server:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "    local-zone: \"%s.\" static\n", n)
		fmt.Fprintf(&b, "    local-data: \"%s. IN A %s\"\n", n, ip)
	}
	return []byte(b.String())
}
