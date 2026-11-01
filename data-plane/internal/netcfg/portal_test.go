package netcfg

import (
	"strings"
	"testing"
)

func codes(issues []Issue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Code)
	}
	return out
}

func hasPortalCode(issues []Issue, code string) bool {
	for _, i := range issues {
		if i.Code == code && i.Field == "portal_ip" {
			return true
		}
	}
	return false
}

// A typical appliance: WAN/management on 192.168.60.0/24, Docker's default bridge, two guest bridges.
func liveHost() []HostAddr {
	return []HostAddr{
		{Iface: "ens160", CIDR: "192.168.60.25/24"},
		{Iface: "docker0", CIDR: "172.17.0.1/16"},
		{Iface: "br-3f2a9c1d7e44", CIDR: "172.18.0.1/16"},
		{Iface: "br-g221", CIDR: "10.221.0.1/22"},
		{Iface: "br-g222", CIDR: "10.222.0.1/24"},
		{Iface: "lo", CIDR: "127.0.0.1/8"},
	}
}

func TestValidatePortalIP(t *testing.T) {
	nets := vipNets()
	disabled := GuestNetwork{Name: "Spare", SubnetCIDR: "10.255.0.0/16", Enabled: false}

	cases := []struct {
		name string
		vip  string
		nets []GuestNetwork
		host []HostAddr
		want string // "" = valid
	}{
		{"the migration default is valid", "10.255.255.1", nets, liveHost(), ""},
		{"a /32 written as CIDR is valid", "10.254.254.1/32", nets, liveHost(), ""},
		{"none configured is valid (HTTP fallback only)", "", nets, liveHost(), ""},
		{"inside a guest subnet", "10.221.1.7", nets, liveHost(), "portal_ip_overlap"},
		{"equal to a guest gateway", "10.222.0.1", nets, liveHost(), "portal_ip_overlap"},
		{"inside a DISABLED guest subnet", "10.255.255.1", append(append([]GuestNetwork{}, nets...), disabled), liveHost(), "portal_ip_overlap"},
		{"inside the WAN/management subnet", "192.168.60.200", nets, liveHost(), "portal_ip_overlap"},
		{"inside the Docker default bridge 172.17.0.1/16", "172.17.5.5", nets, liveHost(), "portal_ip_overlap"},
		{"inside a compose bridge", "172.18.200.1", nets, liveHost(), "portal_ip_overlap"},
		{"a public address", "8.8.8.8", nets, liveHost(), "portal_ip_not_private"},
		{"the old documentation address", "192.0.2.1", nets, liveHost(), "portal_ip_not_private"},
		{"CGNAT is not RFC 1918", "100.64.0.1", nets, liveHost(), "portal_ip_not_private"},
		{"IPv6", "fd00::1", nets, liveHost(), "portal_ip_invalid"},
		{"a wider prefix", "10.255.255.1/24", nets, liveHost(), "portal_ip_invalid"},
		{"garbage", "portal", nets, liveHost(), "portal_ip_invalid"},
		{"a .0", "10.255.255.0", nets, liveHost(), "portal_ip_invalid"},
		{"a .255", "10.255.255.255", nets, liveHost(), "portal_ip_invalid"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			got := ValidatePortalIP(tc.vip, tc.nets, tc.host)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want valid, got %v", got)
				}
				return
			}
			if !hasPortalCode(got, tc.want) {
				t.Fatalf("want %s on portal_ip, got %v", tc.want, codes(got))
			}
		})
	}
}

func TestValidatePortalIP_TheAddressOnItsOwnInterfaceIsNotAnOverlap(t *testing.T) {
	// Re-validating the same address after the first apply (the portal interface carries it) must not find it overlapping itself.
	host := append(liveHost(), HostAddr{Iface: PortalIface, CIDR: "10.255.255.1/32"})
	if got := ValidatePortalIP("10.255.255.1", vipNets(), host); len(got) != 0 {
		t.Fatalf("the portal address overlapped itself: %v", got)
	}
	host = append(liveHost(), HostAddr{Iface: "ens192", CIDR: "10.255.255.1/32"})
	if got := ValidatePortalIP("10.255.255.1", vipNets(), host); !hasPortalCode(got, "portal_ip_overlap") {
		t.Fatalf("an address another interface carries was accepted: %v", got)
	}
}

func TestValidateSetPassesThePortalStateToOption114Rule(t *testing.T) {
	n := vlan20()
	topo := topoT()
	if res := ValidateSet([]GuestNetwork{n}, topo, nil, nil); hasAny(res.Issues, "portal_https") {
		t.Fatalf("the HTTP fallback was refused: %v", res.Issues)
	}
	topo.PortalVIP = "10.255.255.1"
	topo.PortalTLS = PortalTLS{HTTPSActive: true, CanonicalHostname: "k3j9x0ab.gw.onegate.getsemantics.com"}
	if res := ValidateSet([]GuestNetwork{n}, topo, nil, nil); hasAny(res.Issues, "portal_https") {
		t.Fatalf("the canonical HTTPS form was refused: %v", res.Issues)
	}
}

