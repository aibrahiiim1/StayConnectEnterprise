package main

// THE TOPOLOGY FIELDS ARE IMMUTABLE, AND SAYING SO IS PART OF BEING IMMUTABLE.
//
// THE DEFECT THESE PIN. guestNetworkInput accepts network_type, parent_interface and vlan_id. The UPDATE
// statement in updateGuestNetwork names none of them. The handler answered 200 {"status":"updated"}. So a
// client that PUT {"vlan_id": 30} against a VLAN-20 network was told its change had been applied, and
// nothing anywhere had changed -- no 400, no "immutable" code, nothing in the audit detail.
//
// And a client that worked around it by editing the row directly would fare no better: the apply diff is
// keyed on bridge_name, bridge_name is computed once at create, so the existing bridge is found present,
// createNetwork is never called, the VLAN sub-interface for the new id is never made -- while the rendered
// netplan and nft DESCRIBE the new id and the nft fingerprint reports "converged" against a kernel whose
// bridge carries the wrong tag.
//
// The rule is therefore: refuse a CHANGE, accept an unchanged echo. The second half matters as much as the
// first -- a client that GETs the object, edits one setting and PUTs the whole thing back is doing nothing
// wrong, and the previous UI omitted the fields only by convention.

import (
	"strings"
	"testing"
)

func vlan(n int) *int { return &n }

func TestAnUnchangedTopologyEchoIsAccepted(t *testing.T) {
	// A tagged network echoed back exactly as stored.
	if msg := immutableTopologyChange(
		guestNetworkInput{NetworkType: "vlan", ParentInterface: "ens192", VLANID: vlan(20)},
		"vlan", "ens192", vlan(20)); msg != "" {
		t.Errorf("an unchanged echo was refused: %s", msg)
	}
	// An untagged network echoed back exactly as stored, vlan_id absent.
	if msg := immutableTopologyChange(
		guestNetworkInput{NetworkType: "untagged", ParentInterface: "ens192"},
		"untagged", "ens192", nil); msg != "" {
		t.Errorf("an unchanged echo was refused: %s", msg)
	}
	// The fields omitted entirely, which is what the UI sends.
	if msg := immutableTopologyChange(guestNetworkInput{}, "vlan", "ens192", vlan(20)); msg != "" {
		t.Errorf("omitting the topology fields was refused: %s", msg)
	}
}

func TestChangingTheVLANIDIsRefused(t *testing.T) {
	msg := immutableTopologyChange(guestNetworkInput{VLANID: vlan(30)}, "vlan", "ens192", vlan(20))
	if msg == "" {
		t.Fatal("changing vlan_id from 20 to 30 was ACCEPTED; the handler would answer 200 and change nothing")
	}
	for _, want := range []string{"vlan_id", "VLAN 20", "disable"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q: %s", want, msg)
		}
	}
}

func TestSettingAVLANIDOnAnUntaggedNetworkIsRefused(t *testing.T) {
	msg := immutableTopologyChange(guestNetworkInput{VLANID: vlan(30)}, "untagged", "ens192", nil)
	if msg == "" {
		t.Fatal("setting vlan_id on an untagged network was ACCEPTED")
	}
	if !strings.Contains(msg, "untagged") {
		t.Errorf("refusal does not say the network is untagged: %s", msg)
	}
}

// The precise question the closure mission asked: is untagged -> tagged conversion a product change, or is
// delete+recreate the intentional lifecycle? The code, the UI prose and this refusal all now say the same
// thing, in the same words, and the message names the sequence.
func TestConvertingUntaggedToTaggedIsRefusedAndNamesTheSupportedPath(t *testing.T) {
	msg := immutableTopologyChange(
		guestNetworkInput{NetworkType: "vlan", VLANID: vlan(40)}, "untagged", "ens192", nil)
	if msg == "" {
		t.Fatal("converting an untagged network to a tagged one in place was ACCEPTED")
	}
	if !strings.Contains(msg, "network_type") {
		t.Errorf("refusal does not name network_type: %s", msg)
	}
	for _, want := range []string{"disable", "delete", "create a new one"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not name the supported lifecycle step %q: %s", want, msg)
		}
	}
}

func TestChangingTheParentInterfaceIsRefused(t *testing.T) {
	msg := immutableTopologyChange(
		guestNetworkInput{ParentInterface: "ens224"}, "vlan", "ens192", vlan(20))
	if msg == "" {
		t.Fatal("moving a network to another parent interface was ACCEPTED")
	}
	for _, want := range []string{"parent_interface", "ens192"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q: %s", want, msg)
		}
	}
}

func TestConvertingTaggedToUntaggedIsRefused(t *testing.T) {
	msg := immutableTopologyChange(
		guestNetworkInput{NetworkType: "untagged"}, "vlan", "ens192", vlan(20))
	if msg == "" {
		t.Fatal("converting a tagged network to untagged in place was ACCEPTED")
	}
}

// The refusal is about topology only. Everything else on the object stays editable, which is the whole
// reason the endpoint exists.
func TestOrdinarySettingsAreNotAffectedByTheTopologyGuard(t *testing.T) {
	yes := true
	in := guestNetworkInput{
		Name:            "Guest Wi-Fi (renamed)",
		Description:     "third floor",
		GatewayIP:       "10.20.0.1",
		SubnetCIDR:      "10.20.0.0/22",
		DHCPMode:        "local",
		DNSMode:         "custom",
		DNSServers:      []string{"10.20.0.1"},
		LeaseDefault:    3600,
		CaptiveEnabled:  &yes,
		InternetEnabled: &yes,
		NATEnabled:      &yes,
		// and the topology echoed unchanged
		NetworkType:     "vlan",
		ParentInterface: "ens192",
		VLANID:          vlan(20),
	}
	if msg := immutableTopologyChange(in, "vlan", "ens192", vlan(20)); msg != "" {
		t.Errorf("an ordinary settings edit was refused by the topology guard: %s", msg)
	}
}
