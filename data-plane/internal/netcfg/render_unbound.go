package netcfg

import (
	"fmt"
	"strings"
)

// RenderUnbound produces the Unbound config fragment that makes the resolver
// listen on every enabled guest network's gateway, answer only for that
// network's subnet, and serve the portal FQDNs at the correct per-network
// gateway. Networks using appliance DNS rely on this; custom-DNS networks are
// still listed for the portal hostnames but their clients are pointed at the
// custom servers by DHCP.
func RenderUnbound(nets []GuestNetwork) []byte {
	enabled := sortEnabled(nets)
	var b strings.Builder
	b.WriteString("# StayConnect generated — guest DNS. Do not edit by hand.\n")
	b.WriteString("server:\n")
	b.WriteString("    do-ip4: yes\n    do-ip6: no\n    do-udp: yes\n    do-tcp: yes\n")
	b.WriteString("    num-threads: 2\n    prefetch: yes\n    cache-min-ttl: 30\n    cache-max-ttl: 86400\n")
	b.WriteString("    hide-identity: yes\n    hide-version: yes\n    harden-glue: yes\n    harden-dnssec-stripped: yes\n")
	// DNS ABUSE LIMITS (contract §10.1). Proportionate, not a filter: a client may send 100 queries a second
	// (a page load is a few dozen, mostly cached), a single zone 1000 a second upstream -- a resolver tunnel
	// needs sustained high rates to one zone, which these bound. ANY is refused (amplification). A device that
	// has not signed in is held far lower, 20 a second, by the firewall (render_nft.go).
	b.WriteString("    ip-ratelimit: 100\n    ratelimit: 1000\n    deny-any: yes\n")
	// keep loopback for local tooling
	b.WriteString("    interface: 127.0.0.1\n")
	for _, n := range enabled {
		fmt.Fprintf(&b, "    interface: %s\n", n.GatewayIP)
	}
	b.WriteString("    access-control: 127.0.0.0/8 allow\n")
	for _, n := range enabled {
		fmt.Fprintf(&b, "    access-control: %s allow\n", n.SubnetCIDR)
	}
	b.WriteString("    access-control: 0.0.0.0/0 refuse\n")

	// PORTAL NAMES ANSWER WITH THE ASKING NETWORK'S OWN GATEWAY (audit N-21).
	//
	// This used to publish every gateway under the same names in one global zone, on the belief that a client
	// only reaches its own gateway's resolver. It does not: there is one Unbound, listening on every gateway, so
	// a client on VLAN 20 asking for portal.stayconnect.local got VLAN 40's gateway too -- an address its own
	// network may not reach (guest input is limited to its own gateway), so the portal failed for some clients
	// at random. Each network now has its own view, selected by the client's source subnet, holding only its
	// gateway. view-first lets everything else (the portal's public names, recursion) fall through to the
	// global configuration. The global zone stays, empty, so these names are never forwarded upstream.
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