func hasAny(issues []Issue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestCaptivePortalURL(t *testing.T) {
	base := topoT()
	base.PortalHTTPPort = 8380
	// On the HTTP fallback the option is left out: a plain-HTTP value silenced the phones' own probes (2026-11-01 capture).
	httpURL := ""
	active := PortalTLS{HTTPSActive: true, CanonicalHostname: "k3j9x0ab.gw.onegate.getsemantics.com"}

	cases := []struct {
		name string
		vip  string
		tls  PortalTLS
		want string
	}{
		{"fallback carries no option 114", "10.255.255.1", PortalTLS{}, httpURL},
		{"HTTPS active -> the captive API on the canonical name", "10.255.255.1", active,
			"https://k3j9x0ab.gw.onegate.getsemantics.com/captive-portal/api"},
		{"the hotel's domain when it is canonical", "10.255.255.1",
			PortalTLS{HTTPSActive: true, CanonicalHostname: "wifi.nilehotel.com"}, "https://wifi.nilehotel.com/captive-portal/api"},
		{"active but no canonical name", "10.255.255.1", PortalTLS{HTTPSActive: true}, httpURL},
		{"a canonical name that is not a hostname", "10.255.255.1",
			PortalTLS{HTTPSActive: true, CanonicalHostname: "evil.example/\"x"}, httpURL},
		{"a canonical name with a port", "10.255.255.1",
			PortalTLS{HTTPSActive: true, CanonicalHostname: "a.gw.onegate.getsemantics.com:444"}, httpURL},
		{"canonical but HTTPS not active", "10.255.255.1",
			PortalTLS{CanonicalHostname: "k3j9x0ab.gw.onegate.getsemantics.com"}, httpURL},
		{"HTTPS active but no portal address on this render", "", active, httpURL},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			topo := base
			topo.PortalVIP, topo.PortalTLS = tc.vip, tc.tls
			if got := CaptivePortalURL("10.20.0.1", topo); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func option114(t *testing.T, dhcp4 map[string]any) string {
	t.Helper()
	subs := dhcp4["subnet4"].([]map[string]any)
	for _, o := range subs[0]["option-data"].([]map[string]any) {
		if o["code"] == 114 {
			return o["data"].(string)
		}
	}
	return ""
}

func TestRenderKea_Option114FollowsTheHTTPSState(t *testing.T) {
	topo := topoT()
	topo.PortalHTTPPort = 8380
	topo.PortalVIP = "10.255.255.1"

	fallback := RenderKeaDhcp4([]GuestNetwork{vlan20()}, topo, "/x", "/y")
	if got := option114(t, fallback); got != "" {
		t.Fatalf("fallback option 114 = %q; the HTTP fallback must not carry option 114", got)
	}
	topo.PortalTLS = PortalTLS{HTTPSActive: true, CanonicalHostname: "k3j9x0ab.gw.onegate.getsemantics.com"}
	active := RenderKeaDhcp4([]GuestNetwork{vlan20()}, topo, "/x", "/y")
	if got := option114(t, active); got != "https://k3j9x0ab.gw.onegate.getsemantics.com/captive-portal/api" {
		t.Fatalf("active option 114 = %q", got)
	}
	n := vlan20()
	n.CaptiveEnabled = false
	for _, o := range RenderKeaDhcp4([]GuestNetwork{n}, topo, "/x", "/y")["subnet4"].([]map[string]any)[0]["option-data"].([]map[string]any) {
		if o["code"] == 114 || o["name"] == "v4-captive-portal" {
			t.Fatal("a network with the captive portal disabled was given option 114")
		}
	}
}

func TestRenderUnboundPortal(t *testing.T) {
	header := "# StayConnect generated -- portal hostnames -> portal address. Do not edit by hand.\n"

	if got := string(RenderUnboundPortal("", PortalTLS{OneGateHostname: "k3j9x0ab.gw.onegate.getsemantics.com"})); got != header {
		t.Fatalf("no portal address must render the header only, got:\n%s", got)
	}
	if got := string(RenderUnboundPortal("10.255.255.1", PortalTLS{})); got != header {
		t.Fatalf("no hostnames must render the header only, got:\n%s", got)
	}

	got := string(RenderUnboundPortal("10.255.255.1", PortalTLS{
		OneGateHostname: "k3j9x0ab.gw.onegate.getsemantics.com", CustomDomain: "wifi.nilehotel.com"}))
	want := header + "server:\n" +
		"    local-zone: \"k3j9x0ab.gw.onegate.getsemantics.com.\" static\n" +
		"    local-data: \"k3j9x0ab.gw.onegate.getsemantics.com. IN A 10.255.255.1\"\n" +
		"    local-zone: \"wifi.nilehotel.com.\" static\n" +
		"    local-data: \"wifi.nilehotel.com. IN A 10.255.255.1\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// static + A only => AAAA is NODATA; there must never be an AAAA record.
	if strings.Contains(got, "AAAA") {
		t.Fatal("the portal names must have no AAAA")
	}

	evil := string(RenderUnboundPortal("10.255.255.1", PortalTLS{
		OneGateHostname: "x.gw.onegate.getsemantics.com\"\n    local-zone: \".\" refuse", CustomDomain: "WIFI.NileHotel.com."}))
	if strings.Contains(evil, "refuse") || strings.Contains(evil, "x.gw.onegate.getsemantics.com") {
		t.Fatalf("an unsafe hostname was rendered:\n%s", evil)
	}
	if !strings.Contains(evil, "\"wifi.nilehotel.com. IN A 10.255.255.1\"") {
		t.Fatalf("a valid name in other case / with a trailing dot should be normalised and rendered:\n%s", evil)
	}
}

func TestRenderUnboundPortal_IsSeparateFromTheGuestFragment(t *testing.T) {
	guest := string(RenderUnbound(vipNets()))
	if strings.Contains(guest, "onegate") || strings.Contains(guest, "10.255.255.1") {
		t.Fatal("the guest fragment carries portal names; they belong in the separate portal fragment")
	}
}
