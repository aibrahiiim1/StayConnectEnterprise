package sysnet

import "testing"

func goodWAN() WANConfig {
	return WANConfig{Interface: "ens160", Mode: "static", IP: "172.21.60.23", PrefixLen: 24, Gateway: "172.21.60.1", DNS: []string{"1.1.1.1", "8.8.8.8"}}
}
func goodLAN() LANConfig {
	return LANConfig{PhysicalInterface: "ens192", Bridge: "br-lan", IP: "10.10.0.1", PrefixLen: 24, DHCPEnabled: true, DHCPStart: "10.10.0.100", DHCPEnd: "10.10.0.250", DHCPLeaseSeconds: 3600, DNS: []string{"10.10.0.1"}}
}

func hasCode(r ValidationResult, code string) bool {
	for _, i := range r.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestValidBaselinePasses(t *testing.T) {
	r := ValidateFull(goodWAN(), goodLAN())
	if !r.OK {
		t.Fatalf("expected valid, got issues: %+v", r.Issues)
	}
}

func TestRule1_SubnetOverlap(t *testing.T) {
	l := goodLAN()
	l.IP = "172.21.60.50" // same subnet as WAN
	l.PrefixLen = 24
	l.DHCPEnabled = false
	if r := ValidateFull(goodWAN(), l); !hasCode(r, "subnet_overlap") {
		t.Fatalf("expected subnet_overlap, got %+v", r.Issues)
	}
}

func TestRule4_GatewayOffSubnet(t *testing.T) {
	w := goodWAN()
	w.Gateway = "10.9.9.1"
	if r := ValidateFull(w, goodLAN()); !hasCode(r, "gateway_off_subnet") {
		t.Fatalf("expected gateway_off_subnet, got %+v", r.Issues)
	}
}

func TestRule5_DHCPOutsideSubnet(t *testing.T) {
	l := goodLAN()
	l.DHCPEnd = "10.20.0.250"
	if r := ValidateFull(goodWAN(), l); !hasCode(r, "out_of_subnet") {
		t.Fatalf("expected out_of_subnet, got %+v", r.Issues)
	}
}

func TestRule6_DHCPContainsGateway(t *testing.T) {
	l := goodLAN()
	l.DHCPStart = "10.10.0.1" // includes the gateway
	if r := ValidateFull(goodWAN(), l); !hasCode(r, "pool_contains_gateway") {
		t.Fatalf("expected pool_contains_gateway, got %+v", r.Issues)
	}
}

func TestRule6_DHCPContainsBroadcast(t *testing.T) {
	l := goodLAN()
	l.DHCPEnd = "10.10.0.255" // broadcast
	if r := ValidateFull(goodWAN(), l); !hasCode(r, "pool_contains_broadcast") {
		t.Fatalf("expected pool_contains_broadcast, got %+v", r.Issues)
	}
}

func TestRule7_DuplicateWANLANIP(t *testing.T) {
	w := goodWAN()
	w.IP = "10.10.0.1"
	w.PrefixLen = 24
	w.Gateway = "10.10.0.254"
	if r := ValidateFull(w, goodLAN()); !hasCode(r, "duplicate_ip") && !hasCode(r, "subnet_overlap") {
		t.Fatalf("expected duplicate_ip/subnet_overlap, got %+v", r.Issues)
	}
}

func TestRule9_InvalidPrefix(t *testing.T) {
	w := goodWAN()
	w.PrefixLen = 33
	if r := ValidateFull(w, goodLAN()); !hasCode(r, "invalid_prefix") {
		t.Fatalf("expected invalid_prefix, got %+v", r.Issues)
	}
}

func TestRule10_InvalidDNS(t *testing.T) {
	w := goodWAN()
	w.DNS = []string{"not-an-ip"}
	if r := ValidateFull(w, goodLAN()); !hasCode(r, "invalid_dns") {
		t.Fatalf("expected invalid_dns, got %+v", r.Issues)
	}
}

func TestRule11_SameInterfaceWANandLAN(t *testing.T) {
	l := goodLAN()
	l.PhysicalInterface = "ens160" // same as WAN
	if r := ValidateFull(goodWAN(), l); !hasCode(r, "iface_conflict") {
		t.Fatalf("expected iface_conflict, got %+v", r.Issues)
	}
}

func TestRule13_NoManagementIP(t *testing.T) {
	w := goodWAN()
	w.IP = ""
	if r := ValidateFull(w, goodLAN()); !hasCode(r, "no_management_ip") {
		t.Fatalf("expected no_management_ip, got %+v", r.Issues)
	}
}

func TestLANIPCannotBeBroadcast(t *testing.T) {
	l := goodLAN()
	l.IP = "10.10.0.255"
	l.DHCPEnabled = false
	if r := ValidateFull(goodWAN(), l); !hasCode(r, "broadcast_address") {
		t.Fatalf("expected broadcast_address, got %+v", r.Issues)
	}
}

func TestManagementURL(t *testing.T) {
	if got := ManagementURL(goodWAN()); got != "https://172.21.60.23" {
		t.Fatalf("management URL = %q", got)
	}
}

func TestRenderWANNetplanHasNoLegacyAddr(t *testing.T) {
	out := RenderWANNetplan(goodWAN())
	if !containsAll(out, "172.21.60.23/24", "via: 172.21.60.1", "1.1.1.1") {
		t.Fatalf("WAN netplan missing expected fields:\n%s", out)
	}
}

func TestRenderLANNetplanIPOnBridge(t *testing.T) {
	out := RenderLANNetplan(goodLAN())
	// the address must appear under the bridge stanza, member enslaved
	if !containsAll(out, "br-lan:", "interfaces: [ens192]", "10.10.0.1/24") {
		t.Fatalf("LAN netplan wrong:\n%s", out)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
