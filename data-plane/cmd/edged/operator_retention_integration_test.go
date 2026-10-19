//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// GDPR audit B4 (migration 0138): a disabled staff account is anonymised once it has been disabled for longer
// than the appliance's setting; the setting is bounded, needs a reason and the password, and is audited; an
// anonymised account cannot be enabled again; and staff addresses kept outside the activity log are cleared
// once older than its period.
func TestIntegration_StaffRetention_AnonymisesDisabledAccounts(t *testing.T) {
	admin := newSigninAPI(t, "site_admin")
	ctx := context.Background()
	ip := testIP()

	// The setting.
	if r := admin.call(t, "GET", "/operators/account-retention", ip, admin.sessTok, nil, nil); r.code != 200 {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "PUT", "/operators/account-retention", ip, admin.sessTok,
		map[string]any{"disabled_account_days": 10, "reason": "too short", "password": admin.password}, nil); r.code != 400 {
		t.Fatalf("a period below 30 days was accepted: %d", r.code)
	}
	if r := admin.call(t, "PUT", "/operators/account-retention", ip, admin.sessTok,
		map[string]any{"disabled_account_days": 90, "reason": "staff policy", "password": "not-it"}, nil); r.code != 401 {
		t.Fatalf("a wrong password changed the period: %d", r.code)
	}
	if r := admin.call(t, "PUT", "/operators/account-retention", ip, admin.sessTok,
		map[string]any{"disabled_account_days": 90, "reason": "staff policy", "password": admin.password}, nil); r.code != 200 {
		t.Fatalf("set = %d %s", r.code, r.raw)
	}
	t.Cleanup(func() {
		_, _ = admin.pool.Exec(context.Background(), `SELECT public.operator_account_retention_set(730,'test','restore the default')`)
	})

	// Two colleagues: one disabled 100 days ago, one 10 days ago.
	sfx := fmt.Sprint(time.Now().UnixNano())
	seed := func(email string) string {
		var id string
		if err := admin.pool.QueryRow(ctx, `INSERT INTO public.operators(email,display_name,password_hash,status)
			VALUES ($1,'Grace','$argon2id$fixture',$2) RETURNING id::text`, email, "active").Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES (now(),$1,'operator','op-1','operator.created','operator',$2,jsonb_build_object('email',$3::text,'role','site_viewer'))`,
			admin.tenant, id, email); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldEmail, newEmail := "old-"+sfx+"@hotel.test", "new-"+sfx+"@hotel.test"
	old, recent := seed(oldEmail), seed(newEmail)
	if _, err := admin.pool.Exec(ctx, `UPDATE public.operators SET status='disabled' WHERE id = ANY($1::uuid[])`, []string{old, recent}); err != nil {
		t.Fatal(err)
	}
	var stamped bool
	_ = admin.pool.QueryRow(ctx, `SELECT disabled_at > now() - interval '1 minute' FROM public.operators WHERE id=$1`, old).Scan(&stamped)
	if !stamped {
		t.Fatal("disabling did not record when")
	}
	if _, err := admin.pool.Exec(ctx, `UPDATE public.operators SET disabled_at = now() - interval '100 days' WHERE id=$1;`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.pool.Exec(ctx, `UPDATE public.operators SET disabled_at = now() - interval '10 days' WHERE id=$1`, recent); err != nil {
		t.Fatal(err)
	}
	// A staff address on a network change three years ago, and one from today.
	var oldNet, newNet string
	for _, c := range []struct {
		dst *string
		ago string
	}{{&oldNet, "3 years"}, {&newNet, "0 days"}} {
		if err := admin.pool.QueryRow(ctx, `INSERT INTO public.system_network_audit(actor,source_ip,action,target,created_at)
			VALUES ('op','10.9.9.9','apply','lan',now() - $1::interval) RETURNING id::text`, c.ago).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}

	var out map[string]int
	if err := admin.pool.QueryRow(ctx, `SELECT public.run_staff_retention($1::uuid,$2::uuid)`, admin.tenant, admin.site).Scan(&out); err != nil {
		t.Fatal(err)
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var s *string
		if err := admin.pool.QueryRow(ctx, q, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if s == nil {
			return "<null>"
		}
		return *s
	}
	if got := str(`SELECT email || coalesce(display_name,'') || coalesce(password_hash,'') FROM public.operators WHERE id=$1`, old); got != "removed-"+old[0:8]+old[9:13]+"@staff.invalid" {
		t.Errorf("old account = %q", got)
	}
	if got := str(`SELECT email FROM public.operators WHERE id=$1`, recent); got != newEmail {
		t.Errorf("an account inside the period was anonymised: %q", got)
	}
	if got := str(`SELECT payload::text FROM public.audit_log WHERE target_id=$1 AND action='operator.created'`, old); got != `{"role": "site_viewer"}` {
		t.Errorf("the activity log still names the old account: %s", got)
	}
	if got := str(`SELECT payload->>'email' FROM public.audit_log WHERE target_id=$1 AND action='operator.created'`, recent); got != newEmail {
		t.Errorf("the recent account's entry changed: %s", got)
	}
	if got := str(`SELECT coalesce(source_ip,'-') FROM public.system_network_audit WHERE id=$1::uuid`, oldNet); got != "-" {
		t.Errorf("a three-year-old staff address was kept: %s", got)
	}
	if got := str(`SELECT source_ip FROM public.system_network_audit WHERE id=$1::uuid`, newNet); got != "10.9.9.9" {
		t.Errorf("today's staff address was cleared: %s", got)
	}
	if _, err := admin.pool.Exec(ctx, `UPDATE public.operators SET status='active' WHERE id=$1`, old); err == nil {
		t.Fatal("an anonymised account was enabled again")
	}
}
