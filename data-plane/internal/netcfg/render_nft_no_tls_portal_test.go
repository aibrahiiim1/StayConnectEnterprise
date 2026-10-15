package netcfg

// THE FIREWALL STOPS ADVERTISING A LISTENER THE APPLIANCE DOES NOT RUN.
//
// Every unauthenticated guest's port-443 connection was DNAT'd to the captive portal's TLS port. portald
// serves TLS only if a certificate exists at PORTALD_CERT; no tooling in this repository has ever provisioned
// one, and /etc/stayconnect/tls does not exist on the appliance. So the redirect pointed at a socket that has
// never been open, for as long as the guest network has existed.
//
// The Product Owner authorised removing that redirect, and explicitly NOT introducing HTTPS interception, a
// certificate, a portal PKI model or any trust-root change. These tests hold both halves: the phantom
// redirect is gone, and nothing was added in its place that terminates TLS.
//
// The property that matters most is the one a mistake here would break silently. Removing a DNAT must not
// turn a blocked connection into an allowed one: an unauthenticated guest must still not reach the internet
// on 443. That is asserted first and from the rendered ruleset, not from intent.
//
// WHAT IS TRUE NOW (contract ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN §2.1). The portal does have a TLS listener
// today, with a publicly trusted certificate for the appliance's OWN portal names -- and it is reachable ONLY
// behind the portal address, one private /32 on the appliance, by a rule bound to that single destination
// (render_nft_portal_vip_test.go). Nothing here changed: PortalTLSPort is still zero, no gateway redirects 443,
// and a guest's HTTPS to any other host is still never intercepted, whether or not a portal address exists.
// TestArbitrary443IsStillNeverInterceptedWithAPortalAddress holds that with the portal address in the render.

import (
	"strings"
	"testing"
)

func renderWith(t *testing.T, tlsPort int) string {
	t.Helper()
	topo := DefaultTopology()
	topo.PortalTLSPort = tlsPort
	nets := []GuestNetwork{{
		ID: "n1", Name: "Pilot Guest", BridgeName: "br-g-test", GatewayIP: "192.168.77.1",
		SubnetCIDR: "192.168.77.0/24", PrefixLen: 24, NetworkType: "untagged",
		Enabled: true, CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
	}}
	return string(RenderNftables(nets, topo))
}

func TestAnUnauthenticatedGuestStillCannotReachTheInternetOn443(t *testing.T) {
	// THE ONE THING THIS CHANGE MUST NOT DO. The forward chain is policy drop and only authorized guests and
	// the walled garden are accepted out of the WAN; nothing about removing a redirect may add a path.
	out := renderWith(t, 0)

	if !strings.Contains(out, "chain forward {\n\t\ttype filter hook forward priority filter; policy drop;") {
		t.Fatal("the forward chain is no longer default-deny")
	}
	for _, required := range []string{
		`ip saddr @auth_ipv4 accept comment "authenticated guests"`,
		`ip saddr @phase3_auth_ipv4 accept comment "phase-3 authorized guests"`,
	} {
		if !strings.Contains(out, required) {
			t.Errorf("an authorization path was lost: %s", required)
		}
	}
	// No rule may accept guest 443 OUTBOUND. Scoped to the forward chain on purpose: the input chain
	// legitimately accepts 443 on the management interface for Hotel Admin, and an assertion over the whole
	// ruleset would have demanded that rule be removed to pass -- which is how a security test starts
	// breaking security.
	fwd := forwardChain(t, out)
	for _, forbidden := range []string{"tcp dport 443 accept", "tcp dport 443 counter accept"} {
		if strings.Contains(fwd, forbidden) {
			t.Errorf("unauthenticated guest HTTPS is ACCEPTED (%q) — this would be open internet before sign-in", forbidden)
		}
	}
	// The admin rule is on the management interface and is none of this test's business, but losing it would
	// lock the operator out, so its survival is asserted rather than left to chance.
	if !strings.Contains(out, `iifname "ens160" tcp dport 443 accept comment "Caddy TLS (admin)"`) {
		t.Error("the management admin rule on 443 was removed; that is the operator's own way in")
	}
}

// forwardChain returns only the forward chain, so a guest-egress assertion cannot be satisfied or broken by
// a rule that governs the management interface.
func forwardChain(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "chain forward {")
	if i < 0 {
		t.Fatal("no forward chain was rendered")
	}
	rest := out[i:]
	if j := strings.Index(rest, "\n\tchain "); j > 0 {
		rest = rest[:j]
	}
	return rest
}

// unauthenticated443Drop is the one rule that decides an unsigned-in guest's outbound HTTPS.
const unauthenticated443Drop = `iifname @guest_interfaces oifname "ens160" tcp dport 443 drop comment "no TLS portal: unanswered, so browsers run their captive check"`

