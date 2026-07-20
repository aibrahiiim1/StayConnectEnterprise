package grace

import "testing"

func adminPolicy() Policy {
	return Policy{DurationSeconds: 3600, DownKbps: 3000, UpKbps: 1500, DataQuotaBytes: 1 << 30,
		DeviceLimit: 3, DeviceLimitPolicy: DeviceLimitRejectNew}
}

// F5 eligibility: EVERY checked-out Stay with an active valid Entitlement at the boundary is converted,
// regardless of the prior package's origin/price, and regardless of recent device usage. No active
// entitlement ⇒ no Grace. Repeated checkout in the same episode ⇒ no duplicate.
func TestF5_EligibilityByActiveEntitlement(t *testing.T) {
	base := ConversionRequest{HasActiveEntitlementAtCheckout: true, Configured: adminPolicy(), ConfiguredValid: true}

	// active entitlement (any origin: FREE / PAID fixture / Stay-based / other) → Grace created. The core
	// takes NO origin input, so origin cannot change the result — that is the guarantee.
	if d := DecideConversion(base); !d.Create || d.Trigger != TriggerCheckoutGrace {
		t.Fatalf("active entitlement → Grace created, got %+v", d)
	}
	// active entitlement with ZERO recent usage → still created (usage is not an input).
	if d := DecideConversion(base); !d.Create {
		t.Fatal("zero recent usage must NOT block Grace when an entitlement is active")
	}
	// no active entitlement → no Grace.
	if d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: false, ConfiguredValid: true, Configured: adminPolicy()}); d.Create || d.Reason != "NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT" {
		t.Fatalf("no active entitlement → no Grace, got %+v", d)
	}
	// repeated checkout in the SAME episode → no duplicate conversion.
	dup := base
	dup.AlreadyConvertedThisEpisode = true
	if d := DecideConversion(dup); d.Create || d.Reason != "ALREADY_CONVERTED_THIS_EPISODE" {
		t.Fatalf("duplicate episode → no new conversion, got %+v", d)
	}
}

// The pinned policy is the Admin policy when valid; the resulting Grace lifetime is grace_duration_seconds.
func TestF5_PinnedPolicyAndDuration(t *testing.T) {
	d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: true, Configured: adminPolicy(), ConfiguredValid: true})
	if d.Policy.DurationSeconds != 3600 || d.Policy.DownKbps != 3000 || d.IsEmergency {
		t.Fatalf("pinned Admin policy wrong: %+v", d.Policy)
	}
}

// F6 emergency: when the configured policy is invalid/unavailable at conversion, an eligible Guest is still
// converted using the VERSIONED built-in Emergency policy (60m/5Mbps/2Mbps/500MB/REJECT_NEW_DEVICE) with the
// EMERGENCY_GRACE trigger + config-invalid alert. A non-eligible Guest is still not converted.
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
	// invalid config but NO active entitlement → still no Grace.
	if d := DecideConversion(ConversionRequest{HasActiveEntitlementAtCheckout: false, ConfiguredValid: false}); d.Create {
		t.Fatalf("invalid config + no entitlement → no Grace, got %+v", d)
	}
}

// ValidatePolicy is the server-side boundary: disabled / no pinned revision / malformed → invalid.
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

// Grace access during the window: existing device grandfathered; window elapsed → denied; new device rejected
// at/above the limit, admitted below it (REJECT_NEW_DEVICE); shaping applies.
func TestGraceAccess(t *testing.T) {
	p := adminPolicy()
	// existing device, within window → allowed + grace shaping
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 600, DeviceIsExisting: true, ActiveDeviceCount: 5}); !a.Allow || a.DownKbps != 3000 {
		t.Fatalf("existing device in window: %+v", a)
	}
	// window elapsed → denied even for existing device
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 3601, DeviceIsExisting: true}); a.Allow || a.Reason != "GRACE_WINDOW_ELAPSED" {
		t.Fatalf("elapsed: %+v", a)
	}
	// new device at/above limit → rejected
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 100, DeviceIsExisting: false, ActiveDeviceCount: 3}); a.Allow {
		t.Fatalf("new device at limit must be rejected: %+v", a)
	}
	// new device below limit → admitted under REJECT_NEW_DEVICE
	if a := DecideAccess(AccessRequest{Policy: p, GraceAgeSeconds: 100, DeviceIsExisting: false, ActiveDeviceCount: 1}); !a.Allow {
		t.Fatalf("new device below limit may be admitted: %+v", a)
	}
}

func TestGraceDeterministic(t *testing.T) {
	r := ConversionRequest{HasActiveEntitlementAtCheckout: true, Configured: adminPolicy(), ConfiguredValid: true}
	if DecideConversion(r) != DecideConversion(r) {
		t.Fatal("DecideConversion must be deterministic")
	}
}
