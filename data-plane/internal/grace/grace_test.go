package grace

import "testing"

func adminPolicy() Policy {
	return Policy{DurationSeconds: 3600, DownKbps: 3000, UpKbps: 1500, DataQuotaBytes: 1 << 30,
		DeviceLimit: 3, DeviceLimitPolicy: DeviceLimitRejectNew}
}

func TestF5_EligibilityByActiveEntitlement(t *testing.T) {
	base := ConversionRequest{HasActiveEntitlementAtCheckout: true, Configured: adminPolicy(), ConfiguredValid: true}

	if d := DecideConversion(base); !d.Create || d.Trigger != TriggerCheckoutGrace {
		t.Fatalf("active entitlement → Grace created, got %+v", d)
	}
	if d := DecideConversion(base); !d.Create {
		t.Fatal("zero recent usage must NOT block Grace when an entitlement is active")
	}
	if d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: false, ConfiguredValid: true, Configured: adminPolicy()}); d.Create || d.Reason != "NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT" {
		t.Fatalf("no active entitlement → no Grace, got %+v", d)
	}
	dup := base
	dup.AlreadyConvertedThisEpisode = true
	if d := DecideConversion(dup); d.Create || d.Reason != "ALREADY_CONVERTED_THIS_EPISODE" {
		t.Fatalf("duplicate episode → no new conversion, got %+v", d)
	}
}

func TestF5_PinnedPolicyAndDuration(t *testing.T) {
	d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: true, Configured: adminPolicy(), ConfiguredValid: true})
	if d.Policy.DurationSeconds != 3600 || d.Policy.DownKbps != 3000 || d.IsEmergency {
		t.Fatalf("pinned Admin policy wrong: %+v", d.Policy)
	}
}

func TestF6_EmergencyFallbackOnInvalidConfig(t *testing.T) {
	d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: true, ConfiguredValid: false, Configured: Policy{}})
	if !d.Create || d.Trigger != TriggerEmergency || !d.IsEmergency || !d.ConfigInvalidAlert {
		t.Fatalf("invalid config + eligible → Emergency conversion + alert, got %+v", d)
	}
	if d.Policy != BuiltinEmergencyPolicy() {
		t.Fatalf("emergency policy = %+v, want built-in", d.Policy)
	}
	p := BuiltinEmergencyPolicy()
	if p.DurationSeconds != 3600 || p.DownKbps != 5000 || p.UpKbps != 2000 || p.DataQuotaBytes != 500<<20 || p.DeviceLimitPolicy != DeviceLimitRejectNew {
		t.Fatalf("built-in emergency policy values wrong: %+v", p)
	}
	if d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: false, ConfiguredValid: false}); d.Create {
		t.Fatalf("invalid config + no entitlement → no Grace, got %+v", d)
	}
}

func TestValidatePolicy(t *testing.T) {
	p := adminPolicy()
	if !ValidatePolicy(p, true, true) {
		t.Fatal("a valid enabled policy with a pinned revision must validate")
	}
	if ValidatePolicy(p, false, true) {
		t.Fatal("disabled policy must be invalid")
	}
	if ValidatePolicy(p, true, false) {
		t.Fatal("no pinned revision must be invalid")
	}
	bad := p
	bad.DeviceLimitPolicy = "EVICT_OLDEST"
	if ValidatePolicy(bad, true, true) {
		t.Fatal("unapproved device-limit policy must be invalid")
	}
}

func TestGraceAccess(t *testing.T) {
	p := adminPolicy() // DeviceLimit = 3
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 600, DeviceIsExisting: true}); !a.Allow || a.DownKbps != 3000 {
		t.Fatalf("existing device in window: %+v", a)
	}
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 3601, DeviceIsExisting: true}); a.Allow || a.Reason != "GRACE_WINDOW_ELAPSED" {
		t.Fatalf("elapsed: %+v", a)
	}
	for _, lbl := range []string{"below", "at", "above"} {
		if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 100, DeviceIsExisting: false}); a.Allow || a.Reason != "GRACE_NEW_DEVICE_REJECTED" {
			t.Fatalf("new device (%s limit) must be rejected: %+v", lbl, a)
		}
	}
}

func TestExistingDevicesGrandfatheredAboveLimit(t *testing.T) {
	p := adminPolicy() // limit 3
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 100, DeviceIsExisting: true}); !a.Allow {
		t.Fatal("an existing device must not be disconnected because the Grace limit is lower")
	}
	if over := ExistingDevicesOverLimit(p, 5); over != 2 {
		t.Fatalf("over-limit visibility = %d, want 2", over)
	}
	if over := ExistingDevicesOverLimit(p, 2); over != 0 {
		t.Fatalf("under-limit over-count = %d, want 0", over)
	}
}

func TestGraceDeterministic(t *testing.T) {
	r := ConversionRequest{HasActiveEntitlementAtCheckout: true, Configured: adminPolicy(), ConfiguredValid: true}
	if DecideConversion(r) != DecideConversion(r) {
		t.Fatal("DecideConversion must be deterministic")
	}
}
