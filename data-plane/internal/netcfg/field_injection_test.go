package netcfg

import (
	"strings"
	"testing"
)

// A network name is free text from an operator and reached the generated firewall file unescaped (audit Phase 8, H1). These tests pin both
// barriers: the validator refuses it, and the renderer cannot be made to emit a statement even if the validator is bypassed.

var hostileNames = []string{
	"Lobby\"; iifname @guest_interfaces accept comment \"x",
	"a\nadd rule inet stayconnect forward accept",
	"a\r\nflush ruleset",
	"x}\n}\ntable inet evil {",
	"semi;colon",
	`back\slash`,
	"brace{",
	"hash # comment",
	"tab\there",
	"nul\x00byte",
	"emoji \U0001F4A5",
	strings.Repeat("a", MaxNetworkNameRunes+1),
	"   ",
	"",
}

func TestHostileNetworkNamesAreRefused(t *testing.T) {
	for _, name := range hostileNames {
		n := vlan20()
		n.Name = name
		iss := ValidateOne(n, topoT(), availAll(), nil, "")
		if !hasCode(iss, "invalid_name") && !hasCode(iss, "required") {
			t.Errorf("name %q was accepted: %+v", name, iss)
		}
	}
}

func TestOrdinaryNetworkNamesStillValidate(t *testing.T) {
	for _, name := range []string{"PRELIVE Test Guest", "VLAN 60", "Client Wi-Fi", "Guests (ground floor)", "Conference & Events",
		"Staff/Back office", "Le café", "شبكة الضيوف", "Gäste", "Гости", "Rooms 1-3: east", "Owner's lounge"} {
		n := vlan20()
		n.Name = name
		if iss := ValidateOne(n, topoT(), availAll(), nil, ""); hasCode(iss, "invalid_name") {
			t.Errorf("legitimate name %q was refused: %+v", name, iss)
		}
	}
}

func TestOtherRenderedFieldsAreValidated(t *testing.T) {
	cases := []struct {
		field, code string
		mut         func(n *GuestNetwork)
	}{
		{"bridge", "invalid_bridge_name", func(n *GuestNetwork) { n.BridgeName = "br-g20\"; drop" }},
		{"bridge too long", "invalid_bridge_name", func(n *GuestNetwork) { n.BridgeName = "br-g-aaaaaaaaaaaaaaaa" }},
		{"parent", "invalid_interface_name", func(n *GuestNetwork) { n.ParentInterface = "ens192\n  dhcp4: true" }},
		{"domain", "invalid_domain", func(n *GuestNetwork) { n.DomainName = "guest.local\"}, {\"name\":\"x" }},
		{"domain label", "invalid_domain", func(n *GuestNetwork) { n.DomainName = "-bad.example" }},
		{"hostname", "invalid_hostname", func(n *GuestNetwork) {
			n.Reservations = []Reservation{{MAC: "aa:bb:cc:dd:ee:ff", ReservedIP: "10.20.0.5", Hostname: "pc one", Enabled: true}}
		}},
	}
	for _, c := range cases {
		n := vlan20()
		c.mut(&n)
		if iss := ValidateOne(n, topoT(), nil, nil, ""); !hasCode(iss, c.code) {
			t.Errorf("%s: want %s, got %+v", c.field, c.code, iss)
		}
	}
}

// The syntax checks apply to a network that will be rendered; a disabled one is not rendered and must not block every other change.
func TestDisabledNetworkIsNotBlockedByAbsentParentOrLegacyText(t *testing.T) {
	n := vlan20()
	n.Enabled = false
	n.Name = "legacy \"quoted\" name"
	n.ParentInterface = "ens999" // removed NIC
	avail := map[string]bool{"ens160": true}
	if iss := ValidateOne(n, topoT(), avail, map[string]bool{"ens999": true}, ""); len(iss) != 0 {
		t.Fatalf("a disabled network on a removed interface blocked validation: %+v", iss)
	}
	set := ValidateSet([]GuestNetwork{n}, topoT(), avail, map[string]bool{"ens999": true})
	if !set.OK {
		t.Fatalf("a disabled network on a removed interface fails the whole set: %+v", set.Issues)
	}
	// ...but enabling it brings every check back.
	n.Enabled = true
	iss := ValidateOne(n, topoT(), avail, map[string]bool{"ens999": true}, "")
	if !hasCode(iss, "interface_not_present") || !hasCode(iss, "invalid_name") {
		t.Fatalf("enabling must re-apply the checks, got %+v", iss)
	}
}

// Second barrier: even a value that bypassed validation (a hand-edited row) cannot add a statement or leave its string.
func TestRendererNeverEmitsHostileNameAsSyntax(t *testing.T) {
	benign := vlan20()
	benign.InternetEnabled = false
	want := strings.Count(string(RenderNftables([]GuestNetwork{benign}, topoT())), "\n")
	for _, name := range hostileNames {
		n := benign
		n.Name = name
		out := string(RenderNftables([]GuestNetwork{n}, topoT()))
		if got := strings.Count(out, "\n"); got != want {
			t.Errorf("name %q changed the ruleset from %d to %d lines", name, want, got)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "internet disabled for") {
				if q := strings.Count(line, "\""); q%2 != 0 || !strings.HasSuffix(line, "\"") {
					t.Errorf("name %q left an unbalanced string: %s", name, line)
				}
			}
		}
		// Inert text inside the quoted comment is fine; a statement of its own is not.
		for _, line := range strings.Split(out, "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "flush ") || strings.HasPrefix(l, "table inet evil") {
				t.Errorf("name %q produced a statement: %q", name, line)
			}
		}
	}
	// A bridge name that bypassed validation is reduced to the safe interface alphabet too.
	n := benign
	n.BridgeName = "br\"x; accept"
	out := string(RenderNftables([]GuestNetwork{n}, topoT()))
	if strings.Contains(out, "br\"x") || strings.Contains(out, "x; accept") {
		t.Errorf("hostile bridge name reached the ruleset:\n%s", out)
	}
}

func FuzzRenderedNameCannotBreakTheRuleset(f *testing.F) {
	for _, s := range hostileNames {
		f.Add(s)
	}
	f.Add("PRELIVE Test Guest")
	benign := vlan20()
	benign.InternetEnabled = false
	want := strings.Count(string(RenderNftables([]GuestNetwork{benign}, topoT())), "\n")
	f.Fuzz(func(t *testing.T, name string) {
		n := benign
		n.Name = name
		out := string(RenderNftables([]GuestNetwork{n}, topoT()))
		if got := strings.Count(out, "\n"); got != want {
			t.Fatalf("name %q changed the ruleset line count %d -> %d", name, want, got)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "internet disabled for") && strings.Count(line, "\"")%2 != 0 {
				t.Fatalf("name %q left an unbalanced string: %s", name, line)
			}
		}
		// What the validator accepts must survive rendering unchanged.
		if CheckNetworkName(name) == "" && !strings.Contains(out, "internet disabled for "+name+"\"") {
			t.Fatalf("a valid name %q was altered by the renderer", name)
		}
	})
}
