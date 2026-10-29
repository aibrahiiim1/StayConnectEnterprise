package netcfg

// AD-23 (a): with binding on, the forward accept and every "is this device authorized" exemption key on (bridge, IP, MAC);
// with it off the ruleset is the IP-only one. The MAC-bound set is declared in both so the fallback is a rule change only.

import (
	"strings"
	"testing"
)

func macTopo() Topology {
	t := topoT()
	t.MACBinding = true
	return t
}

func TestMACBindingKeysTheAcceptOnTheDeviceAddress(t *testing.T) {
	t.Parallel()
	out := string(RenderNftables(twoNetworks(), macTopo()))
	if !strings.Contains(out, "iifname . ip saddr . ether saddr @phase3_auth_mac_ipv4 accept") {
		t.Fatal("the authorized-guest accept does not match the device address")
	}
	if strings.Contains(out, "iifname . ip saddr @phase3_auth_ipv4 accept") {
		t.Fatal("an IP-only accept survives next to the MAC-bound one: spoofing the address would still work")
	}
	if strings.Contains(out, "!= @phase3_auth_ipv4") {
		t.Fatal("an exemption still keys on the IP-only set")
	}
	if got := strings.Count(out, "ether saddr != @phase3_auth_mac_ipv4"); got != 4 {
		t.Fatalf("expected the 4 captive/DNS exemptions to use the MAC-bound set, got %d", got)
	}
	if !strings.Contains(out, "type ifname . ipv4_addr . ether_addr") {
		t.Fatal("the MAC-bound set is not a three-part key")
	}
}

func TestMACBindingOffIsTheIPOnlyRulesetWithAWarmSetDeclared(t *testing.T) {
	t.Parallel()
	out := string(RenderNftables(twoNetworks(), topoT()))
	if !strings.Contains(out, "iifname . ip saddr @phase3_auth_ipv4 accept") {
		t.Fatal("fallback lost the IP-only accept")
	}
	if strings.Contains(out, "ether saddr @") || strings.Contains(out, "ether saddr !=") {
		t.Fatal("the fallback ruleset still matches on the device address")
	}
	if !strings.Contains(out, "set phase3_auth_mac_ipv4 {") {
		t.Fatal("the MAC-bound set must be declared even while binding is off, so netd can keep it warm")
	}
}

func TestMACBindingChangesTheFingerprintBothWays(t *testing.T) {
	t.Parallel()
	if RenderFingerprint(twoNetworks(), topoT()) == RenderFingerprint(twoNetworks(), macTopo()) {
		t.Fatal("toggling the binding must re-assert the ruleset")
	}
}

func TestMACBindingDoesNotTouchTheInputChainOrThePrivatePolicy(t *testing.T) {
	t.Parallel()
	a, b := topoT(), macTopo()
	a.PrivateReach, b.PrivateReach = PrivateReach{Deny: true}, PrivateReach{Deny: true}
	strip := func(s string) string { // the pre-auth DNS limiter's exemption is meant to follow the binding
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.Contains(l, "dns_preauth_rl") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	if strip(inputChain(t, string(RenderNftables(twoNetworks(), a)))) != strip(inputChain(t, string(RenderNftables(twoNetworks(), b)))) {
		t.Fatal("the binding changed the input chain beyond the DNS-limiter exemption")
	}
	if !strings.Contains(string(RenderNftables(twoNetworks(), b)), "guest_private_ranges") {
		t.Fatal("the two policies must compose")
	}
}
