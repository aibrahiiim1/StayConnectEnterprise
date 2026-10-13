package netcfg

// THE PORTAL ADDRESS IN THE RULESET (contract ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN §2.1, §9).
//
// The portal is served over TLS on one private /32 that exists only on the appliance. What the firewall must make
// true, and what these tests read back from the RENDERED ruleset rather than from intent:
//
//	a guest -- signed in or not -- reaches the portal on that address, 443 -> 8343 and 80 -> 8380;
//	nothing else on the address is open, and nothing but a guest network may address it at all;
//	the address is never routed and never NATed, whatever state its interface is in;
//	and the one 443 redirect in the ruleset is bound to that ONE destination: arbitrary 443 is still never
//	intercepted (render_nft_no_tls_portal_test.go holds that half).

import (
	"strings"
	"testing"
)

const testVIP = "10.255.255.1"

func vipNets() []GuestNetwork {
	return []GuestNetwork{
		{ID: "n1", Name: "Rooms", BridgeName: "br-g221", GatewayIP: "10.221.0.1", SubnetCIDR: "10.221.0.0/22",
			PrefixLen: 22, NetworkType: "vlan", VLANID: 221, Enabled: true, CaptiveEnabled: true,
			InternetEnabled: true, NATEnabled: true},
		{ID: "n2", Name: "Lobby", BridgeName: "br-g222", GatewayIP: "10.222.0.1", SubnetCIDR: "10.222.0.0/24",
			PrefixLen: 24, NetworkType: "vlan", VLANID: 222, Enabled: true, CaptiveEnabled: true,
			InternetEnabled: true, NATEnabled: true},
	}
}

func renderVIP(t *testing.T, vip string) string {
	t.Helper()
	topo := DefaultTopology()
	topo.PortalVIP = vip
	return string(RenderNftables(vipNets(), topo))
}

// chainOf returns one chain's body, so an assertion about one hook cannot be satisfied by another.
func chainOf(t *testing.T, out, name string) string {
	t.Helper()
	i := strings.Index(out, "chain "+name+" {")
	if i < 0 {
		t.Fatalf("no %s chain was rendered", name)
	}
	rest := out[i:]
	if j := strings.Index(rest, "\n\tchain "); j > 0 {
		rest = rest[:j]
	}
	return rest
}

func idx(t *testing.T, hay, needle string) int {
	t.Helper()
	i := strings.Index(hay, needle)
	if i < 0 {
		t.Fatalf("missing rule: %s\n---\n%s", needle, hay)
	}
	return i
}

const (
	vipDNAT443  = `iifname @guest_interfaces ip daddr 10.255.255.1 tcp dport 443 dnat ip to 10.255.255.1:8343 comment "portal address: TLS portal"`
	vipDNAT80   = `iifname @guest_interfaces ip daddr 10.255.255.1 tcp dport 80 dnat ip to 10.255.255.1:8380 comment "portal address: HTTP portal"`
	vipAccept   = `iifname @guest_interfaces ip daddr 10.255.255.1 tcp dport { 8343, 8380 } accept comment "portal address (TLS and HTTP portal)"`
	vipNonGuest = `iifname != @guest_interfaces ip daddr 10.255.255.1 drop comment "portal address: guest networks only"`
	vipFwdDst   = `ip daddr 10.255.255.1 drop comment "portal address is never routed"`
	vipFwdSrc   = `ip saddr 10.255.255.1 drop comment "portal address is never routed (source)"`
)

func TestPortalVIP_NoAddressRendersNothingNew(t *testing.T) {
	// "" is "this appliance has no portal address": the ruleset is byte-for-byte what it was before the feature,
	// so the render fingerprint does not move and a restart of an unconfigured appliance changes nothing.
	topo := DefaultTopology()
	before := string(RenderNftables(vipNets(), topo))
	if strings.Contains(before, "portal address") || strings.Contains(before, "8343") {
		t.Fatal("a ruleset with no portal address mentions one")
	}
	topo.PortalVIP = "not-an-address"
	if got := string(RenderNftables(vipNets(), topo)); got != before {
		t.Fatal("a malformed portal address changed the ruleset; it must render as none (one bad line breaks every network)")
	}
}

