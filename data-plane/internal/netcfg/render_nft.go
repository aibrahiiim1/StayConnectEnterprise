package netcfg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// RenderMarkerSet is an empty set whose only job is to carry, inside the live kernel, the fingerprint of the render
// that produced the table. The structure netd installs is a function of (intent, topology, renderer version) and the
// last term is invisible to the database and the stored bundle: an appliance whose revision was rendered by older
// software would replay a stale stored file and silently delete a set the current software requires.
const RenderMarkerSet = "sc_render_fp"

const renderMarkerPrefix = "netd-render-fp="

// RenderFingerprint returns the fingerprint of the ruleset RenderNftables would produce, computed over the body
// without the marker so the marker can carry it.
func RenderFingerprint(nets []GuestNetwork, topo Topology) string {
	return fingerprintOf(renderNftBody(nets, topo))
}

func fingerprintOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:16])
}

// FingerprintFromSetComment extracts the fingerprint from the marker set's comment, or "" if it is not a marker; an
// unreadable marker compares unequal to every real fingerprint, never equal.
func FingerprintFromSetComment(comment string) string {
	i := strings.Index(comment, renderMarkerPrefix)
	if i < 0 {
		return ""
	}
	fp := comment[i+len(renderMarkerPrefix):]
	if j := strings.IndexAny(fp, " ;\""); j >= 0 {
		fp = fp[:j]
	}
	if len(fp) != 32 {
		return ""
	}
	for _, c := range fp {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	return fp
}

// RenderNftables produces the complete `table inet stayconnect` ruleset for all enabled guest networks.
// The authenticated-guest set is concatenated (ifname . ipv4_addr) so identity is (ingress bridge, IP) even if two
// networks share an IP range. Captive DNAT sends unauthenticated HTTP/HTTPS to that network's own gateway portal
// port; guests can never reach management/WAN host services or each other. walled_garden_ip is global and
// pre-auth-reachable because its targets are public login/payment endpoints.
func RenderNftables(nets []GuestNetwork, topo Topology) []byte {
	body := renderNftBody(nets, topo)
	// The marker is injected after fingerprinting, as the first declaration in the table, so the fingerprint describes
	// the structure rather than itself.
	const open = "table inet stayconnect {\n"
	marker := fmt.Sprintf("\tset %s {\n\t\ttype ipv4_addr\n\t\tcomment \"%s%s\"\n\t}\n\n",
		RenderMarkerSet, renderMarkerPrefix, fingerprintOf(body))
	if i := strings.LastIndex(body, open); i >= 0 {
		j := i + len(open)
		return []byte(body[:j] + marker + body[j:])
	}
	return []byte(body)
}

func renderNftBody(nets []GuestNetwork, topo Topology) string {
	enabled := sortEnabled(nets)
	var b strings.Builder

	b.WriteString("#!/usr/sbin/nft -f\n")
	b.WriteString("# StayConnect generated ruleset — multi guest-network. Do not edit by hand.\n")
	// Replace only our own table, atomically, and never `flush ruleset`: that wipes Docker's nftables nat/filter chains
	// and breaks container port-publishing (and the appliance's Postgres/Redis) until dockerd restarts. The empty
	// declaration guarantees the delete has a target.
	b.WriteString("table inet stayconnect\n")
	b.WriteString("delete table inet stayconnect\n\n")
	b.WriteString("table inet stayconnect {\n")

	b.WriteString("\tset auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t\tflags timeout\n\t\tcomment \"Authenticated guests: (ingress bridge, IP)\"\n\t}\n\n")
	// Phase-3 packet-authorization set, owned by netd. It is separate from auth_ipv4 (reconciled by legacy scd from
	// public.sessions) so a Phase-3 full-state reconciliation can never remove a live legacy guest's authorization.
	// Always emitted and starting empty (an empty set matches nothing): regenerating the ruleset later would flush
	// auth_ipv4 and drop every live legacy guest, so activation stays a flag flip.
	b.WriteString("\tset phase3_auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t\tflags timeout\n\t\tcomment \"Phase-3 authorized guests (netd-owned): (ingress bridge, IP)\"\n\t}\n\n")
	// The walled garden is three sets (internal/nft/garden.go): walled_garden_ip opens every port to an address, and the
	// two port sets open exactly the (address, port) pairs scd reconciles. The baseline resolvers sit in the port sets at
	// 53 only, so DNS-over-HTTPS to them is not reachable before sign-in.
	b.WriteString("\tset walled_garden_ip {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\tauto-merge\n\t}\n\n")
	fmt.Fprintf(&b, "\tset walled_garden_tcp {\n\t\ttype ipv4_addr . inet_service\n\t\tflags interval\n\t\tcomment \"pre-auth (address, TCP port) pairs; scd reconciles\"\n\t\telements = { %s }\n\t}\n\n", baselinePairs())
	fmt.Fprintf(&b, "\tset walled_garden_udp {\n\t\ttype ipv4_addr . inet_service\n\t\tflags interval\n\t\tcomment \"pre-auth (address, UDP port) pairs; scd reconciles\"\n\t\telements = { %s }\n\t}\n\n", baselinePairs())
	// Pre-authentication DNS is rate-limited per device: 20 queries a second, burst 40, per address, to the gateway
	// resolver and the public resolvers alike, so the resolver cannot be tunnelled through. Authenticated devices are
	// exempt; the counters make it observable.
	b.WriteString("\tset dns_preauth_rl {\n\t\ttype ipv4_addr\n\t\tsize 65535\n\t\tflags dynamic,timeout\n\t\ttimeout 1m\n\t}\n\n")
	b.WriteString("\tcounter dns_preauth_limited {\n\t\tcomment \"pre-auth DNS packets refused by the per-device rate limit\"\n\t}\n\n")
	b.WriteString("\tcounter guest_ipv6_dropped {\n\t\tcomment \"IPv6 to or from a client network, refused\"\n\t}\n\n")
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

	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n")
	// Pre-authentication DNS is rate-limited before the established accept: a UDP socket is established at the
	// resolver's first reply, so a limiter after it would let a tunnel through unmetered. Authenticated devices are exempt.
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tiifname @guest_interfaces meta l4proto { tcp, udp } th dport 53 iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 update @dns_preauth_rl { ip saddr limit rate over 20/second burst 40 packets } counter name \"dns_preauth_limited\" drop comment \"pre-auth DNS rate limit\"\n")
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	// The portal address is refused everywhere but a guest network, as a rule and not just the drop policy: the
	// management accepts below key on interface, not destination, so a packet routed to the portal address from WAN or
	// management would reach SSH or the admin console. Placed before those accepts and after the established accept;
	// loopback was accepted above.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tiifname != @guest_interfaces ip daddr %s drop comment \"portal address: guest networks only\"\n", vip)
	}
	fmt.Fprintf(&b, "\t\tiifname \"%s\" tcp dport 22 accept\n", topo.MgmtInterface)
	// Admin UI (Caddy TLS). When MgmtAddr is set the accept is pinned to it, so the admin UI is refused on the WAN IP
	// when mgmt and WAN share one NIC.
	if topo.MgmtAddr != "" {
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport 443 accept comment \"Caddy TLS (admin, mgmt IP only)\"\n", topo.MgmtInterface, topo.MgmtAddr)
	} else {
		fmt.Fprintf(&b, "\t\tiifname \"%s\" tcp dport 443 accept comment \"Caddy TLS (admin)\"\n", topo.MgmtInterface)
	}
	fmt.Fprintf(&b, "\t\tiifname \"%s\" icmp type echo-request accept\n", topo.MgmtInterface)
	// Per-network guest host services are pinned to each network's own gateway address. Keyed on ingress interface
	// alone, the input hook (which fires for any local address) let a guest reach another network's gateway and the
	// management address; the forward chain never sees this path. Every accept carries `ip daddr GATEWAY`. DHCP keeps
	// the broadcast destination: a client with no address sends DISCOVER/REQUEST to 255.255.255.255, so a gateway-only
	// rule would break address assignment.
	for _, n := range enabled {
		br := SafeIface(n.BridgeName)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" meta nfproto ipv6 counter name \"guest_ipv6_dropped\" drop comment \"no IPv6 guest services\"\n", br)
		// Fail safe on a missing gateway: an empty GatewayIP would render `ip daddr  udp dport ...`, a syntax error that,
		// because the ruleset is one atomic `nft -f`, breaks the whole table. A network without a gateway gets no accepts.
		if n.GatewayIP == "" {
			fmt.Fprintf(&b, "\t\t# %s has no gateway address: no guest services are opened on it\n", br)
			continue
		}
		gw := n.GatewayIP
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr { %s, 255.255.255.255 } udp dport { 67, 68 } accept comment \"DHCP (own gateway or broadcast)\"\n", br, gw)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s udp dport 53 accept comment \"DNS (own gateway)\"\n", br, gw)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport 53 accept comment \"DNS (own gateway)\"\n", br, gw)
		// The TLS portal port is opened only when there is one.
		if topo.PortalTLSPort > 0 {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport { %d, %d } accept comment \"portal (own gateway)\"\n", br, gw, topo.PortalHTTPPort, topo.PortalTLSPort)
		} else {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport %d accept comment \"portal (own gateway)\"\n", br, gw, topo.PortalHTTPPort)
		}
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s icmp type echo-request accept comment \"ping own gateway\"\n", br, gw)
	}
	// The portal address, from any guest network, on the two portal listeners only. prerouting_nat has already turned
	// 443 into 8343 and 80 into 8380 on this address, so these are the ports the input hook sees. Nothing else on the
	// portal address is open.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s tcp dport { %d, %d } accept comment \"portal address (TLS and HTTP portal)\"\n",
			vip, PortalAddressTLSPort, portalHTTPPort(topo))
	}
	// The boundary, stated rather than implied by the policy: a guest network may not address the appliance anywhere
	// but its own gateway. As a rule, a later unconstrained accept cannot widen the guest input surface by accident.
	b.WriteString("\t\tiifname @guest_interfaces drop comment \"guest input is limited to its own gateway\"\n")
	b.WriteString("\t}\n\n")

	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n")
	// IPv6 never crosses a client network in either direction, ahead of the established accept so no flow is exempt.
	// The appliance's own IPv6 (WAN, management) is untouched.
	b.WriteString("\t\tiifname @guest_interfaces meta nfproto ipv6 counter name \"guest_ipv6_dropped\" drop comment \"IPv6 guest bypass guard\"\n")
	b.WriteString("\t\toifname @guest_interfaces meta nfproto ipv6 counter name \"guest_ipv6_dropped\" drop comment \"IPv6 guest bypass guard (return path)\"\n")
	// The portal address is never routed. A guest's packet to it is DNAT'd to a local address and goes to the input
	// hook; it reaches this chain only if the portal interface is absent while the ruleset still names the address, and
	// would then be forwarded out of the WAN. Dropped before the established accept and every accept below.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tip daddr %s drop comment \"portal address is never routed\"\n", vip)
		fmt.Fprintf(&b, "\t\tip saddr %s drop comment \"portal address is never routed (source)\"\n", vip)
	}
	// Pre-authentication DNS straight to a public resolver is rate-limited like DNS to the gateway, before the
	// established accept; authenticated devices are exempt.
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" meta l4proto { tcp, udp } th dport 53 iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 update @dns_preauth_rl { ip saddr limit rate over 20/second burst 40 packets } counter name \"dns_preauth_limited\" drop comment \"pre-auth DNS rate limit\"\n", topo.WANInterface)
	// Client isolation: a client's packet routed back into its own network (sent to the gateway's MAC for another
	// client's address) is dropped before the established accept so existing flows are not exempt. Clients sharing an
	// access point exchange frames without the appliance seeing them, so the access point's own isolation is needed too.
	for _, n := range enabled {
		if n.ClientIsolation {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" oifname \"%s\" drop comment \"client isolation\"\n", SafeIface(n.BridgeName), SafeIface(n.BridgeName))
		}
	}
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	// A network on custom DNS reaches its own resolvers before sign-in: DHCP hands those clients the custom servers, so
	// without this neither the captive check nor the sign-in page resolves. Port 53 only, to exactly the configured
	// addresses, from that network only, before the management drop (a hotel resolver often sits in a management range).
	for _, n := range enabled {
		if s := customDNSServers(n); s != "" {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr { %s } meta l4proto { tcp, udp } th dport 53 accept comment \"custom DNS (pre-auth)\"\n",
				SafeIface(n.BridgeName), s)
		}
	}
	for _, cidr := range topo.MgmtCIDRs {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s drop comment \"guest -> mgmt blocked\"\n", cidr)
	}
	b.WriteString("\t\tiifname @guest_interfaces ip daddr @guest_subnets drop comment \"inter-guest isolation\"\n")
	for _, n := range enabled {
		if !n.InternetEnabled {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" oifname \"%s\" drop comment \"internet disabled for %s\"\n",
				SafeIface(n.BridgeName), topo.WANInterface, SafeText(n.Name))
		}
	}
	fmt.Fprintf(&b, "\t\toifname \"%s\" iifname . ip saddr @auth_ipv4 accept comment \"authenticated guests\"\n", topo.WANInterface)
	// The Phase-3 gate. While the set is empty this rule can never match, so a dark appliance is unaffected.
	fmt.Fprintf(&b, "\t\toifname \"%s\" iifname . ip saddr @phase3_auth_ipv4 accept comment \"phase-3 authorized guests\"\n", topo.WANInterface)
	// walled-garden pre-auth -> WAN for any guest interface: every port to an address-only element, and exactly
	// the named port to a port-restricted one.
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr @walled_garden_ip accept\n", topo.WANInterface)
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr . tcp dport @walled_garden_tcp accept\n", topo.WANInterface)
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr . udp dport @walled_garden_udp accept\n", topo.WANInterface)
	// An unauthenticated HTTPS attempt is dropped silently, on purpose. With no TLS portal there is no redirect for
	// HTTPS to another host (the appliance holds certificates only for its own portal names). Not `reject with tcp
	// reset`: Chromium's captive_portal_tab_reloader starts its captive check (and opens the login tab) only when an
	// HTTPS load times out, fails TLS or trips the 30-second slow-SSL timer, and a reset cancels it. A drop lets the
	// check run over plain HTTP into the port-80 redirect, and HTTPS-Upgrades falls back to http:// on any error. UDP
	// 443 (QUIC) is left to the chain policy.
	// Explicit rule, after both authorized-guest accepts and the walled garden, so it only matches guests not signed in.
	if topo.PortalTLSPort == 0 {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" tcp dport 443 drop comment \"no TLS portal: unanswered, so browsers run their captive check\"\n", topo.WANInterface)
	}
	b.WriteString("\t}\n\n")

	b.WriteString("\tchain prerouting_nat {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	// The portal address first: a guest, signed in or not, connecting to it reaches the portal (443 to portald's TLS
	// listener, 80 to its HTTP listener, both on the portal address so the packet stays local). This is the only 443
	// redirect in the ruleset and it is bound to the portal address: HTTPS to any other host is never redirected. It
	// sits before the walled-garden accept and the per-network captive DNAT because a dnat ends the chain.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s tcp dport 443 dnat ip to %s:%d comment \"portal address: TLS portal\"\n",
			vip, vip, PortalAddressTLSPort)
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s tcp dport 80 dnat ip to %s:%d comment \"portal address: HTTP portal\"\n",
			vip, vip, portalHTTPPort(topo))
	}
	// A port-restricted garden pair on 80 or 443 is reachable as itself: accepting here ends NAT processing, so the
	// captive redirect never claims it.
	b.WriteString("\t\tiifname @guest_interfaces ip daddr . tcp dport @walled_garden_tcp accept comment \"port-restricted garden: no captive redirect\"\n")
	for _, n := range enabled {
		if !n.CaptiveEnabled {
			continue
		}
		br := SafeIface(n.BridgeName)
		gw := n.GatewayIP
		// A guest authorized in either set must not be captive-redirected; omitting the Phase-3 exclusion would DNAT an
		// authorized guest's web traffic to the login page.
		fmt.Fprintf(&b, "\t\tiifname \"%s\" iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport 80  dnat ip to %s:%d\n",
			br, gw, topo.PortalHTTPPort)
		// Port 443 is redirected only if there is something to redirect it to; emitting it unconditionally described a
		// captive TLS portal that does not exist.
		if topo.PortalTLSPort > 0 {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport 443 dnat ip to %s:%d\n",
				br, gw, topo.PortalTLSPort)
		}
	}
	b.WriteString("\t}\n\n")

	b.WriteString("\tchain postrouting_nat {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, n := range enabled {
		if !n.NATEnabled {
			continue
		}
		fmt.Fprintf(&b, "\t\tip saddr %s oifname \"%s\" masquerade\n", n.SubnetCIDR, topo.WANInterface)
	}
	b.WriteString("\t}\n")

	b.WriteString("}\n")
	return b.String()
}

