package main

import (
	"strings"
	"testing"
	"time"
)

func ready(id, label string) rsInterface {
	return rsInterface{ID: id, Label: label, Lifecycle: "ACTIVE", Published: true, HealthKnown: true, RoomReady: true, Transport: "CONNECTED"}
}

func ifs(list ...rsInterface) map[string]rsInterface {
	m := map[string]rsInterface{}
	for _, i := range list {
		m[i.ID] = i
	}
	return m
}

func netOf(a roomSignInAssessment, id string) rsNetwork {
	for _, n := range a.Networks {
		if n.NetworkID == id {
			return n
		}
	}
	return rsNetwork{}
}

var now = time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)

func TestAPmsFreeNetworkIsNeverBrokenWhateverThePmsIsDoing(t *testing.T) {
	down := ready("p1", "Protel")
	down.RoomReady, down.Reason = false, roomAuthFeedSilent
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n1", Name: "Vouchers", Enabled: true, Policy: policyNone}}, ifs(down), now)
	if n := netOf(a, "n1"); n.State != rsOff {
		t.Fatalf("a deliberately PMS-free network is %s, want OFF", n.State)
	}
	if len(a.Issues) != 0 || a.Overall != "NOT_OFFERED" {
		t.Fatalf("a PMS-free network raised %+v (overall %s)", a.Issues, a.Overall)
	}
}

func TestAPmsThatAlsoServesAnArchivedNetworkDoesNotWarnWhenItServesAnActiveOne(t *testing.T) {
	p := ready("p1", "Protel")
	a := assessRoomSignIn([]rsNetworkFacts{
		{ID: "old", Name: "VLAN 60", Enabled: false, Archived: true, Policy: policyDedicated, Mapped: []string{"p1"}},
		{ID: "new", Name: "Client Wi-Fi", Enabled: true, IsDefault: true, Policy: policyDedicated, Mapped: []string{"p1"}},
	}, ifs(p), now)
	if len(a.Issues) != 0 || a.Overall != "READY" {
		t.Fatalf("issues %+v overall %s, want a clean READY", a.Issues, a.Overall)
	}
	for _, i := range a.Interfaces {
		if i.ID == "p1" && (i.State != "SERVING" || i.ArchivedRoutes != 1 || len(i.Serving) != 1) {
			t.Fatalf("interface summary %+v", i)
		}
	}
}

func TestARoutedOnlyToArchivedNetworksPmsWarnsOnlyThroughTheNetworkThatLacksRouting(t *testing.T) {
	p := ready("p1", "Protel")
	a := assessRoomSignIn([]rsNetworkFacts{
		{ID: "old", Name: "VLAN 60", Enabled: false, Archived: true, Policy: policyDedicated, Mapped: []string{"p1"}},
		{ID: "new", Name: "Client Wi-Fi", Enabled: true, IsDefault: true},
	}, ifs(p), now)
	n := netOf(a, "new")
	if n.State != rsUndecided || len(a.Issues) != 1 || a.Issues[0].NetworkID != "new" || a.Issues[0].Href != "/pms-routing" {
		t.Fatalf("state %s issues %+v: want one routing decision for the active network", n.State, a.Issues)
	}
	for _, i := range a.Interfaces {
		if i.ID == "p1" && i.State != "NOT_USED" {
			t.Fatalf("a PMS serving no active network is %s, want NOT_USED (not impaired)", i.State)
		}
	}
}

func TestAnUndecidedNetworkIsOnlyAProblemWhereAPmsCouldServeIt(t *testing.T) {
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true}}, ifs(), now)
	if netOf(a, "n").State != rsNotSet || len(a.Issues) != 0 {
		t.Fatalf("no PMS at all: %s %+v", netOf(a, "n").State, a.Issues)
	}
	off := ready("p1", "Protel")
	off.Lifecycle = "AUTH_DISABLED"
	a = assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true}}, ifs(off), now)
	if netOf(a, "n").State != rsNotSet || len(a.Issues) != 0 {
		t.Fatalf("no servable PMS: %s %+v", netOf(a, "n").State, a.Issues)
	}
}

