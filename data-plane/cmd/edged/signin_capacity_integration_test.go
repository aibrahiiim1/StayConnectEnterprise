//go:build integration

package main

import "testing"

// AD-30/31/34/51/58: the sign-in limits are persisted, bounded, defaulted, permission-checked and audited settings.
func TestIntegration_SigninCapacitySettings(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "GET", "/signin-capacity-settings/", nil)
	set, _ := body["settings"].(map[string]any)
	if st != 200 || set["otp_device_hourly_cap"] != float64(5) || set["otp_destination_hourly_cap"] != float64(15) ||
		set["otp_code_lifetime_minutes"] != float64(10) || set["signin_ceiling_per_minute"] != nil ||
		set["portal_image_max_edge_px"] != float64(2560) || set["device_retention_days"] != float64(90) {
		t.Fatalf("defaults = %d %v", st, body)
	}
	if b, _ := body["bounds"].(map[string]any); b["signin_ceiling_per_minute"] == nil {
		t.Fatalf("no bounds served: %v", body)
	}
	put := func(settings map[string]any, reason, pw string) (int, map[string]any) {
		return f.do(t, "PUT", "/signin-capacity-settings/", map[string]any{"settings": settings, "reason": reason, "password": pw})
	}
	if st, _ := put(map[string]any{"signin_ceiling_per_minute": 599}, "below the floor", f.password); st != 400 {
		t.Fatalf("a ceiling below 600 a minute was accepted: %d", st)
	}
	if st, _ := put(map[string]any{"otp_device_hourly_cap": 21}, "too many", f.password); st != 400 {
		t.Fatalf("a device cap above its bound was accepted: %d", st)
	}
	if st, _ := put(map[string]any{"nonsense": 1}, "unknown", f.password); st != 400 {
		t.Fatalf("an unknown setting was accepted: %d", st)
	}
	if st, _ := put(map[string]any{"otp_device_hourly_cap": 3}, "tighter", "wrong"); st != 401 {
		t.Fatalf("a wrong step-up password saved the setting: %d", st)
	}
	// The destination ceiling may not sit below the device cap (a database constraint, reported as a refusal).
	if st, _ := put(map[string]any{"otp_device_hourly_cap": 10, "otp_destination_hourly_cap": 5}, "inverted", f.password); st != 400 {
		t.Fatalf("a ceiling below the per-device cap was accepted: %d", st)
	}
	st, body = put(map[string]any{"otp_device_hourly_cap": 3, "signin_ceiling_per_minute": 1500, "portal_image_quality": 70}, "busy season", f.password)
	if st != 200 || body["config_version"] != float64(1) {
		t.Fatalf("save = %d %v", st, body)
	}
	st, body = f.do(t, "GET", "/signin-capacity-settings/", nil)
	set, _ = body["settings"].(map[string]any)
	ch, _ := body["changes"].([]any)
	if set["otp_device_hourly_cap"] != float64(3) || set["signin_ceiling_per_minute"] != float64(1500) ||
		set["portal_image_quality"] != float64(70) || set["otp_destination_hourly_cap"] != float64(15) || len(ch) != 1 {
		t.Fatalf("after save = %v", body)
	}
	// Clearing the ceiling returns it to "follow the licence".
	if st, _ := put(map[string]any{"signin_ceiling_per_minute": nil}, "back to the licence", f.password); st != 200 {
		t.Fatalf("clearing the ceiling: %d", st)
	}
	_, body = f.do(t, "GET", "/signin-capacity-settings/", nil)
	if set, _ := body["settings"].(map[string]any); set["signin_ceiling_per_minute"] != nil {
		t.Fatalf("the ceiling was not cleared: %v", body)
	}
	desk := newAPIIn(t, f.tenant, "front_office_operator")
	if st, _ := desk.do(t, "PUT", "/signin-capacity-settings/", map[string]any{"settings": map[string]any{"otp_device_hourly_cap": 2}, "reason": "desk try", "password": desk.password}); st != 403 {
		t.Fatalf("the desk changed the setting: %d", st)
	}
}
