package iamv2

// THE OPTION HAS TO BE SELLABLE, NOT MERELY STORABLE.
//
// speed_allocation reached the schema and the edge in the previous round, which meant an operator could not
// actually choose it: the column existed and nothing could set it. These tests pin the authoring rules that
// make it a product — the default that protects every revision already sold, the refusal of a mode nobody
// implements, and the refusal of a SHARED plan with no rate to share.

import "testing"

func allocPlanSpec(mode string, down, up *int) PlanPublishSpec {
	return PlanPublishSpec{
		TenantID: "t", SiteID: "s", PlanCode: "plan-a", Name: "Plan A",
		DownKbps: down, UpKbps: up, MaxConcurrentDevices: 3,
		SpeedAllocation: mode,
	}
}

func allocKbps(v int) *int { return &v }

// AN UNSTATED MODE PUBLISHES PER_DEVICE. Every revision sold so far promised the rate per device, and an
// operator (or an older client) that says nothing must not silently change what a plan means.
func TestPlanPublish_UnstatedSpeedAllocationIsPerDevice(t *testing.T) {
	spec := allocPlanSpec("", allocKbps(20000), allocKbps(5000))
	if err := validatePlanSpec(&spec, false); err != nil {
		t.Fatalf("a spec with no allocation was refused: %v", err)
	}
	if spec.SpeedAllocation != "PER_DEVICE" {
		t.Fatalf("allocation = %q, want PER_DEVICE", spec.SpeedAllocation)
	}
}

func TestPlanPublish_SharedIsAccepted(t *testing.T) {
	spec := allocPlanSpec("SHARED", allocKbps(20000), allocKbps(5000))
	if err := validatePlanSpec(&spec, false); err != nil {
		t.Fatalf("SHARED was refused: %v", err)
	}
	if spec.SpeedAllocation != "SHARED" {
		t.Fatalf("allocation = %q, want SHARED", spec.SpeedAllocation)
	}
}

// AN UNKNOWN MODE IS REFUSED, NOT DEFAULTED. Defaulting it would hand a property that asked for one shared
// ceiling a full rate per guest, overspending its capacity with nothing failing.
func TestPlanPublish_UnknownSpeedAllocationIsRefused(t *testing.T) {
	spec := allocPlanSpec("PER_ROOM", allocKbps(20000), allocKbps(5000))
	if err := validatePlanSpec(&spec, false); err == nil {
		t.Fatal("an unrecognised speed_allocation was published")
	}
}

// A SHARED PLAN NEEDS A RATE TO SHARE. Without one there is no ceiling, and the applier would build the group
// at its unlimited fallback — the opposite of what choosing SHARED means.
func TestPlanPublish_SharedRequiresARateToShare(t *testing.T) {
	for _, tc := range []struct {
		name     string
		down, up *int
	}{
		{"no download", nil, allocKbps(5000)},
		{"no upload", allocKbps(20000), nil},
		{"zero download", allocKbps(0), allocKbps(5000)},
	} {
		spec := allocPlanSpec("SHARED", tc.down, tc.up)
		if err := validatePlanSpec(&spec, false); err == nil {
			t.Fatalf("%s: a SHARED plan with nothing to share was published", tc.name)
		}
	}
	// ...and the same plan is perfectly valid as PER_DEVICE, where an absent rate means unlimited.
	spec := allocPlanSpec("PER_DEVICE", nil, nil)
	if err := validatePlanSpec(&spec, false); err != nil {
		t.Fatalf("an unlimited per-device plan was refused: %v", err)
	}
}
