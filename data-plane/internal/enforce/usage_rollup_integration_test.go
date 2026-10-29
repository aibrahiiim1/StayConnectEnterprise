//go:build integration

package enforce

// AD-48 (migration 0240): per-minute and per-hour usage rolled up from the raw samples, and a purge that works in
// three tiers. This replaces TestIntegration_Retention_PurgesOnlyOldFinishedDetail, which encoded the superseded
// single-tier behaviour (raw samples kept for the whole usage-detail retention, a purge that needed no rollup).

import (
	"context"
	"testing"
)

func rollupAll(t *testing.T, e *Enforcer, tenant, site string) int64 {
	t.Helper()
	var total int64
	for i := 0; i < 100; i++ {
		n, err := e.RollupAccountingUsage(context.Background(), tenant, site)
		if err != nil {
			t.Fatalf("rollup: %v", err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
	t.Fatal("the rollup never drained")
	return total
}

func TestIntegration_UsageRollup_RollsUpThenPurgesInThreeTiers(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, sess, liveSess := oldTerminatedUsage(t, p)
	// In production ingested_at is the sample time; the fixture ingests "now". Align them (the table is immutable,
	// so the superuser steps around its trigger for this one setup statement).
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.accounting_records SET ingested_at = sampled_at WHERE site_id=$1`, f.site); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rawBefore := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, f.site)
	acctd := New(rolePool(t, "svc_acctd"))

	// Nothing is purged before the rollup has read it, whatever the retention says.
	if n, err := acctd.PurgeAccountingDetail(ctx, f.tenant, f.site, 1000); err != nil || n != 0 {
		t.Fatalf("a purge before any rollup removed %d (%v); an unread sample must stay", n, err)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, f.site); c != rawBefore {
		t.Fatalf("raw samples %d -> %d before the rollup", rawBefore, c)
	}

	read := rollupAll(t, acctd, f.tenant, f.site)
	if read != int64(rawBefore) {
		t.Fatalf("the rollup read %d samples, want %d", read, rawBefore)
	}
	// What the hours hold is what the samples summed to, and what the session total says.
	var up, down int64
	if err := p.QueryRow(ctx, `SELECT COALESCE(sum(bytes_up),0), COALESCE(sum(bytes_down),0)
		FROM iam_v2.accounting_usage_hour WHERE session_id=$1`, sess).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if sessTotal := scalar[int64](t, p, `SELECT bytes_up + bytes_down FROM iam_v2.sessions WHERE id=$1`, sess); up+down != sessTotal {
		t.Fatalf("hourly totals %d, session total %d", up+down, sessTotal)
	}
	if mh, mm := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_hour WHERE site_id=$1`, f.site),
		count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_minute WHERE site_id=$1`, f.site); mh == 0 || mm == 0 {
		t.Fatalf("hour rows %d, minute rows %d", mh, mm)
	}
	// Idempotent: a second pass reads nothing and changes nothing.
	hoursBefore := scalar[int64](t, p, `SELECT COALESCE(sum(bytes_up+bytes_down),0) FROM iam_v2.accounting_usage_hour WHERE site_id=$1`, f.site)
	if again := rollupAll(t, acctd, f.tenant, f.site); again != 0 {
		t.Fatalf("a second rollup read %d samples", again)
	}
	if h := scalar[int64](t, p, `SELECT COALESCE(sum(bytes_up+bytes_down),0) FROM iam_v2.accounting_usage_hour WHERE site_id=$1`, f.site); h != hoursBefore {
		t.Fatalf("hourly totals changed on a repeat: %d -> %d", hoursBefore, h)
	}

	// The usage-detail retention (hourly tier) is the existing setting.
	edged := rolePool(t, "svc_edged")
	if _, err := edged.Exec(ctx, `SELECT iam_v2.accounting_retention_set($1,$2,30,'admin@test','keep a month of detail')`,
		f.tenant, f.site); err != nil {
		t.Fatalf("svc_edged could not set the retention: %v", err)
	}
	sessBefore := scalar[int64](t, p, `SELECT bytes_up + bytes_down FROM iam_v2.sessions WHERE id=$1`, sess)
	n, err := acctd.PurgeAccountingDetail(ctx, f.tenant, f.site, 1000)
	if err != nil {
		t.Fatalf("purge as svc_acctd: %v", err)
	}
	if n < rawBefore {
		t.Fatalf("purged %d; the rolled-up raw samples (%d) and the old minute and hour rows should all go", n, rawBefore)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, f.site); c != 0 {
		t.Fatalf("%d raw samples remain; all were rolled up and older than the raw retention", c)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_minute WHERE site_id=$1`, f.site); c != 0 {
		t.Fatalf("%d minute rows remain past the 7-day retention", c)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_hour WHERE session_id=$1`, sess); c != 0 {
		t.Fatal("the hourly totals of the terminated session survive the usage-detail retention")
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_hour WHERE session_id=$1`, liveSess); c == 0 {
		t.Fatal("the purge removed the hourly totals of a LIVE entitlement")
	}
	if after := scalar[int64](t, p, `SELECT bytes_up + bytes_down FROM iam_v2.sessions WHERE id=$1`, sess); after != sessBefore {
		t.Fatalf("session totals changed %d -> %d; the purge removes detail, not totals", sessBefore, after)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_purge_runs WHERE site_id=$1`, f.site); c != 3 {
		t.Fatalf("%d purge runs recorded, want one per tier that removed something (3)", c)
	}
}

func TestIntegration_UsageRollup_SettingsAreBoundedAndAudited(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, _, _ := oldTerminatedUsage(t, p)
	edged := rolePool(t, "svc_edged")
	def := scalar[string](t, p, `SELECT minute_retention_days||'/'||raw_retention_days||'/'||purge_budget_seconds
		FROM iam_v2.usage_rollup_settings_get($1,$2)`, f.tenant, f.site)
	if def != "7/2/10" {
		t.Fatalf("defaults %s, want 7/2/10", def)
	}
	for _, bad := range []string{
		`SELECT iam_v2.usage_rollup_settings_set($1,$2,0,2,10,'admin@test','minute too small')`,
		`SELECT iam_v2.usage_rollup_settings_set($1,$2,7,31,10,'admin@test','raw too large')`,
		`SELECT iam_v2.usage_rollup_settings_set($1,$2,7,2,121,'admin@test','budget too large')`,
		`SELECT iam_v2.usage_rollup_settings_set($1,$2,7,2,10,'admin@test','x')`,
		`SELECT iam_v2.usage_rollup_settings_set($1,$2,7,2,10,'','no operator')`,
	} {
		if _, err := edged.Exec(ctx, bad, f.tenant, f.site); err == nil {
			t.Fatalf("accepted: %s", bad)
		}
	}
	if _, err := edged.Exec(ctx, `SELECT iam_v2.usage_rollup_settings_set($1,$2,14,3,30,'admin@test','a longer window for the audit')`,
		f.tenant, f.site); err != nil {
		t.Fatalf("a valid change refused: %v", err)
	}
	if got := scalar[string](t, p, `SELECT minute_retention_days||'/'||raw_retention_days||'/'||purge_budget_seconds
		FROM iam_v2.usage_rollup_settings_get($1,$2)`, f.tenant, f.site); got != "14/3/30" {
		t.Fatalf("settings read back %s", got)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.site_usage_rollup_changes WHERE site_id=$1`, f.site); c != 1 {
		t.Fatalf("%d audit rows, want 1", c)
	}
	if _, err := rolePool(t, "svc_acctd").Exec(ctx, `SELECT iam_v2.usage_rollup_settings_set($1,$2,7,2,10,'x@test','acctd must not')`,
		f.tenant, f.site); err == nil {
		t.Fatal("svc_acctd can change the settings")
	}
}
