package main

// Topology fields (network_type, parent_interface, vlan_id) are immutable: a silently ignored change would leave the
// kernel bridge on the old VLAN tag while the rendered config and fingerprint claim the new one. Refuse a change,
// but accept an unchanged echo, since a client may PUT back the whole object it fetched.

import (
	"strings"
	"testing"
)

func vlan(n int) *int { return &n }

func TestAnUnchangedTopologyEchoIsAccepted(t *testing.T) {
	if msg := immutableTopologyChange(
		guestNetworkInput{NetworkType: "vlan", ParentInterface: "ens192", VLANID: vlan(20)},
		"vlan", "ens192", vlan(20)); msg != "" {
		t.Errorf("an unchanged echo was refused: %s", msg)
	}
	if msg := immutableTopologyChange(
		guestNetworkInput{NetworkType: "untagged", ParentInterface: "ens192"},
		"untagged", "ens192", nil); msg != "" {
		t.Errorf("an unchanged echo was refused: %s", msg)
	}
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

// Untagged -> tagged is not converted in place; the refusal names the supported delete+recreate sequence.
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

// The guard covers topology only; everything else stays editable.
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
		NetworkType:     "vlan",
		ParentInterface: "ens192",
		VLANID:          vlan(20),
	}
	if msg := immutableTopologyChange(in, "vlan", "ens192", vlan(20)); msg != "" {
		t.Errorf("an ordinary settings edit was refused by the topology guard: %s", msg)
	}
}
