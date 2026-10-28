package sysnet

import (
	"strings"
	"testing"
)

func goodWAN() WANConfig {
	return WANConfig{Interface: "ens160", Mode: "static", IP: "192.0.2.23", PrefixLen: 24, Gateway: "192.0.2.1", DNS: []string{"1.1.1.1", "8.8.8.8"}}
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
	l.IP = "192.0.2.50" // same subnet as WAN
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
	if got := ManagementURL(goodWAN()); got != "https://192.0.2.23" {
		t.Fatalf("management URL = %q", got)
	}
}

func TestRenderWANNetplanHasNoLegacyAddr(t *testing.T) {
	out := RenderWANNetplan(goodWAN())
	if !containsAll(out, "192.0.2.23/24", "via: 192.0.2.1", "1.1.1.1") {
		t.Fatalf("WAN netplan missing expected fields:\n%s", out)
	}
}

func TestRenderLANNetplanIPOnBridge(t *testing.T) {
	out := RenderLANNetplan(goodLAN())
	// the address must appear under the bridge stanza, member enslaved
	if !containsAll(out, `"br-lan":`, `interfaces: ["ens192"]`, "10.10.0.1/24") {
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

// Request-supplied interface names are refused unless they are plain Linux interface names, and the renderer writes
// them as quoted scalars, so a newline cannot add netplan keys (audit Phase 8 M9).
func TestHostileInterfaceNamesAreRefused(t *testing.T) {
	hostile := []string{"ens160\n      addresses: [203.0.113.9/24]", "ens\"160", "-ens160", "ens160:", "a234567890123456", "ens 160", "ens160#x"}
	for _, name := range hostile {
		w := goodWAN()
		w.Interface = name
		if r := ValidateFull(w, goodLAN()); r.OK || !hasCode(r, "invalid_interface_name") {
			t.Errorf("WAN interface %q accepted: %+v", name, r.Issues)
		}
		l := goodLAN()
		l.PhysicalInterface = name
		if r := ValidateFull(goodWAN(), l); r.OK || !hasCode(r, "invalid_interface_name") {
			t.Errorf("LAN interface %q accepted: %+v", name, r.Issues)
		}
		l = goodLAN()
		l.Bridge = name
		if r := ValidateFull(goodWAN(), l); r.OK || !hasCode(r, "invalid_interface_name") {
			t.Errorf("LAN bridge %q accepted: %+v", name, r.Issues)
		}
	}
	for _, name := range []string{"ens160", "eth0.100", "br-lan", "enp3s0f1", "wan_1"} {
		w := goodWAN()
		w.Interface = name
		if r := ValidateFull(w, goodLAN()); hasCode(r, "invalid_interface_name") {
			t.Errorf("plain interface name %q refused: %+v", name, r.Issues)
		}
	}
}

// Audit Phase 8 L14: an unknown mode is not silently treated as static, and addresses that can never be an interface
// address or a gateway are refused.
func TestModeAndAddressClassesAreValidated(t *testing.T) {
	for _, mode := range []string{"", "Static", "pppoe"} {
		w := goodWAN()
		w.Mode = mode
		if r := ValidateFull(w, goodLAN()); r.OK || !hasCode(r, "invalid_mode") {
			t.Errorf("mode %q accepted: %+v", mode, r.Issues)
		}
	}
	w := goodWAN()
	w.Mode = "dhcp"
	if r := ValidateFull(w, goodLAN()); !r.OK {
		t.Errorf("dhcp refused: %+v", r.Issues)
	}
	for _, ip := range []string{"127.0.0.5", "224.0.0.9", "169.254.10.10", "0.0.0.0"} {
		w := goodWAN()
		w.IP, w.Gateway, w.PrefixLen = ip, ip, 8
		if r := ValidateFull(w, goodLAN()); r.OK || !hasCode(r, "unusable_address") {
			t.Errorf("WAN address %s accepted: %+v", ip, r.Issues)
		}
		l := goodLAN()
		l.IP, l.DHCPEnabled = ip, false
		if r := ValidateFull(goodWAN(), l); r.OK || !hasCode(r, "unusable_address") {
			t.Errorf("LAN address %s accepted: %+v", ip, r.Issues)
		}
	}
}

func TestRenderedInterfaceNamesStayOneScalar(t *testing.T) {
	w := goodWAN()
	w.Interface = "ens160:\n      addresses: [203.0.113.9/24]"
	out := RenderWANNetplan(w)
	if strings.Count(out, "\n") != strings.Count(RenderWANNetplan(goodWAN()), "\n") {
		t.Fatalf("an interface name added YAML keys:\n%s", out)
	}
	l := goodLAN()
	l.Bridge = "br-lan\"\n    x:"
	out = RenderLANNetplan(l)
	if strings.Count(out, "\n") != strings.Count(RenderLANNetplan(goodLAN()), "\n") {
		t.Fatalf("a bridge name added YAML keys:\n%s", out)
	}
}
