package main

// SHARED puts one aggregate ceiling on an Entitlement and its devices borrow from it; it is not a division, so one active device gets the whole ceiling. PER_DEVICE is the default.

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func allocSession(id, ip, ent, mode string) shapeplan.Session {
	return shapeplan.Session{SessionID: id, DeviceID: "dev-" + id, IP: ip, MAC: allocMAC(ip),
		Bridge: "br-guest", EntitlementID: ent, SpeedAllocation: mode,
		DownKbps: 20000, UpKbps: 5000, Entitled: true}
}

func allocMAC(ip string) string { return "aa:bb:cc:00:00:" + ip[len(ip)-2:] }

// allocWriter's DHCP evidence confirms every address, since unconfirmed ownership never reaches class construction.
func allocWriter(t *testing.T, tc *fakeTC, g *fakeGate, ips ...string) *phase3Shaping {
	t.Helper()
	rows := make([]KeaLease, 0, len(ips))
	for _, ip := range ips {
		rows = append(rows, lease(ip, allocMAC(ip)))
	}
	p := liveWriter(tc)
	p.gate = g
	p.owner = addressOwner{src: fakeLeases{rows: rows}}
	return p
}

// PER_DEVICE: no group is created and each session hangs off the root at its own rate.
func TestSpeedAllocation_PerDeviceKeepsIndependentRates(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")

	res, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "PER_DEVICE"),
		allocSession("b", "10.0.0.12", "ent-1", "PER_DEVICE")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 2 || res.Failed != 0 {
		t.Fatalf("per-device shaping failed: %+v", res)
	}
	if _, ok := tc.groupRate("br-guest", shape.GroupClassid("ent-1")); ok {
		t.Fatal("PER_DEVICE created an aggregate group class")
	}
	for _, ip := range []string{"10.0.0.11", "10.0.0.12"} {
		if got := tc.parentOf("br-guest", ip); got != shape.RootParent {
			t.Fatalf("%s hangs under %q, want the root — a per-device class shares nothing", ip, got)
		}
	}
}

// An empty mode is PER_DEVICE; older revisions produce one and must not become SHARED.
func TestSpeedAllocation_UnsetModeIsPerDevice(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11")

	if _, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "")), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := tc.groupRate("br-guest", shape.GroupClassid("ent-1")); ok {
		t.Fatal("an unset allocation mode was treated as SHARED")
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != shape.RootParent {
		t.Fatalf("parent = %q, want the root", got)
	}
}

// SHARED: one group class per Entitlement, on both the download bridge and the upload IFB.
func TestSpeedAllocation_SharedBuildsOneCeilingForTheEntitlement(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")

	res, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "SHARED"),
		allocSession("b", "10.0.0.12", "ent-1", "SHARED")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 2 || res.Failed != 0 {
		t.Fatalf("shared shaping failed: %+v", res)
	}
	group := shape.GroupClassid("ent-1")
	down, ok := tc.groupRate("br-guest", group)
	if !ok || down != 20000 {
		t.Fatalf("download group rate = %d (present=%v), want the plan's 20000", down, ok)
	}
	up, ok := tc.groupRate(shape.IFBName("br-guest"), group)
	if !ok || up != 5000 {
		t.Fatalf("upload group rate = %d (present=%v), want the plan's 5000", up, ok)
	}
	for _, ip := range []string{"10.0.0.11", "10.0.0.12"} {
		if got := tc.parentOf("br-guest", ip); got != group {
			t.Fatalf("%s hangs under %q, want the entitlement's group %q", ip, got, group)
		}
	}
}

// Separate entitlements get separate ceilings.
func TestSpeedAllocation_SeparateEntitlementsGetSeparateCeilings(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")

	if _, err := p.submit(context.Background(), envelope(1,
		allocSession("a", "10.0.0.11", "ent-1", "SHARED"),
		allocSession("b", "10.0.0.12", "ent-2", "SHARED")), time.Now()); err != nil {
		t.Fatal(err)
	}
	g1, g2 := shape.GroupClassid("ent-1"), shape.GroupClassid("ent-2")
	if g1 == g2 {
		t.Fatal("two entitlements hashed to one group class")
	}
	if tc.parentOf("br-guest", "10.0.0.11") != g1 || tc.parentOf("br-guest", "10.0.0.12") != g2 {
		t.Fatal("sessions were not placed under their own entitlement's ceiling")
	}
}

// The group is created once and re-rated, never replaced: replacing resets counters and drops the ceiling.
func TestSpeedAllocation_SharedGroupIsIdempotentAcrossPasses(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-1", "SHARED")

	for gen := int64(1); gen <= 3; gen++ {
		if _, err := p.submit(context.Background(), envelope(gen, s), time.Now()); err != nil {
			t.Fatalf("pass %d: %v", gen, err)
		}
	}
	if rate, ok := tc.groupRate("br-guest", shape.GroupClassid("ent-1")); !ok || rate != 20000 {
		t.Fatalf("group rate after three passes = %d (present=%v)", rate, ok)
	}
}

// A SHARED session with no entitlement to group by is refused rather than given its own full rate.
func TestSpeedAllocation_SharedWithoutAnEntitlementIsRefused(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "", "SHARED")

	res, err := p.submit(context.Background(), envelope(1, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || res.Failed == 0 {
		t.Fatalf("a SHARED session with no entitlement was shaped anyway: %+v", res)
	}
	if g.isAuthorized("br-guest", "10.0.0.11") {
		t.Fatal("it was authorized despite having no accountable ceiling")
	}
}

// The floor is a floor, not a slice: it stays well below the ceiling so HTB lends the rest.
func TestSpeedAllocation_TheSharedFloorIsNotADivision(t *testing.T) {
	for _, tc := range []struct{ group, wantMax int }{
		{group: 20000, wantMax: 20000},
		{group: 1000, wantMax: 1000},
		{group: 50, wantMax: 50},
	} {
		floor := shape.SharedFloorKbpsForTest(tc.group)
		if floor > tc.wantMax {
			t.Fatalf("group %d: floor %d exceeds the ceiling", tc.group, floor)
		}
		if tc.group > 100 && floor >= tc.group {
			t.Fatalf("group %d: floor %d leaves nothing to borrow — that is a division, not sharing",
				tc.group, floor)
		}
	}
}
