package iamv2

import "testing"

func allocPlanSpec(mode string, down, up *int) PlanPublishSpec {
	return PlanPublishSpec{
		TenantID: "t", SiteID: "s", PlanCode: "plan-a", Name: "Plan A",
		DownKbps: down, UpKbps: up, MaxConcurrentDevices: 3,
		SpeedAllocation: mode,
	}
}

func allocKbps(v int) *int { return &v }

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

func TestPlanPublish_UnknownSpeedAllocationIsRefused(t *testing.T) {
	spec := allocPlanSpec("PER_ROOM", allocKbps(20000), allocKbps(5000))
	if err := validatePlanSpec(&spec, false); err == nil {
		t.Fatal("an unrecognised speed_allocation was published")
	}
}

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
	// Absent rates mean unlimited under PER_DEVICE.
	spec := allocPlanSpec("PER_DEVICE", nil, nil)
	if err := validatePlanSpec(&spec, false); err != nil {
		t.Fatalf("an unlimited per-device plan was refused: %v", err)
	}
}
