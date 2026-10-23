package iamv2

import "testing"

func planSpec(mode string, quota *int64) PlanPublishSpec {
	down, up := 1000, 1000
	return PlanPublishSpec{
		Name: "p", DownKbps: &down, UpKbps: &up, MaxConcurrentDevices: 2,
		TimeAccountingMode: mode, TimeQuotaSeconds: quota,
	}
}

func TestOmittedTimeModeIsValidityWindow(t *testing.T) {
	for _, allow := range []bool{false, true} {
		spec := planSpec("", nil)
		if err := validatePlanSpec(&spec, allow); err != nil {
			t.Fatalf("capability=%v: a spec with no mode was refused: %v", allow, err)
		}
		if spec.TimeAccountingMode != "VALIDITY_WINDOW" {
			t.Fatalf("capability=%v: an omitted mode became %q", allow, spec.TimeAccountingMode)
		}
	}
}

func TestAggregateModeNeedsTheCapability(t *testing.T) {
	quota := int64(3600)
	spec := planSpec("AGGREGATE_ONLINE_TIME", &quota)
	if err := validatePlanSpec(&spec, false); err == nil {
		t.Fatal("AGGREGATE_ONLINE_TIME was published while the capability was OFF")
	}
	spec = planSpec("AGGREGATE_ONLINE_TIME", &quota)
	if err := validatePlanSpec(&spec, true); err != nil {
		t.Fatalf("AGGREGATE_ONLINE_TIME was refused with the capability ON: %v", err)
	}
}

// Without a budget the entitlement could never exhaust.
func TestAggregateModeRequiresABudget(t *testing.T) {
	for name, quota := range map[string]*int64{"absent": nil, "zero": ptr64(0), "negative": ptr64(-1)} {
		spec := planSpec("AGGREGATE_ONLINE_TIME", quota)
		if err := validatePlanSpec(&spec, true); err == nil {
			t.Fatalf("a %s budget was accepted for an aggregate revision", name)
		}
	}
}

func TestUnknownTimeModeIsRefused(t *testing.T) {
	spec := planSpec("SOMETHING_ELSE", nil)
	if err := validatePlanSpec(&spec, true); err == nil {
		t.Fatal("an unknown accounting mode was accepted")
	}
}

func TestATierCannotOverrideTheAccountingMode(t *testing.T) {
	plan := PlanRevisionRow{DownKbps: 1000, UpKbps: 1000, MaxConcurrentDevices: 2,
		TimeAccountingMode: "VALIDITY_WINDOW"}
	pkg := PackageRevisionRow{}
	tier := GrantTier{Value: map[string]any{"time_accounting_mode": "AGGREGATE_ONLINE_TIME"}}
	if _, err := BuildGrantSnapshot(tier, plan, pkg); err == nil {
		t.Fatal("a tier override changed the accounting mode of a published revision")
	}
}

func TestAnAggregateRevisionIsGrantedUnderItsOwnMode(t *testing.T) {
	plan := PlanRevisionRow{DownKbps: 1000, UpKbps: 1000, MaxConcurrentDevices: 2,
		TimeAccountingMode: "AGGREGATE_ONLINE_TIME"}
	got, err := BuildGrantSnapshot(GrantTier{}, plan, PackageRevisionRow{})
	if err != nil {
		t.Fatalf("an aggregate revision could not be granted: %v", err)
	}
	if got.TimeAccountingMode != "AGGREGATE_ONLINE_TIME" {
		t.Fatalf("the grant snapshot says %q", got.TimeAccountingMode)
	}
	plan.TimeAccountingMode = "VALIDITY_WINDOW"
	got, err = BuildGrantSnapshot(GrantTier{}, plan, PackageRevisionRow{})
	if err != nil || got.TimeAccountingMode != "VALIDITY_WINDOW" {
		t.Fatalf("a validity-window revision came back as %q (%v)", got.TimeAccountingMode, err)
	}
}

func ptr64(v int64) *int64 { return &v }
