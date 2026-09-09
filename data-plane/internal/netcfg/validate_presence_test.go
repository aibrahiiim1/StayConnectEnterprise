package netcfg

import (
	"strings"
	"testing"
)

// A GUEST NETWORK MUST NOT BE ACCEPTED ON AN INTERFACE THAT IS NO LONGER THERE.
//
// The inventory this set comes from accumulates and is never pruned, so "a row exists" used to be treated as
// "the appliance has this interface". Observed on PRE-LIVE: a row for `vguest-h`, a bridge destroyed weeks
// earlier, would have satisfied validation. These cases pin the distinction the fix introduces.

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

// THE DEFECT ITSELF: recorded once, gone now, must be refused.
func TestAbsentButRecordedParentIsRefused(t *testing.T) {
	present := map[string]bool{"ens192": true}
	known := map[string]bool{"ens192": true, "vguest-h": true} // vguest-h was seen once, long ago

	iss := ValidateOne(presenceNet("vguest-h"), topoT(), present, known, "")
	if got := codeOf(iss, "parent_interface"); got != "interface_not_present" {
		t.Fatalf("a recorded-but-absent parent must be refused as interface_not_present, got %q", got)
	}
	if m := msgOf(iss, "parent_interface"); !strings.Contains(m, "NOT PRESENT") {
		t.Fatalf("the message must say the interface is not present now, got %q", m)
	}
}

// A NAME THE APPLIANCE HAS NEVER HAD is a different problem and keeps the original code, because an operator
// resolves a typo differently from a pulled card.
func TestNeverKnownParentKeepsNotFound(t *testing.T) {
	present := map[string]bool{"ens192": true}
	known := map[string]bool{"ens192": true}

	iss := ValidateOne(presenceNet("ens999"), topoT(), present, known, "")
	if got := codeOf(iss, "parent_interface"); got != "interface_not_found" {
		t.Fatalf("an interface this appliance never had must stay interface_not_found, got %q", got)
	}
}

// A PRESENT PARENT IS ACCEPTED — the check must not have become a blanket refusal.
func TestPresentParentIsAccepted(t *testing.T) {
	present := map[string]bool{"ens192": true}
	known := map[string]bool{"ens192": true, "vguest-h": true}
	if iss := ValidateOne(presenceNet("ens192"), topoT(), present, known, ""); len(iss) != 0 {
		t.Fatalf("a present parent must validate cleanly, got %v", iss)
	}
}

// REAPPEARANCE. An interface that comes back is immediately usable again — nothing about its absence is
// sticky, because absence was never recorded as state.
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

// WITHOUT A KNOWN SET the validator still refuses an absent parent; it just cannot say which kind it is.
// This keeps every existing caller correct rather than silently fail-open.
func TestNilKnownSetStillRefusesAbsent(t *testing.T) {
	present := map[string]bool{"ens192": true}
	if codeOf(ValidateOne(presenceNet("vguest-h"), topoT(), present, nil, ""), "parent_interface") != "interface_not_found" {
		t.Fatal("with no known set the absent parent must still be refused")
	}
}

// A NIL PRESENT SET means presence is unknown to this caller, and the check is skipped rather than turned
// into a refusal of everything. That is pre-existing contract and must not change.
func TestNilPresentSetSkipsTheCheck(t *testing.T) {
	if iss := ValidateOne(presenceNet("anything"), topoT(), nil, nil, ""); len(iss) != 0 {
		t.Fatalf("a nil present set must skip the presence check, got %v", iss)
	}
}