func TestADedicatedNetworkFollowsItsOneInterface(t *testing.T) {
	bad := ready("p1", "Protel")
	bad.RoomReady, bad.Reason = false, roomAuthContinuityGap
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(bad), now)
	n := netOf(a, "n")
	if n.State != rsBlocked || len(a.Issues) != 1 || a.Issues[0].Severity != "err" || a.Issues[0].Href != "/pms-interfaces" {
		t.Fatalf("state %s issues %+v", n.State, a.Issues)
	}
	if !strings.Contains(a.Issues[0].Detail, "Protel") || !strings.Contains(a.Issues[0].Detail, "updates from the PMS were missed") {
		t.Fatalf("the warning does not name the interface and the reason: %q", a.Issues[0].Detail)
	}
	// corrected: the warning clears with no other action
	a = assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(ready("p1", "Protel")), now)
	if netOf(a, "n").State != rsReady || len(a.Issues) != 0 {
		t.Fatalf("a corrected network still warns: %+v", a.Issues)
	}
}

func TestASharedStrictNetworkIsBlockedByAnyCandidateThatCannotAnswer(t *testing.T) {
	good, bad := ready("p1", "Protel A"), ready("p2", "Protel B")
	bad.RoomReady, bad.Reason = false, roomAuthFeedSilent
	facts := []rsNetworkFacts{{ID: "n", Name: "Resort", Enabled: true, Policy: policyShared, Mapped: []string{"p1", "p2"}}}
	a := assessRoomSignIn(facts, ifs(good, bad), now)
	n := netOf(a, "n")
	if n.State != rsBlocked || !strings.Contains(n.Summary, "none can be trusted") {
		t.Fatalf("a strict shared network with one candidate down is %s: %q", n.State, n.Summary)
	}
	if len(n.Reasons) != 1 || n.Reasons[0].InterfaceID != "p2" {
		t.Fatalf("the reason must name the candidate that cannot answer: %+v", n.Reasons)
	}
	a = assessRoomSignIn(facts, ifs(good, ready("p2", "Protel B")), now)
	if netOf(a, "n").State != rsReady {
		t.Fatalf("both ready: %s", netOf(a, "n").State)
	}
}

func TestUnknownEvidenceStaysUnknown(t *testing.T) {
	unk := ready("p1", "Protel")
	unk.HealthKnown, unk.RoomReady = false, false
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(unk), now)
	if netOf(a, "n").State != rsUnknown || len(a.Issues) != 0 || a.Overall != "UNKNOWN" {
		t.Fatalf("unreadable health produced %s, issues %+v, overall %s", netOf(a, "n").State, a.Issues, a.Overall)
	}
	// but a determinate failure beats unknown on a shared network
	bad := ready("p2", "B")
	bad.RoomReady, bad.Reason = false, roomAuthFeedSilent
	a = assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Policy: policyShared, Mapped: []string{"p1", "p2"}}}, ifs(unk, bad), now)
	if netOf(a, "n").State != rsBlocked {
		t.Fatalf("a known failure must not be hidden by an unknown: %s", netOf(a, "n").State)
	}
}

func TestTransportDownWithAnIntactMirrorIsStillReady(t *testing.T) {
	p := ready("p1", "Protel")
	p.Transport, p.Source = "DISCONNECTED", roomAuthSourceLastRoster
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(p), now)
	if netOf(a, "n").State != rsReady || len(a.Issues) != 0 {
		t.Fatalf("PMS transport health leaked into room sign-in readiness: %s %+v", netOf(a, "n").State, a.Issues)
	}
}

func TestAnInterfaceOutOfServiceOnAnActiveNetworkIsReportedByName(t *testing.T) {
	p := ready("p1", "Protel")
	p.Lifecycle = "AUTH_DISABLED"
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(p), now)
	if netOf(a, "n").State != rsBlocked || a.Issues[0].Href != "/pms-routing" || !strings.Contains(a.Issues[0].Detail, "Protel is not in service") {
		t.Fatalf("%s %+v", netOf(a, "n").State, a.Issues)
	}
}

func TestAPolicyThatDisagreesWithItsInterfacesIsMisconfiguredNotGuessed(t *testing.T) {
	a := assessRoomSignIn([]rsNetworkFacts{
		{ID: "a", Name: "A", Enabled: true, Policy: policyShared, Mapped: []string{"p1"}},
		{ID: "b", Name: "B", Enabled: true, Mapped: []string{"p1", "p2"}},
		{ID: "c", Name: "C", Enabled: true, Policy: policyShared, Mapped: []string{"p1", "p2", "p3", "p4"}},
	}, ifs(ready("p1", "1"), ready("p2", "2"), ready("p3", "3"), ready("p4", "4")), now)
	for _, id := range []string{"a", "b", "c"} {
		if netOf(a, id).State != rsBadRouting {
			t.Fatalf("network %s is %s, want MISCONFIGURED", id, netOf(a, id).State)
		}
	}
}

