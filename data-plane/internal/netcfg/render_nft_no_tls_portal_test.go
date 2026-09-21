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

func TestWithNoTLSPortalNothingRedirectsTo443AndTheRefusalIsPrompt(t *testing.T) {
	out := renderWith(t, 0)

	if strings.Contains(out, "tcp dport 443 dnat") {
		t.Error("port 443 is still redirected, and there is no listener to redirect it to")
	}
	// Port 80 must keep working: it is the captive path every operating system actually probes.
	if !strings.Contains(out, "tcp dport 80  dnat ip to 192.168.77.1:8380") {
		t.Fatal("the HTTP captive redirect was lost; captive-portal detection depends on it")
	}
	// A silent drop would leave the guest's browser spinning for half a minute. The reset is what keeps the
	// failure immediate, which is also what prompts the OS to fall back to its captive probe.
	if !strings.Contains(out, `tcp dport 443 reject with tcp reset`) {
		t.Error("an unauthenticated HTTPS attempt is silently dropped; it should be refused promptly")
	}
	// ...and the refusal must come after the accepts, or it would refuse authorized guests too.
	rejectAt := strings.Index(out, "tcp dport 443 reject with tcp reset")
	authAt := strings.Index(out, `ip saddr @auth_ipv4 accept comment "authenticated guests"`)
	gardenAt := strings.Index(out, "ip daddr @walled_garden_ip accept")
	if rejectAt < authAt || rejectAt < gardenAt {
		t.Error("the 443 refusal is ordered before an accept, so it would refuse an authorized guest")
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
	if strings.Contains(out, "tcp dport 443 reject with tcp reset") {
		t.Error("an appliance with a working TLS portal should redirect, not refuse")
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
