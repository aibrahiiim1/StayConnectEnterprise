package sysnet

import (
	"fmt"
	"testing"
)

// The LAN base network is a client network: /19 at the widest, /30 at the narrowest, for every prefix from /1 to /32.
func TestLANPrefixFollowsTheClientNetworkRule(t *testing.T) {
	for p := 1; p <= 32; p++ {
		l := goodLAN()
		l.PrefixLen = p
		l.IP = "10.32.0.1"
		l.DHCPEnabled = false
		got := hasCode(ValidateFull(goodWAN(), l), "invalid_prefix")
		if want := p < 19 || p > 30; got != want {
			t.Errorf("/%d: invalid_prefix = %v, want %v", p, got, want)
		}
	}
}

// A wide LAN that contains the WAN/management subnet (or is inside it) is refused, not only an identical subnet.
func TestLANThatContainsOrIsInsideTheWANSubnetIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		wanIP  string
		wanLen int
		lanIP  string
		lanLen int
	}{
		"/19 LAN over a /24 WAN":    {"172.21.96.97", 24, "172.21.64.1", 19},
		"/19 LAN containing WAN":    {"172.21.96.97", 24, "172.21.96.1", 19},
		"/24 WAN inside /19 LAN":    {"10.40.20.5", 24, "10.40.0.1", 19},
		"/19 WAN holding a /24 LAN": {"10.40.0.9", 19, "10.40.20.1", 24},
	} {
		w := goodWAN()
		w.IP, w.PrefixLen, w.Gateway = tc.wanIP, tc.wanLen, ""
		l := goodLAN()
		l.IP, l.PrefixLen, l.DHCPEnabled = tc.lanIP, tc.lanLen, false
		if name == "/19 LAN over a /24 WAN" {
			// 172.21.64.0/19 = 172.21.64.0 - 172.21.95.255: adjacent to 172.21.96.0/24, so this one must be ACCEPTED
			if hasCode(ValidateFull(w, l), "subnet_overlap") {
				t.Errorf("%s: adjacent subnets reported as overlapping", name)
			}
			continue
		}
		if !hasCode(ValidateFull(w, l), "subnet_overlap") {
			t.Errorf("%s: overlap not reported", name)
		}
	}
	l := goodLAN()
	l.IP, l.PrefixLen, l.DHCPEnabled = "10.32.0.1", 19, false
	if r := ValidateFull(goodWAN(), l); !r.OK {
		t.Errorf("a /19 LAN beside the WAN was refused: %+v", r.Issues)
	}
	_ = fmt.Sprint()
}
