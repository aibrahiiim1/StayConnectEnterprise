package enforce

// A device changing address costs nothing (same Entitlement, quota, usage), but its abandoned attachment must
// stop being enforced because DHCP will reassign that address. Same-address sessions are a separate rule.

import "testing"

func moved(id, dev, ip string) SessionShape {
	return SessionShape{SessionID: id, EntitlementID: "ent-1", DeviceID: dev, IP: ip, Bridge: "br-g",
		MAC: "aa:bb:cc:dd:ee:" + dev[len(dev)-2:], DownKbps: 9000, UpKbps: 4000}
}

// A phone authenticated on .139 comes back on .102: the .139 attachment must end with the move reason.
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
	if out.Shape[0].EntitlementID != "ent-1" || out.Tear[0].EntitlementID != "ent-1" {
		t.Fatal("the move changed which entitlement a session belongs to")
	}
}

// Two devices, one account: retiring by entitlement instead of device would disconnect the laptop.
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

// A private or randomised MAC is just a new device; the device limit at the grant bounds it.
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

// Same device, same address, two sessions is address ownership and keeps its own reason code, not "moved".
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

// Three attachments of one device must collapse to the newest in a single pass.
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
