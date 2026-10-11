package netcfg

import (
	"strings"
	"testing"
)

// DNS ABUSE LIMITS: the resolver bounds every client and every zone, and the firewall holds a device that has
// not signed in to 20 queries a second -- to the gateway and to the public resolvers alike -- with a counter.
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
}

// TEMPORARY VERIFICATION ACCESS: metered before the established accept, admitted to the WAN only after the
// management and client-to-client drops, and never captive-redirected while admitted.
func TestVerificationAccessIsMeteredFirstAndAdmittedOnlyToTheInternet(t *testing.T) {
	topo := topoT()
	topo.MgmtCIDRs = []string{"172.21.60.0/24"}
	n := string(RenderNftables(twoNetworks(), topo))
	fwd := n[strings.Index(n, "chain forward"):strings.Index(n, "chain prerouting_nat")]
	est := strings.Index(fwd, "ct state established,related accept")
	for _, meter := range []string{"quota name ip daddr map @verify_quota drop", "limit name ip daddr map @verify_down drop",
		"quota name ip saddr map @verify_quota drop", "limit name ip saddr map @verify_up drop"} {
		if i := strings.Index(fwd, meter); i < 0 || i > est {
			t.Errorf("%q must precede the established accept", meter)
		}
	}
	admit := strings.Index(fwd, "iifname . ip saddr @verify_ipv4 ct mark set 0x56455249 accept")
	if admit < 0 || !strings.Contains(fwd[admit-80:admit], `oifname "ens160"`) {
		t.Fatal("verification access must be admitted to the WAN only")
	}
	for _, guard := range []string{`guest -> mgmt blocked`, `inter-guest isolation`} {
		if g := strings.Index(fwd, guard); g < 0 || g > admit {
			t.Errorf("%q must precede the verification admit", guard)
		}
	}
	// A closed window's flows end with it: the marked-flow drops precede the established accept.
	for _, d := range []string{"ct mark 0x56455249 iifname @guest_interfaces ip saddr != @verify_quota drop", "ct mark 0x56455249 oifname @guest_interfaces ip daddr != @verify_quota drop"} {
		if i := strings.Index(fwd, d); i < 0 || i > est {
			t.Errorf("%q must precede the established accept", d)
		}
	}
	nat := n[strings.Index(n, "chain prerouting_nat"):]
	if strings.Count(nat, "iifname . ip saddr != @verify_ipv4") != strings.Count(nat, "dnat ip to") {
		t.Error("every captive redirect must exclude an admitted device")
	}
	if i, j := strings.Index(fwd, `oifname @guest_interfaces meta nfproto ipv6`), est; i < 0 || i > j {
		t.Error("the IPv6 return-path guard must precede the established accept")
	}
}
