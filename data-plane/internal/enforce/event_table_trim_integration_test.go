//go:build integration

package enforce

import (
	"context"
	"testing"
)

// AD-49 (migration 0178): only rows the retention has already scrubbed, and whose expiry is older than the period,
// are removed.
func TestIntegration_EventTableTrim_RemovesOnlyScrubbedExpiredRows(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, _, _ := oldTerminatedUsage(t, p)
	old := `now() - interval '30 days'`
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := p.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// scrubbed + old (goes), live content + old (stays: not yet scrubbed), scrubbed + recent (stays)
	mustExec(`INSERT INTO public.auth_otps (tenant_id, channel, destination, code_hash, expires_at) VALUES
	   ($1,'email','anonymised','h1',`+old+`), ($1,'email','a@example.test','h2',`+old+`), ($1,'email','anonymised','h3',now())`, f.tenant)
	mustExec(`INSERT INTO public.pms_attempts (tenant_id, room_number, secondary_kind, success, attempted_at) VALUES
	   ($1,'anonymised','either',false,`+old+`), ($1,'101','either',false,`+old+`)`, f.tenant)
	// The state is this table's primary key and the live row is meant to survive the trim, so a fixed literal made the test pass
	// once per database. Per-fixture names (the tenant is new every run) keep it repeatable on a shared database.
	stateOld, stateLive := "w6b-old-"+f.tenant, "w6b-live-"+f.tenant
	mustExec(`INSERT INTO public.social_oauth_states (state, tenant_id, provider, redirect_uri, expires_at) VALUES
	   ($2, $1,'google','https://x.test/cb',`+old+`)`, f.tenant, stateOld)
	mustExec(`INSERT INTO public.social_oauth_states (state, tenant_id, provider, redirect_uri, expires_at, client_ip) VALUES
	   ($2, $1,'google','https://x.test/cb',`+old+`, '10.0.0.1')`, f.tenant, stateLive)

	rows, err := rolePool(t, "svc_scd").Query(ctx, `SELECT o_category, o_anonymised FROM public.run_event_table_trim($1,$2,100)`, f.tenant, f.site)
	if err != nil {
		t.Fatalf("trim as svc_scd: %v", err)
	}
	removed := 0
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			t.Fatal(err)
		}
		removed += n
	}
	rows.Close()
	if removed != 3 {
		t.Fatalf("removed %d rows, want 3 (one OTP, one attempt, one redirect state)", removed)
	}
	if c := count(t, p, `SELECT count(*) FROM public.auth_otps WHERE tenant_id=$1`, f.tenant); c != 2 {
		t.Fatalf("%d OTP rows left, want 2 (the unscrubbed one and the recent one)", c)
	}
	if c := count(t, p, `SELECT count(*) FROM public.pms_attempts WHERE tenant_id=$1 AND room_number='101'`, f.tenant); c != 1 {
		t.Fatal("an unscrubbed attempt was removed")
	}
	if c := count(t, p, `SELECT count(*) FROM public.social_oauth_states WHERE state=$1`, stateLive); c != 1 {
		t.Fatal("a redirect state still holding an address was removed")
	}
	if _, err := rolePool(t, "svc_acctd").Exec(ctx, `SELECT * FROM public.run_event_table_trim($1,$2,100)`, f.tenant, f.site); err == nil {
		t.Fatal("svc_acctd can run the trim")
	}
}
