//go:build integration

package main

// AFTER CHECK-OUT ACCESS API (Product Owner, 2026-10-30): the one configuration surface, in hours, with the enable switch,
// operator bounds, step-up, reason, optimistic version and history. Real PostgreSQL, the real publication boundary.

import (
	"context"
	"strings"
	"testing"
)

func acPolicy(expected int, hours, down, up int, quota int64, devices int) map[string]any {
	return map[string]any{
		"enabled": true, "duration_hours": hours, "down_kbps": down, "up_kbps": up,
		"data_quota_bytes": quota, "device_limit": devices,
		"expected_config_version": expected, "reason_code": "AC_TEST", "password": "operator-step-up-pw",
	}
}

func TestIntegration_API_AfterCheckoutAccess_OneSurfaceWithBoundsSwitchAndHistory(t *testing.T) {
	f := newAPI(t)

	// Nothing published: enabled by default (today's behaviour is a grant at every checkout), the built-in fallback applies,
	// and the bounds the console must enforce are served.
	status, body := f.do(t, "GET", "/after-checkout-access", nil)
	if status != 200 || body["enabled"] != true || body["configured"] != false || body["config_version"].(float64) != 0 {
		t.Fatalf("unconfigured GET: %d %v", status, body)
	}
	if eff := body["effective"].(map[string]any); eff["source"] != "EMERGENCY_FALLBACK" {
		t.Fatalf("effective = %v", eff)
	}
	bounds := body["bounds"].(map[string]any)
	if d := bounds["duration_hours"].(map[string]any); d["min"].(float64) != 1 || d["max"].(float64) != 72 || d["default"].(float64) != 12 {
		t.Fatalf("duration bounds = %v", d)
	}

	// Refusals: every one is a 400 before anything is written.
	good := func() map[string]any { return acPolicy(0, 12, 5000, 2000, 1_000_000_000, 5) }
	for name, mut := range map[string]func(m map[string]any){
		"no version":            func(m map[string]any) { delete(m, "expected_config_version") },
		"no reason":             func(m map[string]any) { delete(m, "reason_code") },
		"free-text reason":      func(m map[string]any) { m["reason_code"] = "because I said so" },
		"zero hours":            func(m map[string]any) { m["duration_hours"] = 0 },
		"73 hours":              func(m map[string]any) { m["duration_hours"] = 73 },
		"download below bound":  func(m map[string]any) { m["down_kbps"] = 63 },
		"upload above bound":    func(m map[string]any) { m["up_kbps"] = 1_000_001 },
		"quota below 10 MiB":    func(m map[string]any) { m["data_quota_bytes"] = 1000 },
		"quota above 100 GiB":   func(m map[string]any) { m["data_quota_bytes"] = int64(101) << 30 },
		"21 devices":            func(m map[string]any) { m["device_limit"] = 21 },
		"another device policy": func(m map[string]any) { m["device_policy"] = "DISCONNECT_OLDEST" },
		"partial policy":        func(m map[string]any) { delete(m, "data_quota_bytes") },
	} {
		m := good()
		mut(m)
		if status, body := f.do(t, "PUT", "/after-checkout-access", m); status != 400 {
			t.Errorf("%s: got %d %v, want 400", name, status, body)
		}
	}
	if status, _ := f.do(t, "PUT", "/after-checkout-access", map[string]any{"expected_config_version": 0, "reason_code": "AC_TEST", "password": "operator-step-up-pw"}); status != 400 {
		t.Errorf("a request naming nothing to change got %d, want 400", status)
	}
	wrongPw := good()
	wrongPw["password"] = "not-the-password"
	if status, _ := f.do(t, "PUT", "/after-checkout-access", wrongPw); status != 401 && status != 403 {
		t.Errorf("a wrong step-up password got %d, want 401/403", status)
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1`, f.tenant); n != 0 {
		t.Fatalf("a refused request created a configuration row")
	}

	// Publish: version 1, derived package accepted by the checkout validator, GET in hours.
	status, body = f.do(t, "PUT", "/after-checkout-access", good())
	if status != 200 || body["config_version"].(float64) != 1 {
		t.Fatalf("publish: %d %v", status, body)
	}
	status, body = f.do(t, "GET", "/after-checkout-access", nil)
	pol := body["policy"].(map[string]any)
	if status != 200 || body["configured"] != true || pol["duration_hours"].(float64) != 12 || pol["duration_seconds"].(float64) != 43200 ||
		pol["down_kbps"].(float64) != 5000 || pol["data_quota_bytes"].(float64) != 1e9 || pol["device_limit"].(float64) != 5 || pol["device_policy"] != "REJECT_NEW_DEVICE" {
		t.Fatalf("GET after publish: %d %v", status, body)
	}
	var rev string
	if err := f.pool.QueryRow(context.Background(), `SELECT grace_package_revision_id::text FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1`, f.tenant).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if got := f.mismatchReason(t, rev, 43200, 5000, 2000, 1_000_000_000, 5, "REJECT_NEW_DEVICE"); got != "" {
		t.Fatalf("the derived package is refused by the checkout validator: %s", got)
	}

	// A stale version is a 409, not an overwrite.
	if status, body := f.do(t, "PUT", "/after-checkout-access", acPolicy(0, 24, 5000, 2000, 1_000_000_000, 5)); status != 409 {
		t.Fatalf("stale version got %d %v, want 409", status, body)
	}

	// Switch off: one version, the policy is kept, the effective source says so, and switching off again is a no-op.
	status, body = f.do(t, "PUT", "/after-checkout-access", map[string]any{"enabled": false, "expected_config_version": 1, "reason_code": "AC_TEST", "password": "operator-step-up-pw"})
	if status != 200 || body["config_version"].(float64) != 2 {
		t.Fatalf("switch off: %d %v", status, body)
	}
	status, body = f.do(t, "GET", "/after-checkout-access", nil)
	if body["enabled"] != false || body["effective"].(map[string]any)["source"] != "DISABLED" || body["policy"] == nil {
		t.Fatalf("GET while off: %d %v", status, body)
	}
	status, body = f.do(t, "PUT", "/after-checkout-access", map[string]any{"enabled": false, "expected_config_version": 2, "reason_code": "AC_TEST", "password": "operator-step-up-pw"})
	if status != 200 || body["config_version"].(float64) != 2 {
		t.Fatalf("switching off twice moved the version: %d %v", status, body)
	}

	// One request may change the policy and the switch together; a new duration is a new version in the history.
	status, body = f.do(t, "PUT", "/after-checkout-access", acPolicy(2, 24, 6000, 2000, 2_000_000_000, 4))
	if status != 200 || body["config_version"].(float64) != 4 { // the policy is version 3, the switch is version 4
		t.Fatalf("policy change + switch on: %d %v (two versions: 3 and 4)", status, body)
	}
	status, body = f.do(t, "GET", "/after-checkout-access", nil)
	if body["enabled"] != true || body["policy"].(map[string]any)["duration_hours"].(float64) != 24 {
		t.Fatalf("GET after change: %d %v", status, body)
	}

	// History: newest first, every version with its actor, reason and the terms it put in force.
	status, body = f.do(t, "GET", "/after-checkout-access/history", nil)
	hist, _ := body["data"].([]any)
	if status != 200 || len(hist) != 4 {
		t.Fatalf("history: %d %v", status, body)
	}
	if first := hist[0].(map[string]any); first["config_version"].(float64) != 4 || first["reason_code"] != "AC_TEST" {
		t.Fatalf("newest history entry = %v", first)
	}
	if mid := hist[2].(map[string]any)["policy"].(map[string]any); mid["change"] != "AFTER_CHECKOUT_ENABLED" || mid["after_checkout_enabled"] != false {
		t.Fatalf("the switch is not in the history: %v", mid)
	}

	// The audit log names what changed and holds no guest data.
	var payload string
	if err := f.pool.QueryRow(context.Background(), `SELECT payload::text FROM public.audit_log WHERE tenant_id=$1 AND action='after_checkout_access.updated' ORDER BY ts DESC LIMIT 1`, f.tenant).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"duration_hours": 24`) || !strings.Contains(payload, `"config_version": 4`) {
		t.Fatalf("audit payload: %s", payload)
	}
}

// The signed-in-limits console no longer carries the retired post-stay window setting, and the database refuses it by name.
func TestIntegration_API_TheRetiredPostStayWindowSettingIsGone(t *testing.T) {
	f := newAPI(t)
	status, body := f.do(t, "GET", "/signin-capacity-settings", nil)
	if status != 200 {
		t.Fatalf("GET: %d %v", status, body)
	}
	if _, has := body["settings"].(map[string]any)["post_stay_signin_days"]; has {
		t.Fatalf("the retired setting is still served: %v", body["settings"])
	}
	if _, has := body["bounds"].(map[string]any)["post_stay_signin_days"]; has {
		t.Fatalf("the retired setting still has bounds: %v", body["bounds"])
	}
	status, body = f.do(t, "PUT", "/signin-capacity-settings", map[string]any{
		"settings": map[string]any{"post_stay_signin_days": 14}, "reason": "try the old setting", "password": "operator-step-up-pw"})
	if status != 400 {
		t.Fatalf("changing the retired setting got %d %v, want 400", status, body)
	}
}
