package netcfg

import (
	"strings"
	"testing"
)

// A guest network must not be accepted on an interface that is no longer there: the inventory is never pruned, so a
// row existing does not mean the appliance has the interface (e.g. a destroyed bridge).

func presenceNet(parent string) GuestNetwork {
	return GuestNetwork{
		Name: "guest", NetworkType: "untagged", ParentInterface: parent,
		BridgeName: "br-g-1", GatewayIP: "192.168.77.1", SubnetCIDR: "192.168.77.0/24",
		PrefixLen: 24, Enabled: true, DHCPMode: DHCPLocal,
		Pools: []Pool{{StartIP: "192.168.77.100", EndIP: "192.168.77.200"}},
	}
}

func codeOf(iss []Issue, field string) string {
	for _, i := range iss {
		if i.Field == field {
			return i.Code
		}
	}
	return ""
}

func msgOf(iss []Issue, field string) string {
	for _, i := range iss {
		if i.Field == field {
			return i.Message
		}
	}
	return ""
}

// Recorded once, gone now: refused.
func TestAbsentButRecordedParentIsRefused(t *testing.T) {
	present := map[string]bool{"ens192": true}
	known := map[string]bool{"ens192": true, "vguest-h": true}

	iss := ValidateOne(presenceNet("vguest-h"), topoT(), present, known, "")
	if got := codeOf(iss, "parent_interface"); got != "interface_not_present" {
		t.Fatalf("a recorded-but-absent parent must be refused as interface_not_present, got %q", got)
	}
	if m := msgOf(iss, "parent_interface"); !strings.Contains(m, "NOT PRESENT") {
		t.Fatalf("the message must say the interface is not present now, got %q", m)
	}
}

// A name never seen is a different problem with the original code, since operators resolve a typo differently from a
// pulled card.
func TestNeverKnownParentKeepsNotFound(t *testing.T) {
	present := map[string]bool{"ens192": true}
	known := map[string]bool{"ens192": true}

	iss := ValidateOne(presenceNet("ens999"), topoT(), present, known, "")
	if got := codeOf(iss, "parent_interface"); got != "interface_not_found" {
		t.Fatalf("an interface this appliance never had must stay interface_not_found, got %q", got)
	}
}

// A present parent is accepted.
func TestPresentParentIsAccepted(t *testing.T) {
	present := map[string]bool{"ens192": true}
	known := map[string]bool{"ens192": true, "vguest-h": true}
	if iss := ValidateOne(presenceNet("ens192"), topoT(), present, known, ""); len(iss) != 0 {
		t.Fatalf("a present parent must validate cleanly, got %v", iss)
	}
}

// An interface that reappears is usable again; absence was never recorded as state.
func TestReappearedParentIsAcceptedAgain(t *testing.T) {
	known := map[string]bool{"ens192": true, "ens224": true}

	absent := map[string]bool{"ens192": true}
	if codeOf(ValidateOne(presenceNet("ens224"), topoT(), absent, known, ""), "parent_interface") != "interface_not_present" {
		t.Fatal("while absent it must be refused")
	}
	back := map[string]bool{"ens192": true, "ens224": true}
	if iss := ValidateOne(presenceNet("ens224"), topoT(), back, known, ""); len(iss) != 0 {
		t.Fatalf("once the interface is observed again it must validate cleanly, got %v", iss)
	}
}

// Without a known set the validator still refuses an absent parent but cannot say which kind.
func TestNilKnownSetStillRefusesAbsent(t *testing.T) {
	present := map[string]bool{"ens192": true}
	if codeOf(ValidateOne(presenceNet("vguest-h"), topoT(), present, nil, ""), "parent_interface") != "interface_not_found" {
		t.Fatal("with no known set the absent parent must still be refused")
	}
}

// A nil present set means presence is unknown to this caller: the check is skipped, not turned into a refusal.
func TestNilPresentSetSkipsTheCheck(t *testing.T) {
	if iss := ValidateOne(presenceNet("anything"), topoT(), nil, nil, ""); len(iss) != 0 {
		t.Fatalf("a nil present set must skip the presence check, got %v", iss)
	}
}