// renderPortalVIP is the portal address as it may appear in the ruleset, or "" for none. A malformed value renders
// nothing: one syntax error in the atomic `nft -f` loses every guest network, and validation refuses it earlier.
func renderPortalVIP(topo Topology) string {
	if ip := ParsePortalIP(topo.PortalVIP); ip != nil {
		return ip.String()
	}
	return ""
}

// customDNSServers is the network's custom resolvers as an nft set body, or "" when it uses the appliance's.
// Only well-formed IPv4 addresses are rendered: one malformed element would break the whole atomic ruleset.
func customDNSServers(n GuestNetwork) string {
	if n.DNSMode != "custom" {
		return ""
	}
	var parts []string
	seen := map[string]bool{}
	for _, d := range n.DNSServers {
		ip := net.ParseIP(strings.TrimSpace(d)).To4()
		if ip == nil || ip.IsUnspecified() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		parts = append(parts, ip.String())
	}
	return strings.Join(parts, ", ")
}

func joinBridges(nets []GuestNetwork) string {
	var parts []string
	for _, n := range nets {
		parts = append(parts, fmt.Sprintf("%q", SafeIface(n.BridgeName)))
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

// BaselineResolvers are the public resolvers every pre-auth device may reach on port 53. The render declares them as
// static elements of walled_garden_tcp/udp, so the converge does not carry them across a structural replace.
var BaselineResolvers = []string{"1.1.1.1", "8.8.8.8", "8.8.4.4"}

// BaselineResolverPort is the only port the baseline resolvers are open on before sign-in.
const BaselineResolverPort = 53

func baselinePairs() string {
	parts := make([]string, 0, len(BaselineResolvers))
	for _, ip := range BaselineResolvers {
		parts = append(parts, fmt.Sprintf("%s . %d", ip, BaselineResolverPort))
	}
	return strings.Join(parts, ", ")
}
