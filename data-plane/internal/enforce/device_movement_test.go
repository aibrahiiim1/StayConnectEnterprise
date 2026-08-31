package enforce

// A DEVICE IS NOT AN ADDRESS, AND AN ADDRESS IS NOT AN ACCOUNT.
//
// The rules these tests pin, in the order they were got wrong:
//
//   * a device may change address, and doing so must cost it nothing — same Entitlement, same Purchase, same
//     quota, same accumulated usage;
//   * the attachment it left behind must not go on being enforced, because DHCP will hand that address to
//     somebody else and the appliance would authorize a stranger under this guest's account;
//   * two sessions on ONE address is a different question with a different answer, and the two rules must not
//     be confused with each other;
//   * a random or private MAC is simply a device the property has not seen before. It is not a person, and it
//     is handled by the device-authorization policy, not here.

import "testing"

func moved(id, dev, ip string) SessionShape {
	return SessionShape{SessionID: id, EntitlementID: "ent-1", DeviceID: dev, IP: ip, Bridge: "br-g",
		MAC: "aa:bb:cc:dd:ee:" + dev[len(dev)-2:], DownKbps: 9000, UpKbps: 4000}
}

// THE LIVE CASE. One iPhone: authenticated on .139, switched off for days, came back on .102. The .139
// attachment must end, and it must end for a reason that says what happened.
func TestDeviceMovement_OlderAttachmentIsRetired(t *testing.T) {
	// Query order is (started, id), so the later element is the newer session.
	out := resolveAddressOwnership(retireMovedDevices(Plan{Shape: []SessionShape{
		moved("s-old", "dev-01", "192.168.77.139"),
		moved("s-new", "dev-01", "192.168.77.102"),
	}}))

	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s-new" {
		t.Fatalf("the device's current attachment did not survive alone: %+v", out.Shape)
	}
	if len(out.Tear) != 1 || out.Tear[0].SessionID != "s-old" {
		t.Fatalf("the abandoned attachment was not retired: %+v", out.Tear)
	}
	if got := out.Tear[0].EndReason; got != EndReasonDeviceMoved {
		t.Fatalf("end reason = %q, want %q", got, EndReasonDeviceMoved)
	}
	if out.Tear[0].DownKbps != 0 || out.Tear[0].UpKbps != 0 {
		t.Fatal("a retired attachment kept its rates")
	}
	// The entitlement is the same on both rows and neither is touched: a move is not a re-purchase.
	if out.Shape[0].EntitlementID != "ent-1" || out.Tear[0].EntitlementID != "ent-1" {
		t.Fatal("the move changed which entitlement a session belongs to")
	}
}

// TWO DEVICES, TWO ADDRESSES, ONE ACCOUNT. Nothing moved; both must stay enforced. A rule that retired by
// entitlement instead of by device would silently disconnect a guest's laptop when their phone connected.
func TestDeviceMovement_TwoDevicesOnOneEntitlementBothSurvive(t *testing.T) {
	out := resolveAddressOwnership(retireMovedDevices(Plan{Shape: []SessionShape{
		moved("s-phone", "dev-01", "10.0.0.11"),
		moved("s-laptop", "dev-02", "10.0.0.12"),
	}}))
	if len(out.Shape) != 2 || len(out.Tear) != 0 {
		t.Fatalf("two devices on one account were treated as a move: shape=%d tear=%d",
			len(out.Shape), len(out.Tear))
	}
}

// A PRIVATE OR RANDOMISED MAC is a device this property has not seen before — an iPhone rotating its address
// looks exactly like a new one. It gets its own device row and its own session, and that is correct: nothing
// here may treat a MAC as a person, and the device LIMIT is what bounds it (enforced at the grant, per
// entitlement, against the pinned plan revision).
func TestDeviceMovement_ARandomisedMACIsJustAnotherDevice(t *testing.T) {
	rotated := moved("s-rotated", "dev-99", "10.0.0.13")
	rotated.MAC = "7a:1f:3c:9d:44:02" // locally administered, as iOS/Android private addresses are
	out := resolveAddressOwnership(retireMovedDevices(Plan{Shape: []SessionShape{
		moved("s-phone", "dev-01", "10.0.0.11"), rotated,
	}}))
	if len(out.Shape) != 2 || len(out.Tear) != 0 {
		t.Fatalf("a rotated MAC was mistaken for a move: shape=%d tear=%d", len(out.Shape), len(out.Tear))
	}
}

// SAME DEVICE, SAME ADDRESS, TWO SESSIONS. That is the address-ownership question — one accountable class per
// address — and it must keep its own answer and its own reason code rather than being absorbed into "moved".
func TestDeviceMovement_SameAddressStaysAnAddressConflict(t *testing.T) {
	out := resolveAddressOwnership(retireMovedDevices(Plan{Shape: []SessionShape{
		moved("s-first", "dev-01", "10.0.0.11"),
		moved("s-second", "dev-01", "10.0.0.11"),
	}}))
	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s-second" {
		t.Fatalf("the newest session did not keep the address: %+v", out.Shape)
	}
	if len(out.Tear) != 1 || out.Tear[0].EndReason != EndReasonSupersededOnAddress {
		t.Fatalf("reason = %+v, want %q", out.Tear, EndReasonSupersededOnAddress)
	}
}

// Three attachments of one device collapse to the newest in a single pass. Converging over several passes
// would leave the appliance authorizing abandoned addresses for as long as it took.
func TestDeviceMovement_CollapsesEveryStaleAttachmentInOnePass(t *testing.T) {
	out := resolveAddressOwnership(retireMovedDevices(Plan{Shape: []SessionShape{
		moved("s1", "dev-01", "10.0.0.11"),
		moved("s2", "dev-01", "10.0.0.12"),
		moved("s3", "dev-01", "10.0.0.13"),
	}}))
	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s3" {
		t.Fatalf("stale attachments survived: %+v", out.Shape)
	}
	if len(out.Tear) != 2 {
		t.Fatalf("tear=%d, want 2", len(out.Tear))
	}
	for _, s := range out.Tear {
		if s.EndReason != EndReasonDeviceMoved {
			t.Fatalf("%s carries reason %q", s.SessionID, s.EndReason)
		}
	}
}

// A session already being torn down is not a claim on anything, and must not retire a live attachment.
func TestDeviceMovement_AnEndedSessionDoesNotRetireALiveOne(t *testing.T) {
	dead := moved("s-dead", "dev-01", "10.0.0.11")
	dead.DownKbps, dead.UpKbps = 0, 0
	out := resolveAddressOwnership(retireMovedDevices(Plan{
		Shape: []SessionShape{moved("s-live", "dev-01", "10.0.0.12")},
		Tear:  []SessionShape{dead},
	}))
	if len(out.Shape) != 1 || out.Shape[0].SessionID != "s-live" {
		t.Fatalf("the live attachment was retired by an ended one: %+v", out.Shape)
	}
}
