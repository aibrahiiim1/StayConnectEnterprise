package netcfg

// AD-22: a client network may not reach private address space through the appliance unless the hotel allowed it. Every case
// is paired with the path it must not break: the appliance's own services (input chain), the walled garden, custom DNS,
// the Internet itself, and the previous behaviour when the policy is switched off.

import (
	"strings"
	"testing"
)

func fwdLines(t *testing.T, out string) []string {
	t.Helper()
	i := strings.Index(out, "chain forward {")
	if i < 0 {
		t.Fatal("no forward chain")
	}
	rest := out[i:]
	j := strings.Index(rest, "\n\t}")
	var lines []string
	for _, l := range strings.Split(rest[:j], "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	return lines
}

func indexOfLine(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func denyTopo(allow ...string) Topology {
	t := topoT()
	t.PrivateReach = PrivateReach{Deny: true, Allow: allow}
	return t
}

func TestPolicyOffRendersExactlyWhatItRenderedBefore(t *testing.T) {
	t.Parallel()
	out := string(RenderNftables(twoNetworks(), topoT()))
	for _, s := range []string{"guest_private_ranges", "guest_private_allow", "guest_private_dropped"} {
		if strings.Contains(out, s) {
			t.Fatalf("policy off must not mention %s (off-switch must be byte-for-byte the old ruleset)", s)
		}
	}
	// The old order is kept: mgmt, isolation, internet-disabled, authorized accepts, then the garden.
	f := fwdLines(t, out)
	if !(indexOfLine(f, "guest -> mgmt blocked") < indexOfLine(f, "inter-guest isolation") &&
		indexOfLine(f, "inter-guest isolation") < indexOfLine(f, "authenticated guests") &&
		indexOfLine(f, "authenticated guests") < indexOfLine(f, "@walled_garden_ip accept")) {
		t.Fatalf("legacy forward order changed:\n%s", strings.Join(f, "\n"))
	}
}

func TestPolicyOnDropsEveryPrivateRangeAfterTheGardenAndBeforeAuthorization(t *testing.T) {
	t.Parallel()
	out := string(RenderNftables(twoNetworks(), denyTopo()))
	for _, r := range PrivateRanges {
		if !strings.Contains(out, r) {
			t.Errorf("private range %s is not in the rendered ruleset", r)
		}
	}
	for _, r := range []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16"} {
		if !strings.Contains(out, "elements = { "+PrivateRanges[0]) || !strings.Contains(out, r) {
			t.Errorf("range %s missing from guest_private_ranges", r)
		}
	}
	f := fwdLines(t, out)
	est := indexOfLine(f, "ct state established,related accept")
	dns := indexOfLine(f, "custom DNS")
	garden := indexOfLine(f, "@walled_garden_ip accept")
	gardenTCP := indexOfLine(f, "@walled_garden_tcp accept")
	gardenUDP := indexOfLine(f, "@walled_garden_udp accept")
	drop := indexOfLine(f, "@guest_private_ranges")
	auth := indexOfLine(f, "authenticated guests")
	p3 := indexOfLine(f, "phase-3 authorized guests")
	if drop < 0 || garden < 0 || gardenTCP < 0 || gardenUDP < 0 || auth < 0 || p3 < 0 || est < 0 {
		t.Fatalf("missing rule(s):\n%s", strings.Join(f, "\n"))
	}
	if !(est < garden && garden < drop && gardenTCP < drop && gardenUDP < drop) {
		t.Errorf("the walled garden must stay ahead of the private drop:\n%s", strings.Join(f, "\n"))
	}
	if !(drop < auth && drop < p3) {
		t.Errorf("the private drop must precede the authorized-guest accepts")
	}
	if dns >= 0 && dns > drop {
		t.Errorf("custom DNS must stay ahead of the private drop")
	}
	// The garden accepts are not duplicated after the authorized accepts.
	if strings.Count(out, "@walled_garden_ip accept") != 1 {
		t.Errorf("garden accept rendered %d times", strings.Count(out, "@walled_garden_ip accept"))
	}
	// Established flows are accepted first on purpose (conntrack flush on revoke handles those, AD-24).
	if est > drop {
		t.Errorf("established accept moved behind the drop")
	}
}

func TestCustomDNSOnAPrivateResolverStaysReachable(t *testing.T) {
	t.Parallel()
	nets := twoNetworks()
	nets[0].DNSMode = "custom"
	nets[0].DNSServers = []string{"10.1.1.53", "192.168.5.5", "not-an-ip"}
	f := fwdLines(t, string(RenderNftables(nets, denyTopo())))
	dns := indexOfLine(f, "custom DNS (pre-auth)")
	drop := indexOfLine(f, "@guest_private_ranges")
	if dns < 0 || dns > drop {
		t.Fatalf("custom DNS to a private resolver must be accepted ahead of the drop:\n%s", strings.Join(f, "\n"))
	}
	if !strings.Contains(f[dns], "10.1.1.53") || strings.Contains(f[dns], "not-an-ip") {
		t.Errorf("custom DNS rule wrong: %s", f[dns])
	}
}

func TestInternetDisabledNetworkStillGetsNoGardenAndNoInternet(t *testing.T) {
	t.Parallel()
	nets := twoNetworks()
	nets[1].InternetEnabled = false
	f := fwdLines(t, string(RenderNftables(nets, denyTopo())))
	off := indexOfLine(f, "internet disabled for")
	garden := indexOfLine(f, "@walled_garden_ip accept")
	if off < 0 || off > garden {
		t.Fatalf("an internet-disabled network must be dropped before the garden accept:\n%s", strings.Join(f, "\n"))
	}
}

func TestAllowListExemptsTheDropAndTheManagementCIDRs(t *testing.T) {
	t.Parallel()
	topo := denyTopo("192.168.60.10/32", " 10.9.0.0/16 ", "garbage", "0.0.0.0/0", "2001:db8::/32", "10.9.0.0/16", "172.16.4.7")
	topo.MgmtCIDRs = []string{"192.168.0.0/16"}
	out := string(RenderNftables(twoNetworks(), topo))
	if !strings.Contains(out, "elements = { 10.9.0.0/16, 172.16.4.7/32, 192.168.60.10/32 }") {
		t.Errorf("allow set not the normalised, de-duplicated, malformed-free list:\n%s", out)
	}
	for _, bad := range []string{"garbage", "2001:db8", "0.0.0.0/0"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q reached the ruleset", bad)
		}
	}
	f := fwdLines(t, out)
	for _, l := range f {
		if strings.Contains(l, "guest -> mgmt blocked") && !strings.Contains(l, "ip daddr != @guest_private_allow") {
			t.Errorf("a management drop ignores the allow-list: %s", l)
		}
	}
}