func TestWithNoTLSPortalNothingRedirectsTo443AndItGoesUnanswered(t *testing.T) {
	out := renderWith(t, 0)

	if strings.Contains(out, "tcp dport 443 dnat") {
		t.Error("port 443 is still redirected, and there is no listener to redirect it to")
	}
	// Port 80 must keep working: it is the captive path every operating system actually probes.
	if !strings.Contains(out, "tcp dport 80  dnat ip to 192.168.77.1:8380") {
		t.Fatal("the HTTP captive redirect was lost; captive-portal detection depends on it")
	}
	// A RESET HIDES THE LOGIN TAB. Chromium's captive-portal tab reloader runs its check only after an HTTPS load
	// times out, fails TLS or trips the slow-SSL timer; a reset or refusal cancels it. So the unauthenticated 443
	// is dropped silently, and nothing anywhere in the ruleset answers it.
	fwd := forwardChain(t, out)
	if !strings.Contains(fwd, unauthenticated443Drop) {
		t.Error("an unauthenticated HTTPS attempt is not silently dropped")
	}
	if strings.Contains(out, "reject") || strings.Contains(out, "tcp reset") {
		t.Error("the ruleset answers an unauthenticated connection with a reject/reset; Chrome then never opens its login tab")
	}
	// ...and the drop must come after every accept, or it would cut off authorized guests and the garden too.
	// It is the forward chain's LAST rule: nothing after it could re-open 443.
	dropAt := strings.Index(fwd, unauthenticated443Drop)
	for _, accept := range []string{
		`ip saddr @auth_ipv4 accept comment "authenticated guests"`,
		`ip saddr @phase3_auth_ipv4 accept comment "phase-3 authorized guests"`,
		"ip daddr @walled_garden_ip accept",
		"ip daddr . tcp dport @walled_garden_tcp accept",
		"ip daddr . udp dport @walled_garden_udp accept",
	} {
		if i := strings.Index(fwd, accept); i < 0 || i > dropAt {
			t.Errorf("the 443 drop is ordered before %q, so it would cut off an allowed flow", accept)
		}
	}
	if rest := strings.TrimSpace(fwd[dropAt+len(unauthenticated443Drop):]); rest != "}" {
		t.Errorf("rules follow the 443 drop in the forward chain: %q", rest)
	}
	// UDP 443 (QUIC) is left to the policy drop, as before: no rule accepts or answers it.
	for _, line := range strings.Split(fwd, "\n") {
		if strings.Contains(line, "udp dport 443") {
			t.Errorf("a forward rule names UDP 443; QUIC must stay with the policy drop: %s", strings.TrimSpace(line))
		}
	}
	// The dead port is no longer opened on the input chain either.
	if strings.Contains(out, "tcp dport { 8380, 8343 }") || strings.Contains(out, "8343") {
		t.Error("the TLS portal port is still opened for guests; nothing listens on it")
	}
}

func TestAnApplianceThatDOESServeATLSPortalIsUnchanged(t *testing.T) {
	// The behaviour is conditional on the fact, not deleted. An appliance that genuinely runs a TLS portal
	// renders exactly what it rendered before, so this is a correction rather than a removal of the feature.
	out := renderWith(t, 8343)

	if !strings.Contains(out, "tcp dport 443 dnat ip to 192.168.77.1:8343") {
		t.Error("an appliance with a TLS portal no longer redirects 443 to it")
	}
	if !strings.Contains(out, "tcp dport { 8380, 8343 } accept") {
		t.Error("an appliance with a TLS portal no longer opens the port")
	}
	if strings.Contains(out, "tcp dport 443 drop") || strings.Contains(out, "reject") {
		t.Error("an appliance with a working TLS portal should redirect, not drop or refuse")
	}
}

func TestArbitrary443IsStillNeverInterceptedWithAPortalAddress(t *testing.T) {
	// The same guarantees as above, with the portal address in the render. The only 443 redirect is the one bound
	// to `ip daddr <portal address>`; no gateway redirect exists; the silent drop of everything else stays.
	topo := DefaultTopology()
	topo.PortalVIP = "10.255.255.1"
	nets := []GuestNetwork{{
		ID: "n1", Name: "Pilot Guest", BridgeName: "br-g-test", GatewayIP: "192.168.77.1",
		SubnetCIDR: "192.168.77.0/24", PrefixLen: 24, NetworkType: "untagged",
		Enabled: true, CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
	}}
	out := string(RenderNftables(nets, topo))

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "tcp dport 443") && strings.Contains(line, "dnat") &&
			!strings.Contains(line, "ip daddr 10.255.255.1 tcp dport 443 dnat ip to 10.255.255.1:8343") {
			t.Errorf("a 443 redirect that is not bound to the portal address: %s", strings.TrimSpace(line))
		}
	}
	if strings.Contains(out, "dnat ip to 192.168.77.1:8343") || strings.Contains(out, "192.168.77.1 tcp dport { 8380, 8343 }") {
		t.Error("the gateway serves the TLS port; it must exist only behind the portal address")
	}
	fwd := forwardChain(t, out)
	if !strings.Contains(fwd, unauthenticated443Drop) || strings.Contains(out, "reject") {
		t.Error("the silent drop of unauthenticated HTTPS was lost (or became a refusal) when a portal address was added")
	}
	for _, forbidden := range []string{"tcp dport 443 accept", "tcp dport 443 counter accept"} {
		if strings.Contains(fwd, forbidden) {
			t.Errorf("unauthenticated guest HTTPS is ACCEPTED (%q)", forbidden)
		}
	}
	// The ruleset still carries no TLS material: the certificate lives with portald, not in the firewall.
	for _, forbidden := range []string{"certificate", "cert ", ".crt", ".pem", "acme", "self-signed"} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("the rendered ruleset mentions %q", forbidden)
		}
	}
}

func TestNothingHereTerminatesTLSOrCarriesACertificate(t *testing.T) {
	// The authorisation was explicit: no HTTPS interception, no certificate impersonation, no portal PKI
	// model, no trust-root change. A renderer is not where such a thing would live, which is exactly why a
	// cheap assertion is worth keeping — it fails loudly if somebody later "restores" HTTPS by this route.
	for _, port := range []int{0, 8343} {
		out := renderWith(t, port)
		for _, forbidden := range []string{"certificate", "cert ", ".crt", ".pem", "acme", "self-signed"} {
			if strings.Contains(strings.ToLower(out), forbidden) {
				t.Errorf("the rendered ruleset mentions %q; TLS material has no business here", forbidden)
			}
		}
	}
}