func TestPortalVIP_PreroutingRedirectsOnlyThePortalAddressAndComesFirst(t *testing.T) {
	out := renderVIP(t, testVIP)
	pre := chainOf(t, out, "prerouting_nat")

	at443 := idx(t, pre, vipDNAT443)
	at80 := idx(t, pre, vipDNAT80)
	// BEFORE the walled-garden accept (which ends NAT for a matching packet) and BEFORE every per-network captive
	// DNAT (which would send the guest's port 80 to its gateway instead of the portal address).
	garden := idx(t, pre, "ip daddr . tcp dport @walled_garden_tcp accept")
	captive := idx(t, pre, "tcp dport 80  dnat ip to 10.221.0.1:8380")
	for _, at := range []int{at443, at80} {
		if at > garden || at > captive {
			t.Fatalf("the portal-address redirect is ordered after a rule that would claim its packet:\n%s", pre)
		}
	}
	// THE REDIRECT IS FOR ANY AUTH STATE: it must not carry an auth-set exclusion, or a signed-in guest could not
	// open the portal (to see their session, sign out, buy more time).
	for _, line := range strings.Split(pre, "\n") {
		if strings.Contains(line, "ip daddr 10.255.255.1") && strings.Contains(line, "@auth_ipv4") {
			t.Fatalf("the portal-address redirect depends on authentication state: %s", line)
		}
	}
	// EVERY 443 DNAT IN THE WHOLE RULESET IS BOUND TO THE PORTAL ADDRESS. This is the guarantee the
	// no-TLS-portal tests were written for: arbitrary HTTPS is never intercepted.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "dport 443") && strings.Contains(line, "dnat") && !strings.Contains(line, "ip daddr 10.255.255.1 ") {
			t.Fatalf("a 443 redirect that is not bound to the portal address: %s", line)
		}
	}
	if n := strings.Count(out, "tcp dport 443 dnat"); n != 1 {
		t.Fatalf("want exactly one 443 redirect (the portal address), got %d", n)
	}
}

func TestPortalVIP_InputAdmitsGuestsOnTheTwoListenersOnly(t *testing.T) {
	out := renderVIP(t, testVIP)
	in := chainOf(t, out, "input")

	if !strings.Contains(in, "type filter hook input priority filter; policy drop;") {
		t.Fatal("the input chain is no longer default-deny")
	}
	acceptAt := idx(t, in, vipAccept)
	guestDropAt := idx(t, in, `iifname @guest_interfaces drop comment "guest input is limited to its own gateway"`)
	if acceptAt > guestDropAt {
		t.Fatal("the portal-address accept is after the guest drop and can never match")
	}
	// Nothing else on the portal address: exactly one accept names it, and it names exactly the two listeners.
	n := 0
	for _, line := range strings.Split(in, "\n") {
		if strings.Contains(line, "10.255.255.1") && strings.Contains(line, "accept") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one accept naming the portal address, got %d:\n%s", n, in)
	}
}

func TestPortalVIP_WANAndManagementCannotReachIt(t *testing.T) {
	// The policy drop does NOT cover this on its own: the management accepts for SSH (22) and the admin UI (443
	// when no MgmtAddr pins it) are keyed on the interface, not the destination. The explicit drop must therefore
	// come before them -- and after the established accept and loopback, which it must not disturb.
	out := renderVIP(t, testVIP)
	in := chainOf(t, out, "input")

	dropAt := idx(t, in, vipNonGuest)
	for _, mgmt := range []string{
		`iifname "ens160" tcp dport 22 accept`,
		`iifname "ens160" tcp dport 443 accept comment "Caddy TLS (admin)"`,
		`iifname "ens160" icmp type echo-request accept`,
	} {
		if at := idx(t, in, mgmt); at < dropAt {
			t.Fatalf("management rule %q precedes the portal-address drop, so the WAN/management side could reach the portal address through it", mgmt)
		}
	}
	if lo := idx(t, in, `iif "lo" accept`); lo > dropAt {
		t.Fatal("the portal-address drop precedes the loopback accept; the appliance's own checks could not reach it")
	}
	if est := idx(t, in, "ct state established,related accept"); est > dropAt {
		t.Fatal("the portal-address drop precedes the established accept")
	}
	// No accept anywhere in the input chain names the WAN/mgmt interface AND the portal address.
	for _, line := range strings.Split(in, "\n") {
		if strings.Contains(line, `"ens160"`) && strings.Contains(line, "10.255.255.1") {
			t.Fatalf("a WAN/management rule names the portal address: %s", line)
		}
	}
	// And with MgmtAddr pinned the same holds.
	topo := DefaultTopology()
	topo.PortalVIP, topo.MgmtAddr = testVIP, "172.21.60.25"
	pinned := chainOf(t, string(RenderNftables(vipNets(), topo)), "input")
	if idx(t, pinned, vipNonGuest) > idx(t, pinned, `iifname "ens160" tcp dport 22 accept`) {
		t.Fatal("with a pinned management address the portal-address drop no longer precedes SSH")
	}
}

