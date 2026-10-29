package netcfg

import (
	"strings"
	"testing"
)

// DNS abuse limits: the resolver bounds every client and zone, and the firewall holds a device that has not signed
// in to 20 queries a second (gateway and public resolvers alike) with a counter.
func TestDNSIsRateLimitedAtTheResolverAndForDevicesThatHaveNotSignedIn(t *testing.T) {
	u := string(RenderUnbound(twoNetworks()))
	for _, want := range []string{"ip-ratelimit: 100", "ratelimit: 1000", "deny-any: yes"} {
		if !strings.Contains(u, want) {
			t.Errorf("unbound fragment lacks %q", want)
		}
	}
	n := string(RenderNftables(twoNetworks(), topoT()))
	if strings.Count(n, `update @dns_preauth_rl { ip saddr limit rate over 20/second burst 40 packets } counter name "dns_preauth_limited" drop`) != 2 {
		t.Error("the pre-auth DNS limit must apply on input (gateway) and forward (public resolvers)")
	}
	in := n[strings.Index(n, "chain input"):strings.Index(n, "chain forward")]
	if !strings.Contains(in, "iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 update @dns_preauth_rl") {
		t.Error("an authenticated device must not be held to the pre-auth DNS limit")
	}
	if strings.Index(in, "dns_preauth_rl") > strings.Index(in, `udp dport 53 accept comment "DNS (own gateway)"`) {
		t.Error("the limit must precede the DNS accept")
	}
	// ...and the established accept: a UDP socket is established after the first reply, so a limiter behind that accept
	// would meter only the first queries.
	if strings.Index(in, "dns_preauth_rl") > strings.Index(in, "ct state established,related accept") {
		t.Error("the input limiter must precede the established accept")
	}
	fwd := n[strings.Index(n, "chain forward"):strings.Index(n, "chain prerouting_nat")]
	if i := strings.Index(fwd, "dns_preauth_rl"); i < 0 || i > strings.Index(fwd, "ct state established,related accept") {
		t.Error("the forward limiter must precede the established accept")
	}
	if !strings.Contains(fwd, "iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 update @dns_preauth_rl") {
		t.Error("authenticated devices must be exempt in forward too")
	}
}

// Nothing grants pre-authentication internet: the temporary verification access is gone from the ruleset, and the
// forward chain's WAN accepts are exactly the two authorization sets and the walled garden.
func TestNoTemporaryVerificationAccessRemainsInTheRuleset(t *testing.T) {
	topo := topoT()
	topo.MgmtCIDRs = []string{"172.21.60.0/24"}
	for _, vip := range []string{"", "10.255.255.1"} {
		topo.PortalVIP = vip
		n := string(RenderNftables(twoNetworks(), topo))
		for _, gone := range []string{"verify_", "0x56455249", "quota", "limit name", "ct mark", "temporary verification"} {
			if strings.Contains(n, gone) {
				t.Errorf("vip=%q: the ruleset still carries %q", vip, gone)
			}
		}
		fwd := n[strings.Index(n, "chain forward"):strings.Index(n, "chain prerouting_nat")]
		allowed := []string{
			"ct state established,related accept",
			`oifname "ens160" iifname . ip saddr @phase3_auth_ipv4 accept comment "phase-3 authorized guests"`,
			`iifname @guest_interfaces oifname "ens160" ip daddr @walled_garden_ip accept`,
			`iifname @guest_interfaces oifname "ens160" ip daddr . tcp dport @walled_garden_tcp accept`,
			`iifname @guest_interfaces oifname "ens160" ip daddr . udp dport @walled_garden_udp accept`,
		}
		accepts := 0
		for _, line := range strings.Split(fwd, "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, "accept") {
				continue
			}
			accepts++
			ok := false
			for _, a := range allowed {
				ok = ok || line == a
			}
			if !ok {
				t.Errorf("vip=%q: a forward accept outside the authorization sets and the garden: %s", vip, line)
			}
		}
		if accepts != len(allowed) {
			t.Errorf("vip=%q: %d forward accepts, want exactly %d", vip, accepts, len(allowed))
		}
		// Every gateway captive redirect exempts exactly the two authorization sets and the address-only garden.
		nat := n[strings.Index(n, "chain prerouting_nat"):strings.Index(n, "chain postrouting_nat")]
		redirects := 0
		for _, line := range strings.Split(nat, "\n") {
			if !strings.Contains(line, "dnat ip to") || !strings.Contains(line, "!= @") {
				continue
			}
			redirects++
			if !strings.Contains(line, "iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport") ||
				strings.Count(line, "!= @") != 3 {
				t.Errorf("vip=%q: a captive redirect with an unexpected exemption: %s", vip, strings.TrimSpace(line))
			}
		}
		if redirects != len(twoNetworks()) {
			t.Errorf("vip=%q: %d gateway captive redirects, want one per network", vip, redirects)
		}
		est := strings.Index(fwd, "ct state established,related accept")
		if i := strings.Index(fwd, `oifname @guest_interfaces meta nfproto ipv6`); i < 0 || i > est {
			t.Errorf("vip=%q: the IPv6 return-path guard must precede the established accept", vip)
		}
	}
}
