package sysnet

import "testing"

// A WAN-only change on an appliance whose base LAN bridge has no address is not blocked by the LAN rules.
func TestValidateProposal_WANOnlyChangeIsNotBlockedByAnUnaddressedLAN(t *testing.T) {
	cur := LANConfig{PhysicalInterface: "ens192", Bridge: "br-lan"}
	p := Proposal{WAN: &WANConfig{Interface: "ens160", Mode: "static", IP: "192.0.2.30", PrefixLen: 24, Gateway: "192.0.2.1"}}
	w, l := Merge(goodWAN(), cur, p)
	if full := ValidateFull(w, l); full.OK {
		t.Fatal("precondition: the full validation rejects the unaddressed LAN")
	}
	if r := ValidateProposal(w, l, cur, p); !r.OK {
		t.Fatalf("a WAN-only change was blocked by the LAN: %+v", r.Issues)
	}
	bad := Proposal{WAN: &WANConfig{Interface: "ens160", Mode: "static", IP: "192.0.2.30", PrefixLen: 24}}
	w, l = Merge(goodWAN(), cur, bad)
	if r := ValidateProposal(w, l, cur, bad); r.OK || !hasCode(r, "no_gateway") {
		t.Fatalf("a WAN with no gateway passed: %+v", r)
	}
}

func TestValidateProposal_LANRulesApplyWhenTheLANIsInPlay(t *testing.T) {
	cur := goodLAN()
	lan := goodLAN()
	lan.IP = "10.10.0.0"
	p := Proposal{LAN: &lan}
	w, l := Merge(goodWAN(), cur, p)
	if r := ValidateProposal(w, l, cur, p); r.OK || !hasCode(r, "network_address") {
		t.Fatalf("a bad LAN address passed: %+v", r)
	}
}

// Different DHCP values are refused rather than silently discarded.
func TestValidateProposal_DHCPIsNotEditedHere(t *testing.T) {
	cur := goodLAN()
	same := goodLAN()
	p := Proposal{LAN: &same}
	w, l := Merge(goodWAN(), cur, p)
	if r := ValidateProposal(w, l, cur, p); !r.OK {
		t.Fatalf("re-sending the DHCP values in force was refused: %+v", r.Issues)
	}
	changed := goodLAN()
	changed.DHCPEnd = "10.10.0.200"
	p = Proposal{LAN: &changed}
	w, l = Merge(goodWAN(), cur, p)
	if r := ValidateProposal(w, l, cur, p); r.OK || !hasCode(r, "dhcp_not_edited_here") {
		t.Fatalf("a DHCP edit was accepted and would have been discarded: %+v", r)
	}
}