func TestPortalVIP_IsNeverRoutedNorNATed(t *testing.T) {
	out := renderVIP(t, testVIP)
	fwd := chainOf(t, out, "forward")
	post := chainOf(t, out, "postrouting_nat")

	dst := idx(t, fwd, vipFwdDst)
	src := idx(t, fwd, vipFwdSrc)
	est := idx(t, fwd, "ct state established,related accept")
	if dst > est || src > est {
		t.Fatal("the never-routed drops come after the established accept, so an existing flow would be exempt")
	}
	// Before every accept in the forward chain.
	for _, line := range strings.Split(fwd, "\n") {
		if strings.Contains(line, " accept") {
			if strings.Index(fwd, line) < dst {
				t.Fatalf("a forward accept precedes the portal-address drop: %s", line)
			}
		}
		if strings.Contains(line, "10.255.255.1") && strings.Contains(line, "accept") {
			t.Fatalf("a forward accept names the portal address: %s", line)
		}
	}
	if strings.Contains(post, "10.255.255.1") {
		t.Fatalf("the portal address appears in postrouting NAT:\n%s", post)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "masquerade") && strings.Contains(line, "10.255.255.1") {
			t.Fatalf("the portal address is masqueraded: %s", line)
		}
	}
}

// THE UNAUTHENTICATED-443 REFUSAL CANNOT CATCH PORTAL TRAFFIC. That reset lives in the FORWARD chain; a guest's
// 443 to the portal address is DNAT'd in prerouting to the portal address itself -- a local address -- so the
// routing decision sends it to INPUT, and the forward chain never sees it. The render proves the three facts that
// argument rests on: the DNAT target is the portal address (local, not forwarded), input admits the translated
// port, and the reset is a forward-chain rule ordered after the forward never-routed drop.
func TestPortalVIP_TheForward443ResetIsNotOnThePortalPath(t *testing.T) {
	out := renderVIP(t, testVIP)
	if !strings.Contains(out, "dnat ip to 10.255.255.1:8343") {
		t.Fatal("the 443 redirect does not target the portal address itself")
	}
	idx(t, chainOf(t, out, "input"), "tcp dport { 8343, 8380 } accept")
	fwd := chainOf(t, out, "forward")
	reset := idx(t, fwd, "tcp dport 443 reject with tcp reset")
	if strings.Contains(chainOf(t, out, "input"), "reject with tcp reset") {
		t.Fatal("a 443 reset was rendered into the input chain, where it would refuse the portal")
	}
	if idx(t, fwd, vipFwdDst) > reset {
		t.Fatal("the never-routed drop is ordered after the 443 reset")
	}
	if strings.Contains(fwd, "8343") {
		t.Fatal("the forward chain mentions the portal listener; portal traffic is input traffic")
	}
}

func TestPortalVIP_ChangesTheFingerprintAndOnlyWhenItChanges(t *testing.T) {
	topo := DefaultTopology()
	none := RenderFingerprint(vipNets(), topo)
	topo.PortalVIP = testVIP
	a := RenderFingerprint(vipNets(), topo)
	topo.PortalVIP = "10.254.254.1"
	b := RenderFingerprint(vipNets(), topo)
	if none == a || a == b {
		t.Fatal("the portal address must be part of the render fingerprint, or a changed address would never reach the kernel")
	}
	// The HTTPS state is NOT structure: flipping it must not touch the ruleset.
	topo.PortalVIP = testVIP
	topo.PortalTLS = PortalTLS{HTTPSActive: true, CanonicalHostname: "abcdefgh.gw.onegate.work"}
	if RenderFingerprint(vipNets(), topo) != a {
		t.Fatal("the HTTPS state changed the ruleset; whether HTTPS is active must never change what the firewall admits")
	}
}
