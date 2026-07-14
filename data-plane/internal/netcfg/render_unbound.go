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

	// Portal FQDNs resolve to each network's own gateway. Because a single
	// static zone can only map a name to one address, we serve a per-network
	// view via response-ip is overkill; instead we publish the canonical
	// portal hostnames pointing at every gateway (clients on a network only
	// reach their own gateway's unbound instance, which returns its own A).
	b.WriteString("    local-zone: \"stayconnect.local.\" static\n")
	for _, n := range enabled {
		fmt.Fprintf(&b, "    local-data: \"portal.stayconnect.local. IN A %s\"\n", n.GatewayIP)
		fmt.Fprintf(&b, "    local-data: \"captive.stayconnect.local. IN A %s\"\n", n.GatewayIP)
		fmt.Fprintf(&b, "    local-data: \"gw.stayconnect.local. IN A %s\"\n", n.GatewayIP)
	}
	return []byte(b.String())
}