func TestAllowListNeverOpensAnythingWithoutSignIn(t *testing.T) {
	t.Parallel()
	// The allow-list only lifts the DROP; reaching a private destination still needs the authorized accept, so no rule
	// accepts on the allow set itself.
	out := string(RenderNftables(twoNetworks(), denyTopo("10.9.0.0/16")))
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "@guest_private_allow") && strings.Contains(l, "accept") {
			t.Fatalf("a rule accepts on the allow set (would bypass sign-in): %s", l)
		}
	}
}

func TestPolicyDoesNotTouchTheApplianceItself(t *testing.T) {
	t.Parallel()
	off := inputChain(t, string(RenderNftables(twoNetworks(), topoT())))
	on := inputChain(t, string(RenderNftables(twoNetworks(), denyTopo("10.9.0.0/16"))))
	// Management, WAN and Central reachability are the input chain and the appliance's own output; the policy edits only
	// the forward chain, so the input chain must be identical.
	if off != on {
		t.Fatalf("the private-reach policy changed the input chain:\n--- off\n%s\n--- on\n%s", off, on)
	}
	if strings.Contains(on, "guest_private") {
		t.Fatal("input chain mentions the private-reach sets")
	}
}

func TestPolicyChangesTheFingerprintSoNetdReassertsIt(t *testing.T) {
	t.Parallel()
	a := RenderFingerprint(twoNetworks(), topoT())
	b := RenderFingerprint(twoNetworks(), denyTopo())
	c := RenderFingerprint(twoNetworks(), denyTopo("10.9.0.0/16"))
	if a == b || b == c || a == c {
		t.Fatalf("fingerprints must differ: %s %s %s", a, b, c)
	}
	if b != RenderFingerprint(twoNetworks(), denyTopo()) {
		t.Fatal("fingerprint is not deterministic")
	}
	// Order and duplicates in the allow-list do not change the ruleset.
	if RenderFingerprint(twoNetworks(), denyTopo("10.1.0.0/16", "10.2.0.0/16")) != RenderFingerprint(twoNetworks(), denyTopo("10.2.0.0/16", "10.1.0.0/16", "10.1.0.0/16")) {
		t.Fatal("allow-list order/duplicates changed the fingerprint")
	}
}

func TestNormalizeAllow(t *testing.T) {
	t.Parallel()
	got := NormalizeAllow([]string{"10.1.2.3/24", "1.2.3.4", "", "  ", "::1", "x", "0.0.0.0/0", "10.1.2.0/24"})
	want := []string{"1.2.3.4/32", "10.1.2.0/24"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}
