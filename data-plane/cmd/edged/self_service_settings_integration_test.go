//go:build integration

package main

import (
	"context"
	"testing"
)

// EMAIL ACCOUNTS against a real database (migration 0117), guarded like every edged API+PG test by
// PHASE3_TEST_DSN: the settings' defaults, bounds, step-up, change log and audit; the accounts list's verification
// state; and the staff password refusal for a self-service account.

func selfServiceBody(f *apiFixture, over map[string]any) map[string]any {
	m := map[string]any{
		"verification_required": true, "verification_deadline_hours": 48, "suspension_policy": "SUSPEND",
		"reminder_hours_before": 6, "resend_cooldown_seconds": 120, "resend_max_per_day": 4, "grace_minutes": 30,
		"max_grace_extensions": 2, "registrations_per_device_per_day": 2, "reason": "conference week",
		"password": f.password,
	}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

func TestIntegration_SelfServiceSettings_DefaultsStepUpHistoryAndAudit(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()

	st, body := f.do(t, "GET", "/self-service-settings/", nil)
	set, _ := body["settings"].(map[string]any)
	if st != 200 || set["verification_deadline_hours"] != float64(24) || set["suspension_policy"] != "SUSPEND" ||
		set["grace_minutes"] != float64(60) || body["config_version"] != float64(0) {
		t.Fatalf("defaults = %d %v", st, body)
	}

	for name, bad := range map[string]map[string]any{
		"deadline 0":    selfServiceBody(f, map[string]any{"verification_deadline_hours": 0}),
		"grace 241":     selfServiceBody(f, map[string]any{"grace_minutes": 241}),
		"reminder late": selfServiceBody(f, map[string]any{"reminder_hours_before": 48}),
		"no reason":     selfServiceBody(f, map[string]any{"reason": nil}),
	} {
		if st, b := f.do(t, "PUT", "/self-service-settings/", bad); st != 400 {
			t.Fatalf("%s accepted: %d %v", name, st, b)
		}
	}
	if st, b := f.do(t, "PUT", "/self-service-settings/", selfServiceBody(f, map[string]any{"password": "wrong"})); st != 401 {
		t.Fatalf("a wrong step-up password saved: %d %v", st, b)
	}

	// The setter enforces the bounds itself, for a caller that skipped edged.
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.self_service_settings_set($1,$2,true,24,'SUSPEND',4,60,5,60,4,3,'x','bounds test')`,
		f.tenant, f.site); err == nil {
		t.Fatal("the SQL setter stored max_grace_extensions=4, above the hard maximum of 3")
	}

	st, body = f.do(t, "PUT", "/self-service-settings/", selfServiceBody(f, nil))
	if st != 200 || body["config_version"] != float64(1) {
		t.Fatalf("save = %d %v", st, body)
	}
	st, body = f.do(t, "GET", "/self-service-settings/", nil)
	set, _ = body["settings"].(map[string]any)
	if st != 200 || set["verification_deadline_hours"] != float64(48) || set["grace_minutes"] != float64(30) {
		t.Fatalf("after save = %v", body)
	}
	ch, _ := body["changes"].([]any)
	if len(ch) != 1 {
		t.Fatalf("changes = %v", body["changes"])
	}
	if c := ch[0].(map[string]any); c["change_reason"] != "conference week" {
		t.Fatalf("change row = %v", c)
	}
	var audited int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_log
		WHERE action='self_service_settings.changed' AND payload->>'reason'='conference week' AND actor_id=$1`,
		f.operator).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows = %d (%v)", audited, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.self_service_settings_changes SET change_reason='x'
		WHERE tenant_id=$1`, f.tenant); err == nil {
		t.Fatal("the change history is not append-only")
	}
}

func TestIntegration_SelfServiceSettings_ReadOnlyRoleCannotChangeThem(t *testing.T) {
	f := newAPI(t, "front_office_operator")
	if st, b := f.do(t, "GET", "/self-service-settings/", nil); st != 200 {
		t.Fatalf("read = %d %v", st, b)
	}
	if st, b := f.do(t, "PUT", "/self-service-settings/", selfServiceBody(f, nil)); st != 403 {
		t.Fatalf("the desk changed the settings: %d %v", st, b)
	}
}

func TestIntegration_SelfServiceAccounts_ListStateAndPasswordRefusal(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	var suspended, fresh, staff string
	for _, row := range []struct {
		dst  *string
		user string
		sql  string
	}{
		{&suspended, "late@example.com", `true, NULL, now() - interval '1 hour'`},
		{&fresh, "new@example.com", `true, NULL, now() + interval '20 hours'`},
		{&staff, "room-desk", `false, NULL, NULL`},
	} {
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
			(tenant_id, site_id, username, password_hash, self_registered, email_verified_at, verification_deadline)
			VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash', `+row.sql+`) RETURNING id::text`,
			f.tenant, f.site, row.user).Scan(row.dst); err != nil {
			t.Fatalf("seed %s: %v", row.user, err)
		}
	}
	st, body := f.getAccounts(t, "", "")
	if st != 200 {
		t.Fatalf("list = %d %v", st, body)
	}
	byID := map[string]map[string]any{}
	for _, x := range body["data"].([]any) {
		m := x.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if a := byID[suspended]; a["self_registered"] != true || a["email_verified"] != false || a["suspended"] != true {
		t.Fatalf("past-deadline account = %v", a)
	}
	if a := byID[fresh]; a["suspended"] != false || a["verification_deadline"] == nil {
		t.Fatalf("fresh account = %v", a)
	}
	if a := byID[staff]; a["self_registered"] != false || a["suspended"] != false {
		t.Fatalf("staff account = %v", a)
	}

	if st, b := f.do(t, "POST", "/guest-accounts/"+suspended+"/set-password", map[string]any{"generate": true}); st != 403 ||
		b["error"] != "self_service_account" {
		t.Fatalf("staff re-passworded a self-service account: %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/guest-accounts/"+staff+"/set-password", map[string]any{"password": "desk-pass-1"}); st != 200 {
		t.Fatalf("a staff-created account could not be re-passworded: %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/guest-accounts/"+staff+"/resend-verification", nil); st != 409 {
		t.Fatalf("resend for a staff account = %d %v", st, b)
	}
}
