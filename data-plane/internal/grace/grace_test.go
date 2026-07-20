package grace

import "testing"

func cfg() Config {
	return Config{EligibilityWindowSeconds: 86400, GraceDurationSeconds: 3600, Policy: "REJECT_NEW_DEVICE",
		GraceDownKbps: 2000, GraceUpKbps: 1000, GraceDataQuotaBytes: 1 << 30}
}

// F4 grandfathering: an existing device retains bounded access within the checkout-grace window (with grace
// shaping); a new device is rejected.
func TestF4_Grandfathering(t *testing.T) {
	// existing device, 10 min after checkout, grace window 60 min → GRACE allow + shaping
	d := Decide(cfg(), Request{CheckedOut: true, CheckoutAgeSeconds: 600, DeviceIsExisting: true}, 0)
	if !d.Allow || d.Mode != ModeGrace || d.DownKbps != 2000 {
		t.Fatalf("existing device in grace: %+v, want GRACE allow + shaping", d)
	}
	// new device during grace → rejected (REJECT_NEW_DEVICE)
	d = Decide(cfg(), Request{CheckedOut: true, CheckoutAgeSeconds: 600, DeviceIsExisting: false}, 0)
	if d.Allow || d.Reason != "GRACE_NEW_DEVICE_REJECTED" {
		t.Fatalf("new device in grace: %+v, want DENIED (reject new)", d)
	}
}

// F5 eligibility/grace window: after the grace window elapses, even an existing device is denied.
func TestF5_WindowElapsed(t *testing.T) {
	d := Decide(cfg(), Request{CheckedOut: true, CheckoutAgeSeconds: 3601, DeviceIsExisting: true}, 0)
	if d.Allow || d.Reason != "GRACE_WINDOW_ELAPSED" {
		t.Fatalf("after window: %+v, want DENIED/GRACE_WINDOW_ELAPSED", d)
	}
	// grace disabled (duration 0): checkout ends access immediately
	c := cfg()
	c.GraceDurationSeconds = 0
	d = Decide(c, Request{CheckedOut: true, CheckoutAgeSeconds: 5, DeviceIsExisting: true}, 0)
	if d.Allow || d.Reason != "GRACE_DISABLED" {
		t.Fatalf("grace disabled: %+v, want DENIED/GRACE_DISABLED", d)
	}
}

// F6 emergency fallback: during a cloud outage the appliance serves an EXISTING device locally (bounded), but
// still rejects a NEW device; once the emergency bound elapses, access ends.
func TestF6_EmergencyFallback(t *testing.T) {
	// existing device, outage 2 min, emergency bound 30 min → EMERGENCY allow (even if checked out)
	d := Decide(cfg(), Request{CheckedOut: true, CheckoutAgeSeconds: 99999, DeviceIsExisting: true, CloudOutage: true, OutageAgeSeconds: 120}, 1800)
	if !d.Allow || d.Mode != ModeEmergency {
		t.Fatalf("emergency existing device: %+v, want EMERGENCY allow", d)
	}
	// new device during outage → rejected
	d = Decide(cfg(), Request{DeviceIsExisting: false, CloudOutage: true, OutageAgeSeconds: 120}, 1800)
	if d.Allow || d.Reason != "EMERGENCY_NEW_DEVICE_REJECTED" {
		t.Fatalf("emergency new device: %+v, want DENIED", d)
	}
	// outage beyond the emergency bound → denied
	d = Decide(cfg(), Request{DeviceIsExisting: true, CloudOutage: true, OutageAgeSeconds: 4000}, 1800)
	if d.Allow || d.Reason != "EMERGENCY_GRACE_EXPIRED" {
		t.Fatalf("emergency expired: %+v, want DENIED", d)
	}
}

// in-house (not checked out) → NORMAL full access regardless of grace config.
func TestInHouseNormal(t *testing.T) {
	d := Decide(cfg(), Request{CheckedOut: false, DeviceIsExisting: true}, 1800)
	if !d.Allow || d.Mode != ModeNormal {
		t.Fatalf("in-house: %+v, want NORMAL allow", d)
	}
}

// determinism: identical (config, request) → identical decision.
func TestGraceDeterministic(t *testing.T) {
	r := Request{CheckedOut: true, CheckoutAgeSeconds: 600, DeviceIsExisting: true}
	if Decide(cfg(), r, 1800) != Decide(cfg(), r, 1800) {
		t.Fatal("Decide must be deterministic")
	}
}
