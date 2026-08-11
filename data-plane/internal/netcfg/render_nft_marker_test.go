package netcfg

import (
	"strings"
	"testing"
)

func markerTestNets() []GuestNetwork {
	return []GuestNetwork{{
		Name: "guest90", Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: 90,
		BridgeName: "br-g90", GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22,
		DHCPMode: "managed", CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
	}}
}

func markerTestTopo() Topology {
	return Topology{WANInterface: "ens160", MgmtInterface: "ens160", MgmtAddr: "172.21.60.23"}
}

// The render must carry its own fingerprint into the kernel, and that fingerprint must be the one
// RenderFingerprint reports — otherwise reconciliation would compare two different things and either never
// settle or never converge.
func TestRender_MarkerCarriesTheReportedFingerprint(t *testing.T) {
	out := string(RenderNftables(markerTestNets(), markerTestTopo()))
	want := RenderFingerprint(markerTestNets(), markerTestTopo())

	if !strings.Contains(out, "set "+RenderMarkerSet+" {") {
		t.Fatal("the render carries no marker set")
	}
	var comment string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, renderMarkerPrefix) {
			comment = line
		}
	}
	if got := FingerprintFromSetComment(comment); got != want {
		t.Fatalf("marker carries %q, RenderFingerprint reports %q", got, want)
	}
	if len(want) != 32 {
		t.Fatalf("fingerprint %q is not 32 hex characters", want)
	}
}

// The marker sits INSIDE the table, so a `delete table` + recreate replaces it atomically with the rest of the
// structure. A marker outside the table would survive a replace and could claim a structure that is gone.
func TestRender_MarkerIsInsideTheTable(t *testing.T) {
	out := string(RenderNftables(markerTestNets(), markerTestTopo()))
	open := strings.LastIndex(out, "table inet stayconnect {\n")
	marker := strings.Index(out, "set "+RenderMarkerSet)
	if open < 0 || marker < 0 || marker < open {
		t.Fatal("the marker is not inside the generated table")
	}
	if !strings.Contains(out, "delete table inet stayconnect") {
		t.Fatal("the render no longer replaces the table atomically")
	}
}

// The fingerprint describes the STRUCTURE, not itself. If the marker were included in its own input the value
// could never be reproduced by a reader.
func TestRender_FingerprintExcludesTheMarker(t *testing.T) {
	fp := RenderFingerprint(markerTestNets(), markerTestTopo())
	if strings.Contains(renderNftBody(markerTestNets(), markerTestTopo()), RenderMarkerSet) {
		t.Fatal("the fingerprinted body contains the marker; the value is circular")
	}
	if RenderFingerprint(markerTestNets(), markerTestTopo()) != fp {
		t.Fatal("the fingerprint is not stable across calls")
	}
}

// Every structural input must move the fingerprint, or an upgrade that changes the ruleset would be invisible
// to reconciliation and silently skipped.
func TestRender_FingerprintMovesWithStructure(t *testing.T) {
	base := RenderFingerprint(markerTestNets(), markerTestTopo())

	cases := []struct {
		name string
		mut  func(nets []GuestNetwork, topo *Topology) []GuestNetwork
	}{
		{"subnet", func(n []GuestNetwork, _ *Topology) []GuestNetwork { n[0].SubnetCIDR = "10.30.0.0/22"; return n }},
		{"bridge", func(n []GuestNetwork, _ *Topology) []GuestNetwork { n[0].BridgeName = "br-g91"; return n }},
		{"gateway", func(n []GuestNetwork, _ *Topology) []GuestNetwork { n[0].GatewayIP = "10.20.0.254"; return n }},
		{"captive off", func(n []GuestNetwork, _ *Topology) []GuestNetwork { n[0].CaptiveEnabled = false; return n }},
		{"nat off", func(n []GuestNetwork, _ *Topology) []GuestNetwork { n[0].NATEnabled = false; return n }},
		{"disabled", func(n []GuestNetwork, _ *Topology) []GuestNetwork { n[0].Enabled = false; return n }},
		{"wan iface", func(n []GuestNetwork, tp *Topology) []GuestNetwork { tp.WANInterface = "eth9"; return n }},
		{"second network", func(n []GuestNetwork, _ *Topology) []GuestNetwork {
			extra := n[0]
			extra.Name, extra.BridgeName, extra.GatewayIP, extra.SubnetCIDR = "g91", "br-g91", "10.24.0.1", "10.24.0.0/22"
			return append(n, extra)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := markerTestTopo()
			nets := tc.mut(markerTestNets(), &topo)
			if RenderFingerprint(nets, topo) == base {
				t.Fatalf("changing %s did not change the fingerprint", tc.name)
			}
		})
	}
}

// A comment that is not a well-formed marker must yield "", which compares unequal to every fingerprint. The
// failure mode this prevents is a truncated or hand-edited comment being read as a match and skipping a needed
// convergence.
func TestRender_MalformedMarkerCommentsYieldNothing(t *testing.T) {
	good := RenderFingerprint(markerTestNets(), markerTestTopo())
	if FingerprintFromSetComment(`comment "`+renderMarkerPrefix+good+`"`) != good {
		t.Fatal("a well-formed marker comment was not read")
	}
	for _, bad := range []string{
		"",
		"comment \"\"",
		"comment \"some other set\"",
		`comment "` + renderMarkerPrefix + `"`, // empty value
		`comment "` + renderMarkerPrefix + good[:31] + `"`,               // one character short
		`comment "` + renderMarkerPrefix + good + "aa" + `"`,             // too long
		`comment "` + renderMarkerPrefix + strings.ToUpper(good) + `"`,   // uppercase hex
		`comment "` + renderMarkerPrefix + strings.Repeat("z", 32) + `"`, // non-hex
		`comment "netd-render-fp"`,                                       // no separator
	} {
		if got := FingerprintFromSetComment(bad); got != "" {
			t.Fatalf("%q was read as fingerprint %q", bad, got)
		}
	}
}

// The Phase-3 set is part of the rendered structure — that is what makes its presence durable across restarts
// rather than dependent on a one-off operator install.
func TestRender_Phase3SetIsPartOfTheRenderedStructure(t *testing.T) {
	out := string(RenderNftables(markerTestNets(), markerTestTopo()))
	for _, want := range []string{
		"set phase3_auth_ipv4 {",
		"ip saddr @phase3_auth_ipv4 accept",
		"ip saddr != @phase3_auth_ipv4",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the render is missing %q", want)
		}
	}
	if strings.Contains(out, "elements = { ") && strings.Contains(out, "phase3_auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t\tflags timeout\n\t\telements") {
		t.Fatal("phase3_auth_ipv4 is rendered with elements; it must start empty")
	}
}
