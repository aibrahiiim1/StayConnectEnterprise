package netcfg

import (
	"fmt"
	"strings"
)

// RenderUnbound produces the Unbound fragment: listen on every enabled guest network's gateway, answer only for that
// network's subnet and serve the portal FQDNs at the per-network gateway. Custom-DNS networks are listed for portal
// hostnames but DHCP points their clients elsewhere.
func RenderUnbound(nets []GuestNetwork) []byte {
	enabled := sortEnabled(nets)
	var b strings.Builder
	b.WriteString("# StayConnect generated — guest DNS. Do not edit by hand.\n")
	b.WriteString("server:\n")
	b.WriteString("    do-ip4: yes\n    do-ip6: no\n    do-udp: yes\n    do-tcp: yes\n")
	b.WriteString("    num-threads: 2\n    prefetch: yes\n    cache-min-ttl: 30\n    cache-max-ttl: 86400\n")
	b.WriteString("    hide-identity: yes\n    hide-version: yes\n    harden-glue: yes\n    harden-dnssec-stripped: yes\n")
	// DNS abuse limits (contract section 10.1): a client may send 100 queries a second and a zone 1000 a second upstream
	// (bounding resolver tunnels), ANY is refused (amplification), and a device not signed in is held to 20 a second by
	// the firewall (render_nft.go).
	b.WriteString("    ip-ratelimit: 100\n    ratelimit: 1000\n    deny-any: yes\n")
	b.WriteString("    interface: 127.0.0.1\n")
	for _, n := range enabled {
		fmt.Fprintf(&b, "    interface: %s\n", n.GatewayIP)
	}
	b.WriteString("    access-control: 127.0.0.0/8 allow\n")
	for _, n := range enabled {
		fmt.Fprintf(&b, "    access-control: %s allow\n", n.SubnetCIDR)
	}
	b.WriteString("    access-control: 0.0.0.0/0 refuse\n")

	// Portal names answer with the asking network's own gateway: one Unbound listens on every gateway, so a global zone
	// returned another VLAN's gateway, which guest input cannot reach. Each network gets a view selected by client source
	// subnet; view-first lets other names fall through to the global config, and the global zone stays, empty, so these
	// names are never forwarded upstream.
	b.WriteString("    local-zone: \"stayconnect.local.\" static\n")
	for _, n := range enabled {
		fmt.Fprintf(&b, "    access-control-view: %s %s\n", n.SubnetCIDR, unboundViewName(n))
	}
	for _, n := range enabled {
		fmt.Fprintf(&b, "view:\n    name: \"%s\"\n    view-first: yes\n", unboundViewName(n))
		b.WriteString("    local-zone: \"stayconnect.local.\" static\n")
		fmt.Fprintf(&b, "    local-data: \"portal.stayconnect.local. IN A %s\"\n", n.GatewayIP)
		fmt.Fprintf(&b, "    local-data: \"captive.stayconnect.local. IN A %s\"\n", n.GatewayIP)
		fmt.Fprintf(&b, "    local-data: \"gw.stayconnect.local. IN A %s\"\n", n.GatewayIP)
	}
	return []byte(b.String())
}

// unboundViewName is a per-network view name: the bridge name is unique and unbound-safe.
func unboundViewName(n GuestNetwork) string {
	return "sc-" + strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, n.BridgeName)
}
