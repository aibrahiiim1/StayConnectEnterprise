package netcfg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// RenderMarkerSet is an empty set whose ONLY job is to carry, inside the live kernel, the fingerprint of the
// render that produced the table. It exists because the structure netd must install is a pure function of
// (intent, topology, RENDERER VERSION) — and the third term is invisible to anything that only inspects the
// database or the stored bundle.
//
// Live Increment 9 is exactly what happens without it: an appliance whose active revision was rendered in July
// replayed that stored file on every netd start and silently deleted a set the current software requires. The
// stored artifact could not know it was stale, and neither could the DB, because neither changed.
//
// Putting the fingerprint in the kernel means the question "does the live ruleset match what this binary would
// build?" is answered by reading the live ruleset — the same place the answer has to be true.
const RenderMarkerSet = "sc_render_fp"

const renderMarkerPrefix = "netd-render-fp="

// RenderFingerprint returns the fingerprint of the ruleset RenderNftables would produce for this input. It is
// computed over the rendered body WITHOUT the marker, so the marker can carry it without being circular.
func RenderFingerprint(nets []GuestNetwork, topo Topology) string {
	return fingerprintOf(renderNftBody(nets, topo))
}

func fingerprintOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:16])
}

// FingerprintFromSetComment extracts the fingerprint from the marker set's comment, or "" if the comment is not
// a marker. A set that is present but carries an unrecognised comment yields "", which compares unequal to every
// real fingerprint — an unreadable marker is treated as "does not match", never as "matches".
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
	body := renderNftBody(nets, topo)
	// The marker is injected AFTER fingerprinting, as the first declaration inside the table, so that the
	// fingerprint describes the structure rather than itself.
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
	// Replace ONLY our own table, atomically. NEVER `flush ruleset`: on a host
	// where Docker (or anything else) uses the nftables backend, flushing the
	// whole ruleset wipes Docker's ip/ip6 nat+filter chains, which silently
	// breaks container port-publishing until dockerd is restarted — and would
	// break the appliance's own Postgres/Redis on the next apply or reboot.
	// `table …; delete table …; table … { … }` in one atomic `nft -f` file
	// removes any prior stayconnect table (the empty decl guarantees the delete
	// has a target) and installs the fresh one, touching nothing else.
	b.WriteString("table inet stayconnect\n")
	b.WriteString("delete table inet stayconnect\n\n")
	b.WriteString("table inet stayconnect {\n")

	// --- sets ---
	b.WriteString("\tset auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t\tflags timeout\n\t\tcomment \"Authenticated guests: (ingress bridge, IP)\"\n\t}\n\n")
	// PHASE-3 packet-authorization set — owned by netd, written by nothing else.
	//
	// It is separate from auth_ipv4 on purpose. Legacy scd reconciles auth_ipv4 from public.sessions; Phase 3
	// authorizes from iam_v2.sessions through netd. Sharing one set would mean a Phase-3 full-state
	// reconciliation (which must delete what no Phase-3 session claims) could remove a live legacy guest's
	// authorization. Two sets make that impossible structurally rather than by careful filtering.
	//
	// It is emitted ALWAYS and starts EMPTY. An empty set matches nothing, so while Phase 3 is dark the
	// appliance forwards exactly what it forwarded before. Emitting it from the start (rather than adding it
	// at cutover) matters because this ruleset is applied as `delete table` + recreate: regenerating it later
	// would flush auth_ipv4 and drop every live legacy guest. Present-but-empty makes Phase-3 activation a
	// flag flip with no packet interruption and exactly one cutover boundary.
	b.WriteString("\tset phase3_auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t\tflags timeout\n\t\tcomment \"Phase-3 authorized guests (netd-owned): (ingress bridge, IP)\"\n\t}\n\n")
	// THE WALLED GARDEN IS THREE SETS (internal/nft/garden.go). walled_garden_ip opens every port to an address
	// -- an operator rule written without ports. The two port sets open exactly the (address, port) pairs scd
	// reconciles into them: a rule's Ports, an email service's mail ports, an identity provider's or payment
	// page's 443. The baseline resolvers live in the port sets at 53 only: they used to be open on every port,
	// which made DNS-over-HTTPS to them (and whatever else those addresses serve) reachable before sign-in.
	b.WriteString("\tset walled_garden_ip {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\tauto-merge\n\t}\n\n")
	fmt.Fprintf(&b, "\tset walled_garden_tcp {\n\t\ttype ipv4_addr . inet_service\n\t\tflags interval\n\t\tcomment \"pre-auth (address, TCP port) pairs; scd reconciles\"\n\t\telements = { %s }\n\t}\n\n", baselinePairs())
	fmt.Fprintf(&b, "\tset walled_garden_udp {\n\t\ttype ipv4_addr . inet_service\n\t\tflags interval\n\t\tcomment \"pre-auth (address, UDP port) pairs; scd reconciles\"\n\t\telements = { %s }\n\t}\n\n", baselinePairs())
	// PRE-AUTHENTICATION DNS IS RATE-LIMITED PER DEVICE. A device that has not signed in may resolve names (the
	// captive-portal probe needs it) but not tunnel through the resolver: 20 queries a second, burst 40, per
	// address, to the gateway resolver and to the public resolvers in the garden alike. Authenticated devices
	// are not subject to it. The counters make the defence observable.
	b.WriteString("\tset dns_preauth_rl {\n\t\ttype ipv4_addr\n\t\tsize 65535\n\t\tflags dynamic,timeout\n\t\ttimeout 1m\n\t}\n\n")
	b.WriteString("\tcounter dns_preauth_limited {\n\t\tcomment \"pre-auth DNS packets refused by the per-device rate limit\"\n\t}\n\n")
	b.WriteString("\tcounter guest_ipv6_dropped {\n\t\tcomment \"IPv6 to or from a client network, refused\"\n\t}\n\n")
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
	// PRE-AUTHENTICATION DNS IS RATE-LIMITED BEFORE THE ESTABLISHED ACCEPT. A UDP socket becomes "established"
	// at the resolver's first reply, so a limiter placed after that accept saw only a client's first queries --
	// a tunnel uses one socket and would pass unmetered (measured on PRE-LIVE 2026-10-11). Authenticated
	// devices are exempt.
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tiifname @guest_interfaces meta l4proto { tcp, udp } th dport 53 iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 update @dns_preauth_rl { ip saddr limit rate over 20/second burst 40 packets } counter name \"dns_preauth_limited\" drop comment \"pre-auth DNS rate limit\"\n")
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	// THE PORTAL ADDRESS IS REFUSED EVERYWHERE BUT A GUEST NETWORK, and the refusal is a rule, not the policy.
	//
	// The policy drop alone does NOT cover it. The management accepts just below (SSH on 22, and the admin UI on
	// 443 when no MgmtAddr pins it) are keyed on the interface, not the destination -- so anything on the WAN or
	// management side that routed a packet to the portal address at this appliance would be accepted by them and
	// land on SSH or the admin console under the portal's name. The portal address is answered on guest
	// networks only (contract §2.1, §9), so every other ingress is dropped here, BEFORE those accepts and after
	// the established accept (a reply to the appliance's own connection is never addressed to it). Loopback was
	// accepted above: the appliance's own health checks may still reach it.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tiifname != @guest_interfaces ip daddr %s drop comment \"portal address: guest networks only\"\n", vip)
	}
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
	// PER-NETWORK GUEST HOST SERVICES, PINNED TO EACH NETWORK'S OWN GATEWAY ADDRESS.
	//
	// THE DESTINATION USED TO BE UNCONSTRAINED, and that was the hole. These accepts were keyed on the
	// INGRESS INTERFACE alone -- `iifname "br-g221" tcp dport 8380 accept` -- and the input hook fires for
	// every packet addressed to ANY local address. So a guest on one network could reach the appliance on
	// another network's gateway, and on the management address, simply by addressing it: the packet arrived
	// on a guest bridge, matched the interface, and was accepted whatever it was aimed at.
	//
	// MEASURED ON PRE-LIVE before this change, from a client on VLAN 221:
	//
	//	the other guest network's gateway (10.222.0.1)   REACHABLE   (icmp, and the portal on :8380)
	//	the management address (172.21.60.25) by ICMP     REACHABLE
	//	the Hotel Admin UI, PostgreSQL, SSH               already blocked
	//
	// The FORWARD chain was never the problem -- it drops guest -> mgmt CIDRs and guest -> @guest_subnets
	// already, which is why guest-to-guest client isolation held. This is the INPUT path to the appliance
	// itself, which those rules never see.
	//
	// Every accept below now carries `ip daddr <that network's gateway>`, so a guest may reach the services
	// on ITS OWN gateway and nothing else. That preserves every required same-network path: DHCP, DNS,
	// captive detection, the captive portal (each network's portal_url and DHCP option 114 already name its
	// own gateway), the walled garden, NAT, enforcement and accounting, all of which are either same-network
	// or live in the forward/nat chains.
	//
	// DHCP KEEPS THE BROADCAST DESTINATION, and it has to: a client with no address yet sends DISCOVER and
	// REQUEST to 255.255.255.255, so a gateway-only rule would break address assignment entirely -- the one
	// mistake here that would take guest networking down rather than merely harden it.
	for _, n := range enabled {
		br := n.BridgeName
		fmt.Fprintf(&b, "\t\tiifname \"%s\" meta nfproto ipv6 counter name \"guest_ipv6_dropped\" drop comment \"no IPv6 guest services\"\n", br)
		// FAIL SAFE ON A MISSING GATEWAY. An empty GatewayIP would render `ip daddr  udp dport ...`, which is
		// a syntax error -- and because this ruleset is applied as one atomic `nft -f`, a single malformed
		// line does not break one network, it breaks the WHOLE table and takes every guest network with it.
		// So a network without a gateway gets no accepts at all: it cannot serve, which is the safe reading
		// of an incomplete configuration, and the comment says so in the installed ruleset.
		if n.GatewayIP == "" {
			fmt.Fprintf(&b, "\t\t# %s has no gateway address: no guest services are opened on it\n", br)
			continue
		}
		gw := n.GatewayIP
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr { %s, 255.255.255.255 } udp dport { 67, 68 } accept comment \"DHCP (own gateway or broadcast)\"\n", br, gw)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s udp dport 53 accept comment \"DNS (own gateway)\"\n", br, gw)
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport 53 accept comment \"DNS (own gateway)\"\n", br, gw)
		// The TLS portal port is opened only when there IS one. Accepting traffic to a port nothing listens
		// on is not harmful in itself, but it advertises a service the appliance does not run.
		if topo.PortalTLSPort > 0 {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport { %d, %d } accept comment \"portal (own gateway)\"\n", br, gw, topo.PortalHTTPPort, topo.PortalTLSPort)
		} else {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s tcp dport %d accept comment \"portal (own gateway)\"\n", br, gw, topo.PortalHTTPPort)
		}
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr %s icmp type echo-request accept comment \"ping own gateway\"\n", br, gw)
	}
	// THE PORTAL ADDRESS, FROM ANY GUEST NETWORK, ON THE TWO PORTAL LISTENERS ONLY. prerouting_nat has already
	// turned the guest's 443 into 8343 and 80 into 8380 on this same address, so these are the ports the input
	// hook sees. Nothing else on the portal address is open: no DNS, no ping, and none of portald's other ports
	// by their own numbers -- a guest who connects to <portal address>:8343 directly is admitted only because that
	// is where its 443 lands anyway, which is the same listener.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s tcp dport { %d, %d } accept comment \"portal address (TLS and HTTP portal)\"\n",
			vip, PortalAddressTLSPort, portalHTTPPort(topo))
	}
	// AND THE BOUNDARY, STATED RATHER THAN IMPLIED BY THE POLICY.
	//
	// The chain's policy is already drop, so this changes no packet's fate today. It is here because the
	// policy is a default and this is a decision: a guest network may not address the appliance anywhere but
	// its own gateway. Written as a rule, it also means a future accept appended after it cannot widen the
	// guest input surface by accident -- an unconstrained accept added below this line is simply unreachable
	// for guest traffic, which is the safe direction for a mistake to fail in.
	b.WriteString("\t\tiifname @guest_interfaces drop comment \"guest input is limited to its own gateway\"\n")
	b.WriteString("\t}\n\n")

	// --- forward ---
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n")
	// IPv6 NEVER CROSSES A CLIENT NETWORK, in either direction, and the guard sits before the established
	// accept so no flow -- however it came to exist -- is exempt. The appliance's own IPv6 (WAN, management)
	// is untouched: only packets entering or leaving a client bridge are refused.
	b.WriteString("\t\tiifname @guest_interfaces meta nfproto ipv6 counter name \"guest_ipv6_dropped\" drop comment \"IPv6 guest bypass guard\"\n")
	b.WriteString("\t\toifname @guest_interfaces meta nfproto ipv6 counter name \"guest_ipv6_dropped\" drop comment \"IPv6 guest bypass guard (return path)\"\n")
	// THE PORTAL ADDRESS IS NEVER ROUTED, in either direction, from or to anything. A guest's packet to it is
	// DNAT'd to a LOCAL address and goes to the input hook, never here; the only way one reaches this chain is
	// the portal interface being absent (a failed apply, a hand-deleted link) while the ruleset still names the
	// address -- and then an authorized guest's packet would be forwarded out of the WAN, and masqueraded, to
	// whatever holds that private address upstream. Dropped here, before the established accept and before
	// every accept below, so the address cannot leave the appliance whatever state the interface is in.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tip daddr %s drop comment \"portal address is never routed\"\n", vip)
		fmt.Fprintf(&b, "\t\tip saddr %s drop comment \"portal address is never routed (source)\"\n", vip)
	}
	// Pre-authentication DNS straight to a public resolver, rate-limited exactly like DNS to the gateway and for
	// the same reason placed before the established accept; authenticated devices are exempt.
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" meta l4proto { tcp, udp } th dport 53 iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 update @dns_preauth_rl { ip saddr limit rate over 20/second burst 40 packets } counter name \"dns_preauth_limited\" drop comment \"pre-auth DNS rate limit\"\n", topo.WANInterface)
	// CLIENT ISOLATION (audit N-4): the switch was stored and never rendered. A client's packet ROUTED back into
	// its own network (sent to the gateway's MAC for another client's address) is dropped here, before the
	// established accept so an existing flow is not exempt. Clients that share an access point or switch port
	// exchange frames there without the appliance ever seeing them, which is why the console tells the operator
	// to switch on the access point's own client isolation as well.
	for _, n := range enabled {
		if n.ClientIsolation {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" oifname \"%s\" drop comment \"client isolation\"\n", n.BridgeName, n.BridgeName)
		}
	}
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	// A NETWORK ON CUSTOM DNS REACHES ITS OWN RESOLVERS BEFORE SIGN-IN (audit N-6). DHCP hands those clients the
	// custom servers instead of the gateway, and nothing opened them pre-authentication: a client could resolve
	// nothing, so neither its captive check nor the sign-in page's own names worked. Port 53 only, to exactly the
	// configured addresses, from that network only (rate-limited above like all pre-auth DNS). Before the
	// management drop, because a hotel's own resolver commonly sits in a management range and the operator named
	// it explicitly.
	for _, n := range enabled {
		if s := customDNSServers(n); s != "" {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" ip daddr { %s } meta l4proto { tcp, udp } th dport 53 accept comment \"custom DNS (pre-auth)\"\n",
				n.BridgeName, s)
		}
	}
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
	// The Phase-3 gate. While the set is empty this rule can never match, so a dark appliance is unaffected.
	fmt.Fprintf(&b, "\t\toifname \"%s\" iifname . ip saddr @phase3_auth_ipv4 accept comment \"phase-3 authorized guests\"\n", topo.WANInterface)
	// walled-garden pre-auth -> WAN for any guest interface: every port to an address-only element, and exactly
	// the named port to a port-restricted one.
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr @walled_garden_ip accept\n", topo.WANInterface)
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr . tcp dport @walled_garden_tcp accept\n", topo.WANInterface)
	fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" ip daddr . udp dport @walled_garden_udp accept\n", topo.WANInterface)
	// AN UNAUTHENTICATED HTTPS ATTEMPT GOES UNANSWERED, AND ON PURPOSE.
	//
	// With no TLS portal there is no redirect for a guest's HTTPS to another host: the appliance holds a
	// certificate for its own portal names and nobody else's, and intercepting or impersonating TLS is ruled
	// out. So the packet is dropped -- never redirected, never accepted, never sent out of the WAN.
	//
	// THIS USED TO BE `reject with tcp reset`, on the reasoning that failing fast prompts the browser to fall
	// back to its captive-portal check. For desktop Chrome and Edge it does the opposite. Chromium's
	// CaptivePortalTabReloader (components/captive_portal/content/captive_portal_tab_reloader.cc) starts a
	// captive-portal check -- and then opens its login tab -- only when an HTTPS load fails with
	// ERR_CONNECTION_TIMED_OUT, ERR_SSL_PROTOCOL_ERROR or a certificate error, or when its 30-second slow-SSL
	// timer fires on a load still in progress. A reset or a refusal returns the reloader to its idle state and
	// NO check runs: a guest who opened an https:// site got a bare error page and never saw the login tab.
	//
	// A silent drop leaves the SYN unanswered, so the load either reaches the slow-SSL timer or times out, and
	// both run Chrome's check -- which probes over plain HTTP and is answered by the port-80 captive redirect.
	// HTTPS-Upgrades still works too: a navigation Chrome upgraded to https:// falls back to http:// after its
	// fallback delay on ANY network error, a timeout included, and that http:// load is redirected the same way.
	// UDP 443 (QUIC) is not touched here: the chain's policy drops it, as before, and browsers fall back to TCP.
	//
	// It stays an explicit rule rather than being left to the policy, because it is a decision. It sits AFTER
	// both authorized-guest accepts and the walled garden, so it can only ever match a guest who has not
	// signed in.
	if topo.PortalTLSPort == 0 {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces oifname \"%s\" tcp dport 443 drop comment \"no TLS portal: unanswered, so browsers run their captive check\"\n", topo.WANInterface)
	}
	b.WriteString("\t}\n\n")

	// --- prerouting nat (captive redirect per network to its own gateway) ---
	b.WriteString("\tchain prerouting_nat {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	// THE PORTAL ADDRESS, FIRST. A guest -- signed in or not -- that connects to the portal address reaches the
	// portal: 443 to portald's TLS listener, 80 to its HTTP listener, both on the portal address itself, so the
	// packet stays local and is decided by the input chain above.
	//
	// THIS IS THE ONLY 443 REDIRECT IN THE RULESET, AND IT IS BOUND TO ONE DESTINATION. `ip daddr <portal
	// address>` is the whole of its scope: a guest's HTTPS to any other host is never redirected (PortalTLSPort
	// stays zero), because the appliance holds a certificate for its own portal names and nobody else's.
	//
	// It sits before the walled-garden accept and before the per-network captive DNAT, so neither can claim a
	// packet addressed to the portal; a nat rule's dnat ends the chain for that packet.
	if vip := renderPortalVIP(topo); vip != "" {
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s tcp dport 443 dnat ip to %s:%d comment \"portal address: TLS portal\"\n",
			vip, vip, PortalAddressTLSPort)
		fmt.Fprintf(&b, "\t\tiifname @guest_interfaces ip daddr %s tcp dport 80 dnat ip to %s:%d comment \"portal address: HTTP portal\"\n",
			vip, vip, portalHTTPPort(topo))
	}
	// A port-restricted garden pair on 80 or 443 is reachable as itself: accepting here ends NAT processing for
	// the packet, so the captive redirect below (which excludes only the address-only set) never claims it.
	b.WriteString("\t\tiifname @guest_interfaces ip daddr . tcp dport @walled_garden_tcp accept comment \"port-restricted garden: no captive redirect\"\n")
	for _, n := range enabled {
		if !n.CaptiveEnabled {
			continue
		}
		br := n.BridgeName
		gw := n.GatewayIP
		// A guest authorized in EITHER set must not be captive-redirected. Omitting the Phase-3 exclusion
		// would leave an authorized Phase-3 guest's web traffic DNAT'd to the portal — internet "granted"
		// but every page still the login screen.
		fmt.Fprintf(&b, "\t\tiifname \"%s\" iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport 80  dnat ip to %s:%d\n",
			br, gw, topo.PortalHTTPPort)
		// PORT 443 IS REDIRECTED ONLY IF THERE IS SOMETHING TO REDIRECT IT TO.
		//
		// This rule used to be emitted unconditionally, to a port that has never had a listener, so every
		// unauthenticated guest who opened an HTTPS site was DNAT'd to a closed socket. The kernel answered
		// with a reset, which at least failed quickly -- but the firewall was describing a captive TLS portal
		// that does not exist, and the next person to read the ruleset would reasonably believe it did.
		if topo.PortalTLSPort > 0 {
			fmt.Fprintf(&b, "\t\tiifname \"%s\" iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport 443 dnat ip to %s:%d\n",
				br, gw, topo.PortalTLSPort)
		}
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
	return b.String()
}

// renderPortalVIP is the portal address as it may appear in the ruleset, or "" for none.
//
// A malformed value renders NOTHING rather than a broken line. The ruleset is one atomic `nft -f`: a single
// syntax error does not lose the portal, it loses every guest network at once (the same fail-safe as a missing
// gateway above). Validation refuses such a value long before this, so reaching here with one means something
// wrote the setting behind the validator's back, and the safe reading is "no portal address".
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

// BaselineResolvers are the public resolvers every pre-auth device may reach on port 53, TCP and UDP. The render
// declares them as the static elements of walled_garden_tcp / walled_garden_udp; the converge does not carry
// them across a structural replace, because the new ruleset already declares them (nftconverge).
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