func TestALegacySingleRouteWithoutAPolicyIsDedicated(t *testing.T) {
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, Mapped: []string{"p1"}}}, ifs(ready("p1", "Protel")), now)
	n := netOf(a, "n")
	if n.State != rsReady || n.Policy != policyDedicated || !n.Legacy {
		t.Fatalf("%+v", n)
	}
}

func TestANetworkWhoseRoomMethodIsSwitchedOffIsNotOffered(t *testing.T) {
	bad := ready("p1", "Protel")
	bad.RoomReady, bad.Reason = false, roomAuthFeedSilent
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: true, RoomMethodOff: true, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(bad), now)
	if netOf(a, "n").State != rsOff || len(a.Issues) != 0 {
		t.Fatalf("%s %+v", netOf(a, "n").State, a.Issues)
	}
}

func TestASwitchedOffNetworkKeepsItsRoutingAndRaisesNothing(t *testing.T) {
	bad := ready("p1", "Protel")
	bad.RoomReady, bad.Reason = false, roomAuthFeedSilent
	a := assessRoomSignIn([]rsNetworkFacts{{ID: "n", Name: "Guest", Enabled: false, Policy: policyDedicated, Mapped: []string{"p1"}}}, ifs(bad), now)
	if netOf(a, "n").State != rsInactive || len(a.Issues) != 0 {
		t.Fatalf("%s %+v", netOf(a, "n").State, a.Issues)
	}
}

func TestOverallSeparatesPartialFromBlocked(t *testing.T) {
	bad := ready("p2", "B")
	bad.RoomReady, bad.Reason = false, roomAuthFeedSilent
	a := assessRoomSignIn([]rsNetworkFacts{
		{ID: "n1", Name: "Good", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}},
		{ID: "n2", Name: "Bad", Enabled: true, Policy: policyDedicated, Mapped: []string{"p2"}},
	}, ifs(ready("p1", "A"), bad), now)
	if a.Overall != "PARTIAL" || len(a.Issues) != 1 || a.Issues[0].NetworkID != "n2" {
		t.Fatalf("%s %+v", a.Overall, a.Issues)
	}
}

func TestRoutingFingerprintIsOrderIndependentAndPolicySensitive(t *testing.T) {
	if routingFingerprint(policyShared, []string{"b", "a"}) != routingFingerprint(policyShared, []string{"a", "b"}) {
		t.Fatal("the fingerprint depends on the order the interfaces were listed in")
	}
	if routingFingerprint(policyShared, []string{"a", "b"}) == routingFingerprint(policyDedicated, []string{"a", "b"}) {
		t.Fatal("the fingerprint ignores the policy")
	}
	if routingFingerprint("", nil) == routingFingerprint(policyNone, nil) {
		t.Fatal("undecided and NONE must differ: one is a decision, the other is not")
	}
}

// Room sign-in switched off for the whole site is not "ready", however healthy the PMS is: scd refuses every guest
// (pms_method_disabled). Switching it on makes the same network ready.
func TestRoomSignInSwitchedOffForTheSiteIsNotReportedReady(t *testing.T) {
	off := rsNetworkFacts{ID: "n", Name: "Client Wi-Fi", Enabled: true, Policy: policyDedicated, Mapped: []string{"p1"}, SiteMethodOff: true}
	a := assessRoomSignIn([]rsNetworkFacts{off}, ifs(ready("p1", "CHR")), now)
	n := netOf(a, "n")
	if n.State != rsOff || a.Overall == "READY" || !strings.Contains(n.Summary, "Sign-in methods") {
		t.Fatalf("site switch off: state %s overall %s summary %q", n.State, a.Overall, n.Summary)
	}
	off.SiteMethodOff = false
	if a := assessRoomSignIn([]rsNetworkFacts{off}, ifs(ready("p1", "CHR")), now); netOf(a, "n").State != rsReady || a.Overall != "READY" {
		t.Fatalf("site switch on: %s %s", netOf(a, "n").State, a.Overall)
	}
}
