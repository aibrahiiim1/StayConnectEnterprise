package netcfg

// The portal address in the rendered ruleset: a guest, signed in or not, reaches the portal on that address (443 ->
// 8343, 80 -> 8380); nothing else on the address is open and only a guest network may address it; the address is
// never routed or NATed; and the one 443 redirect is bound to that destination (arbitrary 443 is never intercepted,
// see render_nft_no_tls_portal_test.go).

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
	// "" means no portal address: the ruleset is byte-for-byte what it was before the feature, so the fingerprint does not move.
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
	// Before the walled-garden accept (which ends NAT) and every per-network captive DNAT (which would send port 80 to
	// the gateway).
	garden := idx(t, pre, "ip daddr . tcp dport @walled_garden_tcp accept")
	captive := idx(t, pre, "tcp dport 80  dnat ip to 10.221.0.1:8380")
	for _, at := range []int{at443, at80} {
		if at > garden || at > captive {
			t.Fatalf("the portal-address redirect is ordered after a rule that would claim its packet:\n%s", pre)
		}
	}
	// The redirect applies in any auth state: an auth-set exclusion would stop a signed-in guest opening the portal.
	for _, line := range strings.Split(pre, "\n") {
		if strings.Contains(line, "ip daddr 10.255.255.1") && strings.Contains(line, "@auth_ipv4") {
			t.Fatalf("the portal-address redirect depends on authentication state: %s", line)
		}
	}
	// Every 443 DNAT in the ruleset is bound to the portal address: arbitrary HTTPS is never intercepted.
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
	// The policy drop does not cover this: the SSH and admin UI accepts key on interface, not destination, so the
	// explicit drop must come before them and after the established accept and loopback.
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

// The unauthenticated-443 drop cannot catch portal traffic: it is in the forward chain, but a guest's 443 to the
// portal address is DNAT'd to a local address and goes to INPUT. The render shows the DNAT target is the portal
// address, input admits the translated port, and the drop is ordered after the forward never-routed drop.
func TestPortalVIP_TheForward443DropIsNotOnThePortalPath(t *testing.T) {
	out := renderVIP(t, testVIP)
	if !strings.Contains(out, "dnat ip to 10.255.255.1:8343") {
		t.Fatal("the 443 redirect does not target the portal address itself")
	}
	idx(t, chainOf(t, out, "input"), "tcp dport { 8343, 8380 } accept")
	fwd := chainOf(t, out, "forward")
	drop := idx(t, fwd, `oifname "ens160" tcp dport 443 drop`)
	if strings.Contains(chainOf(t, out, "input"), "tcp dport 443 drop") {
		t.Fatal("a 443 drop was rendered into the input chain, where it would refuse the portal")
	}
	if strings.Contains(out, "reject") {
		t.Fatal("the ruleset answers something with a reject; an unauthenticated 443 must go unanswered")
	}
	if idx(t, fwd, vipFwdDst) > drop {
		t.Fatal("the never-routed drop is ordered after the 443 drop")
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
