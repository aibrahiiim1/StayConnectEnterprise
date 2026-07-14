package netcfg

import (
	"fmt"
	"strings"
)

// RenderNftables produces the complete `table inet stayconnect` ruleset for all
// enabled guest networks. It replaces the old single-br-lan static file.
//
// Key differences from the legacy ruleset:
//   - The authenticated-guest set is CONCATENATED: `auth_ipv4 { ifname . ipv4_addr }`
//     so identity is (ingress bridge, IP) — correct even if two networks ever
//     share an IP range. scd adds `<bridge> . <ip>` elements.
//   - Per-network captive DNAT sends unauthenticated HTTP/HTTPS to THAT
//     network's own gateway:portal-port.
//   - Per-network masquerade for networks with NAT enabled.
//   - Guests can never reach the management/WAN host services or each other
//     (unless a network opts into shared access — not exposed yet).
//   - Dynamic sets guest_interfaces / guest_subnets drive generic rules.
//
// The walled_garden_ip set is preserved (scd reconciles it) and remains global
// pre-auth-reachable; that is acceptable because walled-garden targets are
// public login/payment endpoints, not per-tenant secrets.
func RenderNftables(nets []GuestNetwork, topo Topology) []byte {
	enabled := sortEnabled(nets)
	var b strings.Builder

	b.WriteString("#!/usr/sbin/nft -f\n")
	b.WriteString("# StayConnect generated ruleset — multi guest-network. Do not edit by hand.\n")
	// Replace ONLY our own table, atomically. NEVER `flush ruleset`: on a host
	// where Docker (or anything else) uses the nftables backend, flushing the
	// whole ruleset wipes Docker's ip/ip6 nat+filter chains, which silently
	// breaks container port-publishing until dockerd is restarted — and would
	// break the appliance's own Postgres/NATS/Redis on the next apply or reboot.
	// `table …; delete table …; table … { … }` in one atomic `nft -f` file
	// removes any prior stayconnect table (the empty decl guarantees the delete
	// has a target) and installs the fresh one, touching nothing else.
	b.WriteString("table inet stayconnect\n")
	b.WriteString("delete table inet stayconnect\n\n")
	b.WriteString("table inet stayconnect {\n")

	// --- sets ---
	b.WriteString("\tset auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t\tflags timeout\n\t\tcomment \"Authenticated guests: (ingress bridge, IP)\"\n\t}\n\n")
	b.WriteString("\tset walled_garden_ip {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { 1.1.1.1, 8.8.8.8, 8.8.4.4 }\n\t}\n\n")
	// dynamic descriptor sets (useful for generic rules + diagnostics)
	b.WriteString("\tset guest_interfaces {\n\t\ttype ifname\n")
	if len(enabled) > 0 {
		b.WriteString("\t\telements = { " + joinBridges(enabled) + " }\n")
	}
	b.WriteString("\t}\n\n")
	b.WriteString("\tset guest_subnets {\n\t\ttype ipv4_addr\n\t\tflags interval\n")
	if s := joinSubnets(enabled); s != "" {
		b.WriteString("\t\telements = { " + s + " }\n")
	}
	b.WriteString("\t}\n\n")

	// --- input ---
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n")
	b.WriteString("\t\tiif \"lo\" accept\n\t\tct state established,related accept\n\t\tct state invalid drop\n")
	fmt.Fprintf(&b, "\t\tiifname \"%s\" tcp dport 22 accept\n", topo.MgmtInterface)
	// Admin UI (Caddy TLS). When MgmtAddr is set, pin the accept to that address
	// so the admin UI is refused on any other IP the mgmt NIC carries — this is
	// what keeps it off the WAN IP on a pilot where mgmt and WAN share one NIC.
	if topo.MgmtAddr != "" {
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport 443 accept comment \"Caddy TLS (admin, mgmt IP only)\"\n", topo.MgmtInterface, topo.MgmtAddr)
	} else {
		fmt.Fprintf(&b, "\t\tiifname \"%s\" tcp dport 443 accept comment \"Caddy TLS (admin)\"\n", topo.MgmtInterface)
	}
	fmt.Fprintf(&b, "\t\tiifname \"%s\" icmp type echo-request accept\n", topo.MgmtInterface)
	// per-network guest host services (DHCP/DNS/portal/ICMP) on each bridge
	for _, n := range enabled {
		br := n.BridgeName
		fmt.Fprintf(&b, "\t\tiifname \"%s\" meta nfproto ipv6 drop comment \"no IPv6 guest services\"\n", br)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" udp dport { 67, 68 } accept\n", br)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" udp dport 53 accept\n", br)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" tcp dport 53 accept\n", br)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" tcp dport { %d, %d } accept comment \"portal\"\n", br, topo.PortalHTTPPort, topo.PortalTLSPort)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" icmp type echo-request accept\n", br)
	}
	b.WriteString("\t}\n\n")

	// --- forward ---
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n")
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	b.WriteString("\t\tiifname @guest_interfaces meta nfproto ipv6 drop comment \"IPv6 guest bypass guard\"\n")
	// block guests reaching management subnets
	for _, cidr := range topo.MgmtCIDRs {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s drop comment \"guest -> mgmt blocked\"\n", cidr)
	}
	// block inter-guest-network traffic (guest subnet -> another guest subnet)
	b.WriteString("\t\tiifname @guest_interfaces ip daddr @guest_subnets drop comment \"inter-guest isolation\"\n")
	// networks with internet disabled: drop their egress to WAN entirely
	for _, n := range enabled {
		if !n.InternetEnabled {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" oifname \"%s\" drop comment \"internet disabled for %s\"\n",
				n.BridgeName, topo.WANInterface, n.Name)
		}
	}
	// authenticated guests -> WAN: single rule keyed on the concatenated
	// (ingress bridge, source IP) auth set.
	fmt.Fprintf(&b, "\t\toifname \"%s\" iifname . ip saddr @auth_ipv4 accept comment \"authenticated guests\"\n", topo.WANInterface)
	// walled-garden pre-auth -> WAN for any guest interface
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr @walled_garden_ip accept\n", topo.WANInterface)
	b.WriteString("\t}\n\n")

	// --- prerouting nat (captive redirect per network to its own gateway) ---
	b.WriteString("\tchain prerouting_nat {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, n := range enabled {
		if !n.CaptiveEnabled {
			continue
		}
		br := n.BridgeName
		gw := n.GatewayIP
		fmt.Fprintf(&b, "\t\tiifname \"%s\" iifname . ip saddr != @auth_ipv4 ip daddr != @walled_garden_ip tcp dport 80  dnat ip to %s:%d\n",
			br, gw, topo.PortalHTTPPort)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" iifname . ip saddr != @auth_ipv4 ip daddr != @walled_garden_ip tcp dport 443 dnat ip to %s:%d\n",
			br, gw, topo.PortalTLSPort)
	}
	b.WriteString("\t}\n\n")

	// --- postrouting nat (per-network masquerade) ---
	b.WriteString("\tchain postrouting_nat {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, n := range enabled {
		if !n.NATEnabled {
			continue
		}
		fmt.Fprintf(&b, "\t\tip saddr %s oifname \"%s\" masquerade\n", n.SubnetCIDR, topo.WANInterface)
	}
	b.WriteString("\t}\n")

	b.WriteString("}\n")
	return []byte(b.String())
}

func joinBridges(nets []GuestNetwork) string {
	var parts []string
	for _, n := range nets {
		parts = append(parts, fmt.Sprintf("%q", n.BridgeName))
	}
	return strings.Join(parts, ", ")
}

func joinSubnets(nets []GuestNetwork) string {
	var parts []string
	for _, n := range nets {
		parts = append(parts, n.SubnetCIDR)
	}
	return strings.Join(parts, ", ")
}
