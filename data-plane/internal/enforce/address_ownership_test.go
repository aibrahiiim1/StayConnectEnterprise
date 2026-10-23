package enforce

// The applier refuses both sessions of a plan that claims one (bridge, IP) twice, so the producer resolves it.

import "testing"

func shape(id, ip string) SessionShape {
	return SessionShape{SessionID: id, EntitlementID: "e-" + id, DeviceID: "dev", IP: ip, Bridge: "br-g",
		DownKbps: 9000, UpKbps: 4000}
}

// The ordinary case (one claimant per address) must be untouched, including the allocation.
func TestAddressOwnership_LeavesAnUncontestedPlanAlone(t *testing.T) {
	in := Plan{Shape: []SessionShape{shape("s1", "10.0.0.1"), shape("s2", "10.0.0.2")}}
	out := resolveAddressOwnership(in)
	if len(out.Shape) != 2 || len(out.Tear) != 0 {
		t.Fatalf("an uncontested plan was rewritten: shape=%d tear=%d", len(out.Shape), len(out.Tear))
	}
}

// Two sessions, one device, one address: one phone logging into two rooms.
func TestAddressOwnership_NewestSessionKeepsTheAddress(t *testing.T) {
	// The plan query orders by (started, id), so the later element is the newer session.
	older, newer := shape("s-older", "192.168.77.139"), shape("s-newer", "192.168.77.139")
	out := resolveAddressOwnership(Plan{Shape: []SessionShape{older, newer}})

	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s-newer" {
		t.Fatalf("the newest session did not keep the address: %+v", out.Shape)
	}
	if len(out.Tear) != 1 || out.Tear[0].SessionID != "s-older" {
		t.Fatalf("the superseded session was not torn down: %+v", out.Tear)
	}
	if got := out.Tear[0].EndReason; got != EndReasonSupersededOnAddress {
		t.Fatalf("end reason = %q, want %q — an operator reading the record must see a handover, not a "+
			"teardown failure", got, EndReasonSupersededOnAddress)
	}
	if out.Tear[0].DownKbps != 0 || out.Tear[0].UpKbps != 0 {
		t.Fatalf("a superseded session kept its rates: %+v", out.Tear[0])
	}
}

// The rule is per address: two sessions of one device on different addresses are both enforceable.
func TestAddressOwnership_SameDeviceOnTwoAddressesIsNotAConflict(t *testing.T) {
	a, b := shape("s1", "10.0.0.1"), shape("s2", "10.0.0.2")
	b.DeviceID = a.DeviceID
	out := resolveAddressOwnership(Plan{Shape: []SessionShape{a, b}})
	if len(out.Shape) != 2 || len(out.Tear) != 0 {
		t.Fatalf("one device on two addresses was treated as a conflict: shape=%d tear=%d",
			len(out.Shape), len(out.Tear))
	}
}

// The same address on different bridges is two endpoints and two tc classes.
func TestAddressOwnership_SameIPOnDifferentBridgesIsNotAConflict(t *testing.T) {
	a, b := shape("s1", "10.0.0.1"), shape("s2", "10.0.0.1")
	b.Bridge = "br-g-other"
	out := resolveAddressOwnership(Plan{Shape: []SessionShape{a, b}})
	if len(out.Shape) != 2 || len(out.Tear) != 0 {
		t.Fatalf("one address on two bridges was treated as a conflict: shape=%d tear=%d",
			len(out.Shape), len(out.Tear))
	}
}

// Three claimants must collapse to one in a single pass.
func TestAddressOwnership_ResolvesMoreThanTwoInOnePass(t *testing.T) {
	out := resolveAddressOwnership(Plan{Shape: []SessionShape{
		shape("s1", "10.0.0.9"), shape("s2", "10.0.0.9"), shape("s3", "10.0.0.9")}})
	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s3" {
		t.Fatalf("three claimants did not collapse to the newest: %+v", out.Shape)
	}
	if len(out.Tear) != 2 {
		t.Fatalf("two superseded sessions were not torn down: %+v", out.Tear)
	}
	for _, s := range out.Tear {
		if s.EndReason != EndReasonSupersededOnAddress {
			t.Fatalf("superseded session %s carries reason %q", s.SessionID, s.EndReason)
		}
	}
}

// A session with no address stays in the plan so the applier reports it.
func TestAddressOwnership_UnaddressedSessionsAreLeftForTheApplier(t *testing.T) {
	out := resolveAddressOwnership(Plan{Shape: []SessionShape{shape("s1", ""), shape("s2", "")}})
	if len(out.Shape) != 2 || len(out.Tear) != 0 {
		t.Fatalf("unaddressed sessions were superseded: shape=%d tear=%d", len(out.Shape), len(out.Tear))
	}
}

// An ended session must never take an address away from a live one.
func TestAddressOwnership_ExistingTearsDoNotContestTheAddress(t *testing.T) {
	live := shape("s-live", "10.0.0.5")
	dead := shape("s-dead", "10.0.0.5")
	dead.DownKbps, dead.UpKbps = 0, 0
	out := resolveAddressOwnership(Plan{Shape: []SessionShape{live}, Tear: []SessionShape{dead}})
	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s-live" {
		t.Fatalf("a live session lost its address to an ended one: %+v", out.Shape)
	}
	if len(out.Tear) != 1 {
		t.Fatalf("the tear list was rewritten: %+v", out.Tear)
	}
}
