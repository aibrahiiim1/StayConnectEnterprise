package netcfg

// THE PORTAL ADDRESS AND THE PORTAL'S PUBLIC NAMES (contract docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md
// §2.1, §4, §6).
//
// The portal is served over HTTPS under a publicly trusted name -- <label>.gw.onegate.work, or the hotel's own
// verified domain -- whose public A record is ONE private /32 that exists only inside this appliance. Everything
// in this file is pure: it decides what that address may be, what DHCP option 114 says, and what the guest
// resolver answers for the portal names. netd owns the side effects.

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// HostAddr is one IPv4 address the appliance carries right now, on any interface (WAN, management, a guest
// bridge, a Docker bridge...). The portal address must not fall inside any of their subnets.
type HostAddr struct {
	Iface string `json:"iface"`
	CIDR  string `json:"cidr"` // "172.17.0.1/16"
}

// rfc1918 are the only blocks the portal address may come from. The documentation blocks (RFC 5737) "are not for
// local use" -- that is why the old 192.0.2.1 was replaced -- and a public address would be routed off the
// appliance by every guest's resolver answer.
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

// ValidatePortalIP decides whether vip may be the portal address on an appliance carrying these guest networks
// and these host addresses. "" is valid: it means "no portal address", and the HTTP fallback is all there is.
//
// THE RULES, AND WHY EACH ONE EXISTS:
//
//   - IPv4 /32 only. The public A record has no AAAA, and guest IPv6 is refused outright.
//   - RFC 1918 only. Anything else is either "not for local use" (RFC 5737) or somebody else's public address.
//   - not inside ANY guest network's subnet, enabled or not. A disabled network is one click from enabled, and
//     the moment it is, a client could be handed the portal address by DHCP or the gateway would answer ARP for
//     it on that bridge.
//   - not inside any subnet the appliance carries on ANY interface -- the WAN, the management side, a Docker
//     bridge (172.17.0.0/16 by default), a guest bridge. A /32 on the dummy interface wins every local route, so
//     an overlap would silently steal one address from a real network: a container, the upstream gateway, a
//     management host. The dummy interface itself is excluded, or the address would always overlap itself.
//
// Every failure carries code portal_ip_* on field portal_ip, so edged can show it against the setting.
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
		// Not wrong for a /32 in itself, but a .0 or .255 is what a resolver, a captive helper or a human reads
		// as a network or broadcast address. Refusing it costs nothing and removes a class of confusion.
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

// hostnameRE is a lower-case DNS name with at least two labels and an alphabetic TLD. The names come from the
// database (written by scd from Central's answers); they are rendered into a URL handed to every guest and into
// the resolver's configuration, so anything that is not plainly a hostname is treated as absent rather than
// quoted into either.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// ValidHostname reports whether s is safe to render as a portal hostname.
func ValidHostname(s string) bool {
	return len(s) >= 4 && len(s) <= 253 && hostnameRE.MatchString(s)
}

// captiveAPIURL is the RFC 8908 captive API URL while HTTPS is active, and whether HTTPS may be advertised at all.
//
// THREE CONDITIONS, ALL REQUIRED: scd says HTTPS is active, the canonical hostname is a real hostname, and this
// render carries a portal address. The last one is netd's own fact: the canonical name resolves to the portal
// address, so with no portal address there is nothing for the https URL to reach.
func captiveAPIURL(topo Topology) (string, bool) {
	p := topo.PortalTLS
	if !p.HTTPSActive || !ValidHostname(p.CanonicalHostname) || ParsePortalIP(topo.PortalVIP) == nil {
		return "", false
	}
	return "https://" + p.CanonicalHostname + "/captive-portal/api", true
}

// CaptivePortalURL is what DHCP option 114 carries for one network: the captive API on the canonical hostname
// while the portal's HTTPS is active, otherwise the plain-HTTP portal on that network's own gateway -- exactly
// the URL every earlier release handed out, so the fallback is today's behaviour, unchanged.
func CaptivePortalURL(gatewayIP string, topo Topology) string {
	if u, ok := captiveAPIURL(topo); ok {
		return u
	}
	return PortalURLFor(gatewayIP, topo.PortalHTTPPort)
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
	for _, h := range []string{p.OneGateHostname, p.CustomDomain} {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if !ValidHostname(h) || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// RenderUnboundPortal produces the SEPARATE resolver fragment (stayconnect-portal.conf) that answers the portal's
// public names with the portal address from the appliance's own resolver.
//
// WHY THE APPLIANCE ANSWERS A NAME PUBLIC DNS ALREADY ANSWERS. Before sign-in a guest may only reach the
// resolvers in the walled garden, and a public resolver's answer for a private address can be withheld by
// DNS-rebinding protection (contract §10). Answering locally makes the portal name work for every guest that
// uses the appliance's DNS, whatever its upstream does with private answers.
//
// `static` makes the zone authoritative for exactly these names: an A query gets the portal address, an AAAA
// query gets NODATA (there is no IPv6 portal, and NODATA -- unlike NXDOMAIN -- does not tell the client the
// name does not exist), and nothing under the name is forwarded upstream.
//
// It is a separate file from the guest fragment on purpose: it changes at runtime when scd learns a hostname,
// with no network revision, and rewriting it must never be able to disturb the listener and access-control
// configuration the guest fragment carries. With no portal address or no names it is a header and nothing else.
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
