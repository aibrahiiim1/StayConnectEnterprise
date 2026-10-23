//go:build integration && phase6

package enforce

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AGGREGATE_ONLINE_TIME through the sweep that ends access: exhaustion must reach the same termination path as
// a window elapse or data crossing, in one transaction at the instant the budget ran out, and revoke access.
// A second termination path would drift in revocation, evidence and timing rules.

// seedAggregate builds an AGGREGATE_ONLINE_TIME entitlement with `budget` seconds and an active session
// already observed for `observed`. It reuses the package's seed helpers; the watermark is seeded because the
// monotonic trigger refuses moving it backwards and a test cannot wait.
func seedAggregate(t *testing.T, p *pgxpool.Pool, budget int, observed time.Duration) (fixture, string, string) {
	t.Helper()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	ent, ses := grant(t, p, f, nil, time.Now().Add(-observed))

	// Plan revisions are immutable, so publish a new revision in the new mode and repoint the entitlement.
	var aggRev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		   (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
		    device_limit_policy, time_accounting_mode, time_quota_seconds)
		   SELECT gen_random_uuid(), r.tenant_id, r.site_id, r.service_plan_id, r.revision_no + 1,
		          r.down_kbps, r.up_kbps, r.max_concurrent_devices, r.device_limit_policy,
		          'AGGREGATE_ONLINE_TIME', $2
		     FROM iam_v2.service_plan_revisions r WHERE r.id = $1
		   RETURNING id::text`, f.svcRev, budget).Scan(&aggRev); err != nil {
		t.Fatalf("publish an aggregate plan revision: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.entitlements
		   SET service_plan_revision_id=$2, time_accounting_mode='AGGREGATE_ONLINE_TIME' WHERE id=$1`,
		ent, aggRev); err != nil {
		t.Fatalf("point the entitlement at the aggregate revision: %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.session_online_watermarks
		   (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
		   VALUES ($1,$2,$3, now() - $4::interval, 0)`,
		f.tenant, f.site, ses, observed.String()); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	return f, ent, ses
}

// Exhaustion ends access at the instant the budget ran out, through the one boundary path.
func TestIntegration_Phase6_ExhaustionTerminatesThroughTheSweep(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 60, 10*time.Minute)
	tenant, site := f.tenant, f.site

	due, err := New(p).WithAggregateOnlineTime(3600).EnforceExpiries(ctx, tenant, site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var got *Expiry
	for i := range due {
		if due[i].EntitlementID == ent {
			got = &due[i]
		}
	}
	if got == nil {
		t.Fatal("the exhausted entitlement was not ended by the sweep; the guest keeps browsing on a spent budget")
	}
	if got.Reason != "TIME" {
		t.Fatalf("aggregate exhaustion ended with reason %q; the contract's TIME reason covers it", got.Reason)
	}
	// Dated when the budget ran out (~9 minutes ago), not when the sweep ran.
	if age := time.Since(got.At); age < 7*time.Minute {
		t.Fatalf("the ending was dated %s ago; it should be dated when the budget ran out", age)
	}

	var status, reason string
	var consumed int64
	if err := p.QueryRow(ctx,
		`SELECT status, COALESCE(terminal_reason,''), consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`,
		ent).Scan(&status, &reason, &consumed); err != nil {
		t.Fatal(err)
	}
	if status != "TERMINATED" || reason != "TIME" {
		t.Fatalf("entitlement is %s/%s after exhaustion", status, reason)
	}
	if consumed != 60 {
		t.Fatalf("consumption is %d, not capped at the 60s budget", consumed)
	}
	var live int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`,
		ses).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatal("the session is still live after its entitlement was exhausted")
	}
	var bindings int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND status='AUTHORIZED'`,
		ent).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatal("device authorizations survived the termination")
	}

	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no termination evidence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("the evidence says %q", cause)
	}

	again, err := New(p).WithAggregateOnlineTime(3600).EnforceExpiries(ctx, tenant, site)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	for _, x := range again {
		if x.EntitlementID == ent {
			t.Fatal("a terminated entitlement was ended a second time")
		}
	}
}

// The outer window wins when it comes first and produces its own evidence; whichever clock runs out first ends it once.
func TestIntegration_Phase6_OuterWindowEndsAnAggregateEntitlement(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 86400, 2*time.Minute)
	tenant, site := f.tenant, f.site
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET window_ends_at = now() - interval '1 hour' WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}

	due, err := New(p).WithAggregateOnlineTime(3600).EnforceExpiries(ctx, tenant, site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	n := 0
	for _, x := range due {
		if x.EntitlementID == ent {
			n++
			if x.Reason != "TIME" {
				t.Fatalf("the outer window ended it with reason %q", x.Reason)
			}
			if time.Since(x.At) < 50*time.Minute {
				t.Fatalf("the ending was dated %s ago, not at the window's end", time.Since(x.At))
			}
		}
	}
	if n == 0 {
		t.Fatal("an aggregate entitlement whose outer window elapsed was not ended")
	}
	if n > 1 {
		t.Fatalf("it was ended %d times; two terminal conditions must produce ONE transition", n)
	}
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no termination evidence: %v", err)
	}
	if cause != "AGGREGATE_OUTER_WINDOW_EXPIRED" {
		t.Fatalf("the evidence says %q; the calendar ran out, not the minutes", cause)
	}
}

// The first of the three terminal conditions ends the entitlement once; the losing condition contributes no
// consumption, watermark or evidence.

// seedDataCrossing gives the entitlement a data quota and enough attributed usage to have crossed it `ago`.
func seedDataCrossing(t *testing.T, p *pgxpool.Pool, f fixture, ent, ses string, quota int64, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	// Plan revisions are immutable: the quota arrives in a new revision the entitlement is pointed at.
	var rev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		 (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
		  device_limit_policy, time_accounting_mode, time_quota_seconds, data_quota_bytes)
		 SELECT gen_random_uuid(), r.tenant_id, r.site_id, r.service_plan_id, r.revision_no + 1,
		        r.down_kbps, r.up_kbps, r.max_concurrent_devices, r.device_limit_policy,
		        r.time_accounting_mode, r.time_quota_seconds, $2
		   FROM iam_v2.service_plan_revisions r
		  WHERE r.id = (SELECT service_plan_revision_id FROM iam_v2.entitlements WHERE id=$1)
		 RETURNING id::text`, ent, quota).Scan(&rev); err != nil {
		t.Fatalf("publish a revision carrying the data quota: %v", err)
	}
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET service_plan_revision_id=$2 WHERE id=$1`, ent, rev); err != nil {
		t.Fatalf("point the entitlement at it: %v", err)
	}
	// One sample at the crossing instant; the sweep derives the crossing from the running sum of samples.
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.accounting_records
		 (tenant_id, site_id, session_id, sample_seq, sampled_at, bytes_up, bytes_down)
		 VALUES ($1,$2,$3, 1, now() - $4::interval, $5, 0)`,
		f.tenant, f.site, ses, ago.String(), quota); err != nil {
		t.Fatalf("seed accounting sample: %v", err)
	}
}

func terminalState(t *testing.T, p *pgxpool.Pool, ent string) (status, reason string, consumed int64, at time.Time) {
	t.Helper()
	if err := p.QueryRow(context.Background(),
		`SELECT status, COALESCE(terminal_reason,''), consumed_online_seconds, COALESCE(terminated_at, 'epoch')
		   FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&status, &reason, &consumed, &at); err != nil {
		t.Fatal(err)
	}
	return
}

// Data crossed first: data wins, and aggregate accounting stops at the crossing.
func TestIntegration_Phase6_DataCrossingBoundsAggregateAccrual(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 30*time.Minute)
	seedDataCrossing(t, p, f, ent, ses, 1_000_000, 20*time.Minute)

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	n := 0
	for _, x := range due {
		if x.EntitlementID == ent {
			n++
			if x.Reason != "DATA" {
				t.Fatalf("the entitlement ended with reason %q; the data quota was crossed first", x.Reason)
			}
		}
	}
	if n != 1 {
		t.Fatalf("the entitlement was ended %d times; exactly one terminal transition is allowed", n)
	}

	// Only the ~10 of 30 observed minutes before the crossing are billable.
	status, reason, consumed, _ := terminalState(t, p, ent)
	if status != "TERMINATED" || reason != "DATA" {
		t.Fatalf("durable state is %s/%s", status, reason)
	}
	if consumed < 570 || consumed > 630 {
		t.Fatalf("aggregate consumption is %ds; only the ~600s before the data crossing were billable", consumed)
	}
	var wm time.Time
	if err := p.QueryRow(ctx,
		`SELECT accounted_through FROM iam_v2.session_online_watermarks WHERE session_id=$1`, ses).Scan(&wm); err != nil {
		t.Fatal(err)
	}
	if time.Since(wm) < 19*time.Minute {
		t.Fatalf("the watermark advanced to %s ago, past the data crossing", time.Since(wm))
	}
	var timeEvidence int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`, ent).Scan(&timeEvidence); err != nil {
		t.Fatal(err)
	}
	if timeEvidence != 0 {
		t.Fatal("a DATA termination recorded time-termination evidence")
	}

	before := consumed
	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	_, _, after, _ := terminalState(t, p, ent)
	if after != before {
		t.Fatalf("a later sweep changed consumption from %d to %d", before, after)
	}
}

// The budget running out before the data quota makes TIME win with its own evidence.
func TestIntegration_Phase6_AggregateExhaustionBeatsALaterDataCrossing(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// The budget ran out ~29 minutes ago; the data quota was crossed 5 minutes ago.
	f, ent, ses := seedAggregate(t, p, 60, 30*time.Minute)
	seedDataCrossing(t, p, f, ent, ses, 1_000_000, 5*time.Minute)

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	n := 0
	for _, x := range due {
		if x.EntitlementID == ent {
			n++
			if x.Reason != "TIME" {
				t.Fatalf("the entitlement ended with reason %q; the budget ran out first", x.Reason)
			}
			if age := time.Since(x.At); age < 25*time.Minute {
				t.Fatalf("the ending was dated %s ago, not at the crossing ~29 minutes ago", age)
			}
		}
	}
	if n != 1 {
		t.Fatalf("the entitlement was ended %d times", n)
	}
	status, reason, consumed, _ := terminalState(t, p, ent)
	if status != "TERMINATED" || reason != "TIME" || consumed != 60 {
		t.Fatalf("durable state is %s/%s consumed=%d", status, reason, consumed)
	}
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no time-termination evidence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("the evidence says %q", cause)
	}
}

// DATA at T1 and the outer window at T2 > T1, swept after both: the earliest wins, else the ending is dated
// late and the guest is billed for time after access ended.
func TestIntegration_Phase6_EarliestOfDataAndWindowWins_DataFirst(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 60*time.Minute)
	seedDataCrossing(t, p, f, ent, ses, 1_000_000, 45*time.Minute)
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET window_ends_at = now() - interval '20 minutes' WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	n := 0
	for _, x := range due {
		if x.EntitlementID == ent {
			n++
			if x.Reason != "DATA" {
				t.Fatalf("reason %q: DATA crossed first, so DATA is the terminal condition", x.Reason)
			}
			if age := time.Since(x.At); age < 40*time.Minute || age > 50*time.Minute {
				t.Fatalf("ended %s ago; the data crossing was 45 minutes ago", age)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d terminal transitions; exactly one is allowed", n)
	}

	// Accounting stops at the data crossing: 15 of the 60 observed minutes preceded T1.
	_, reason, consumed, _ := terminalState(t, p, ent)
	if reason != "DATA" {
		t.Fatalf("durable terminal reason is %q", reason)
	}
	if consumed < 870 || consumed > 930 {
		t.Fatalf("consumed %ds; only the ~900s before the data crossing were billable", consumed)
	}
	var wm time.Time
	if err := p.QueryRow(ctx,
		`SELECT accounted_through FROM iam_v2.session_online_watermarks WHERE session_id=$1`, ses).Scan(&wm); err != nil {
		t.Fatal(err)
	}
	if time.Since(wm) < 44*time.Minute {
		t.Fatalf("the watermark advanced to %s ago, past the data crossing", time.Since(wm))
	}
	var evidence int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`, ent).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != 0 {
		t.Fatal("a DATA termination wrote time-termination evidence")
	}

	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	_, _, after, _ := terminalState(t, p, ent)
	if after != consumed {
		t.Fatalf("a later sweep moved consumption from %d to %d", consumed, after)
	}
}

// The mirror image: the window elapsed first, so TIME wins with the window's evidence and consumption stops there.
func TestIntegration_Phase6_EarliestOfDataAndWindowWins_WindowFirst(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 60*time.Minute)
	seedDataCrossing(t, p, f, ent, ses, 1_000_000, 20*time.Minute)
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET window_ends_at = now() - interval '45 minutes' WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	n := 0
	for _, x := range due {
		if x.EntitlementID == ent {
			n++
			if x.Reason != "TIME" {
				t.Fatalf("reason %q: the window elapsed first", x.Reason)
			}
			if age := time.Since(x.At); age < 40*time.Minute || age > 50*time.Minute {
				t.Fatalf("ended %s ago; the window ended 45 minutes ago", age)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d terminal transitions", n)
	}
	_, reason, consumed, _ := terminalState(t, p, ent)
	if reason != "TIME" || consumed < 870 || consumed > 930 {
		t.Fatalf("durable state: reason=%s consumed=%d (want TIME, ~900s up to the window)", reason, consumed)
	}
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no evidence: %v", err)
	}
	if cause != "AGGREGATE_OUTER_WINDOW_EXPIRED" {
		t.Fatalf("the evidence says %q; the calendar ran out, not the minutes", cause)
	}
	_ = ses
}

// These tests run EnforceExpiries end to end as svc_acctd (a privilege audit alone shows what a role may do,
// not that it can do its job), then check the role still cannot do anything else.

// acctdPool returns a pool whose connections switch to svc_acctd at connect time, so nothing runs as owner.
func acctdPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE svc_acctd")
		return err
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// A TIME exhaustion performed entirely by the accounting role.
func TestIntegration_Phase6_TheWholeSweepRunsAsSvcAcctd_TimeExhaustion(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, owner, 60, 30*time.Minute)

	due, err := New(acctdPool(t)).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("the sweep could not run as svc_acctd: %v", err)
	}
	found := false
	for _, x := range due {
		if x.EntitlementID == ent {
			found = true
			if x.Reason != "TIME" {
				t.Fatalf("reason %q", x.Reason)
			}
			if x.Sessions == 0 {
				t.Fatal("the revocation ended no sessions; the writer did not do its job")
			}
		}
	}
	if !found {
		t.Fatal("svc_acctd ran the sweep but the exhausted entitlement was not ended")
	}

	status, reason, consumed, _ := terminalState(t, owner, ent)
	if status != "TERMINATED" || reason != "TIME" || consumed != 60 {
		t.Fatalf("durable state after the real-role sweep: %s/%s consumed=%d", status, reason, consumed)
	}
	var cause string
	if err := owner.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("svc_acctd's sweep recorded no Phase-6 evidence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("evidence says %q", cause)
	}
	var live int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`,
		ses).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatal("the session survived a sweep run by svc_acctd")
	}
}

// A DATA termination through the same writer.
func TestIntegration_Phase6_TheWholeSweepRunsAsSvcAcctd_DataTermination(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, owner, 86400, 30*time.Minute)
	seedDataCrossing(t, owner, f, ent, ses, 1_000_000, 10*time.Minute)

	due, err := New(acctdPool(t)).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("the sweep could not run as svc_acctd: %v", err)
	}
	for _, x := range due {
		if x.EntitlementID == ent && x.Reason != "DATA" {
			t.Fatalf("reason %q", x.Reason)
		}
	}
	_, reason, consumed, _ := terminalState(t, owner, ent)
	if reason != "DATA" {
		t.Fatalf("durable reason %q", reason)
	}
	if consumed < 1140 || consumed > 1260 {
		t.Fatalf("consumed %ds; accrual should stop at the crossing 10 minutes ago (~1200s)", consumed)
	}
}

// Least privilege: running the sweep must not give svc_acctd any other capability.
func TestIntegration_Phase6_SvcAcctdGainedNoOtherMutation(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, owner, 86400, 5*time.Minute)
	acctd := acctdPool(t)

	refused := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := acctd.Exec(ctx, sql, args...); err == nil {
			t.Errorf("svc_acctd could %s", name)
		}
	}
	refused("update an entitlement directly",
		`UPDATE iam_v2.entitlements SET consumed_online_seconds = 0 WHERE id=$1`, ent)
	refused("move a session watermark",
		`UPDATE iam_v2.session_online_watermarks SET accounted_through = now()`)
	refused("write termination evidence",
		`INSERT INTO iam_v2.entitlement_termination_evidence
		   (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode)
		   VALUES ($1,$2,$3,'TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME')`,
		ent, f.tenant, f.site)
	refused("call the termination primitive directly",
		`SELECT iam_v2.terminate_entitlement_at_boundary($1, now(), 'ADMIN')`, ent)
	refused("record time termination directly",
		`SELECT iam_v2.p6_record_time_termination($1,'AGGREGATE_ONLINE_TIME_EXHAUSTED')`, ent)
	refused("authorize a device",
		`SELECT iam_v2.authorize_entitlement_device($1, gen_random_uuid(), now())`, ent)
	refused("release a guest device",
		`SELECT iam_v2.p6_guest_release_device_policy($1, gen_random_uuid())`, ent)
	refused("end sessions directly",
		`UPDATE iam_v2.sessions SET state='ended' WHERE entitlement_id=$1`, ent)

	// The sanctioned writer takes no reason or instant and refuses a healthy entitlement itself.
	if _, err := acctd.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Error("svc_acctd ended a healthy entitlement through the expiry writer")
	}
	refused("establish the terminal condition itself",
		`SELECT * FROM iam_v2.p6_due_terminal($1)`, ent)
	status, _, _, _ := terminalState(t, owner, ent)
	if status == "TERMINATED" {
		t.Fatal("one of the refused operations went through")
	}
}

// The candidate read has no FOR UPDATE (that needs UPDATE on entitlements, which the accounting role must never
// hold), so concurrent sweeps see the same entitlement and only the writer's row lock and idempotence prevent a
// double ending. These attack that directly.

// Concurrent sweeps must produce exactly one terminal transition.
func TestIntegration_Phase6_ConcurrentSweepsProduceOneTermination(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 60, 30*time.Minute)

	const sweeps = 6
	var wg sync.WaitGroup
	ended := make([]int, sweeps)
	errs := make([]error, sweeps)
	start := make(chan struct{})
	for i := 0; i < sweeps; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
			errs[i] = err
			for _, x := range due {
				if x.EntitlementID == ent {
					ended[i]++
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		// A serialization failure is acceptable for a loser; a wrong answer is not.
		if err != nil && !strings.Contains(err.Error(), "SQLSTATE 40001") && !strings.Contains(err.Error(), "deadlock") {
			t.Fatalf("sweep %d failed: %v", i, err)
		}
	}

	var transitions, evidence int
	var consumed int64
	var status string
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM iam_v2.entitlement_state_transitions WHERE entitlement_id=$1 AND to_state='TERMINATED'),
		 (SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1),
		 (SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1),
		 (SELECT status FROM iam_v2.entitlements WHERE id=$1)`, ent).
		Scan(&transitions, &evidence, &consumed, &status); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("%d terminal transitions from %d concurrent sweeps", transitions, sweeps)
	}
	if evidence != 1 {
		t.Fatalf("%d time-termination evidence rows; the ending happened once", evidence)
	}
	if status != "TERMINATED" || consumed != 60 {
		t.Fatalf("durable state %s consumed=%d (budget was 60)", status, consumed)
	}
}

// Six overlapping ticks over one observed interval must charge it once.
func TestIntegration_Phase6_ConcurrentTicksChargeTheIntervalOnce(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 86400, 10*time.Minute)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
		}()
	}
	close(start)
	wg.Wait()

	var consumed int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed < 590 || consumed > 615 {
		t.Fatalf("six concurrent sweeps charged %ds for a ten-minute interval", consumed)
	}
}

// A restart (a fresh Enforcer over the same state) charges nothing extra; the watermark makes it a no-op.
func TestIntegration_Phase6_RestartChargesNothingTwice(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 86400, 10*time.Minute)

	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var first int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&first); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
			t.Fatal(err)
		}
	}
	var after int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after-first > 3 {
		t.Fatalf("three restarts charged %ds beyond the first sweep", after-first)
	}
}

// The Phase-6 flag gates acquisition and surfaces, never accrual: if accrual were gated, losing the flag
// (rollback, config change) would freeze consumption and silently give holders of finite packages unlimited access.

// A live aggregate entitlement keeps consuming and still exhausts with no Phase-6 flag set.
func TestIntegration_Phase6_DisablingTheCapabilityDoesNotMakeLiveAccessUnlimited(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 60, 30*time.Minute)

	// No Phase-6 flags at all, as after a rollback.
	for _, k := range []string{"STAYCONNECT_PHASE6_MASTER", "STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"} {
		t.Setenv(k, "")
	}
	// acctd's wiring: the bound comes from the tick interval; the flag does not decide whether accrual happens.
	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	found := false
	for _, x := range due {
		if x.EntitlementID == ent {
			found = true
		}
	}
	if !found {
		t.Fatal("with the capability disabled, a finite aggregate entitlement was never exhausted: " +
			"its holder now has effectively unlimited access")
	}
	status, reason, consumed, _ := terminalState(t, p, ent)
	if status != "TERMINATED" || reason != "TIME" || consumed != 60 {
		t.Fatalf("durable state after the disabled-capability sweep: %s/%s consumed=%d", status, reason, consumed)
	}
	var live int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`,
		ses).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatal("the session outlived its exhausted budget")
	}
}

// An appliance with no aggregate entitlements is untouched: no writes at all.
func TestIntegration_Phase6_NoAggregateEntitlementsMeansNoWritesAtAll(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	ent, ses := grant(t, p, f, nil, time.Now().Add(-30*time.Minute))

	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	var consumed int64
	var watermarks, skipped, evidence int
	var status string
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1),
		 (SELECT status FROM iam_v2.entitlements WHERE id=$1),
		 (SELECT count(*) FROM iam_v2.session_online_watermarks WHERE session_id=$2),
		 (SELECT count(*) FROM iam_v2.online_time_skipped_intervals WHERE entitlement_id=$1),
		 (SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1)`,
		ent, ses).Scan(&consumed, &status, &watermarks, &skipped, &evidence); err != nil {
		t.Fatal(err)
	}
	if consumed != 0 || watermarks != 0 || skipped != 0 || evidence != 0 {
		t.Fatalf("a validity-window entitlement was touched by aggregate accounting: "+
			"consumed=%d watermarks=%d skipped=%d evidence=%d", consumed, watermarks, skipped, evidence)
	}
	if status == "TERMINATED" {
		t.Fatal("a validity-window entitlement with no expiry was terminated")
	}
}

// The writer takes only an id and derives the terminal condition from authoritative state; otherwise any role
// holding EXECUTE could end any live entitlement it could name. These refusals use real, healthy entitlements.

// seedValidityWindow builds an ordinary healthy VALIDITY_WINDOW entitlement with a live session.
func seedValidityWindow(t *testing.T, p *pgxpool.Pool, window *time.Time) (fixture, string, string) {
	t.Helper()
	f := seed(t, p, 1000, 1000, 0)
	ent, ses := grant(t, p, f, window, time.Now().Add(-30*time.Minute))
	return f, ent, ses
}

// A healthy VALIDITY_WINDOW entitlement cannot be ended by anyone holding the writer.
func TestIntegration_Phase6_WriterRefusesAHealthyValidityWindowEntitlement(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	future := time.Now().Add(24 * time.Hour)
	_, ent, ses := seedValidityWindow(t, owner, &future)
	acctd := acctdPool(t)

	if _, err := acctd.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Fatal("a healthy entitlement was terminated by the expiry writer")
	}
	status, _, _, _ := terminalState(t, owner, ent)
	if status == "TERMINATED" {
		t.Fatal("the refusal still terminated it")
	}
	var live int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`,
		ses).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatal("the refused call still ended the guest's session")
	}
}

// Likewise a healthy aggregate entitlement (budget intact, window open).
func TestIntegration_Phase6_WriterRefusesAnUnexhaustedAggregateEntitlement(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	_, ent, _ := seedAggregate(t, owner, 86400, 5*time.Minute)
	acctd := acctdPool(t)

	if _, err := acctd.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Fatal("an unexhausted aggregate entitlement was terminated")
	}
	status, _, consumed, _ := terminalState(t, owner, ent)
	if status == "TERMINATED" {
		t.Fatal("the refusal still terminated it")
	}
	if consumed != 0 {
		t.Fatalf("the refused call charged %ds", consumed)
	}
}

// An uncrossed data quota cannot be ended as DATA: the writer computes the crossing rather than trusting one.
func TestIntegration_Phase6_WriterRefusesWhenTheQuotaIsNotCrossed(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, owner, 86400, 5*time.Minute)
	seedDataQuotaOnly(t, owner, f, ent, ses, 1_000_000_000, 1_000)
	acctd := acctdPool(t)

	if _, err := acctd.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Fatal("an entitlement below its data quota was terminated as DATA")
	}
	status, _, _, _ := terminalState(t, owner, ent)
	if status == "TERMINATED" {
		t.Fatal("the refusal still terminated it")
	}
}

// Positive controls: the same role and call must end something genuinely due, or the refusals prove nothing.
func TestIntegration_Phase6_WriterExpiresWhatIsGenuinelyDue(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	acctd := acctdPool(t)

	t.Run("elapsed window", func(t *testing.T) {
		past := time.Now().Add(-45 * time.Minute)
		_, ent, _ := seedValidityWindow(t, owner, &past)
		var reason string
		var at time.Time
		var devices, sessions int
		if err := acctd.QueryRow(ctx, `SELECT reason, at, devices, sessions FROM iam_v2.p6_expire_entitlement($1)`,
			ent).Scan(&reason, &at, &devices, &sessions); err != nil {
			t.Fatalf("a genuinely expired entitlement was not expired: %v", err)
		}
		if reason != "TIME" {
			t.Fatalf("reason %q", reason)
		}
		if d := at.Sub(past).Abs(); d > time.Minute {
			t.Fatalf("ended at %s, not at the window instant %s", at, past)
		}
		if sessions == 0 {
			t.Fatal("no session was ended")
		}
	})

	t.Run("crossed data quota", func(t *testing.T) {
		f, ent, ses := seedAggregate(t, owner, 86400, 30*time.Minute)
		seedDataCrossing(t, owner, f, ent, ses, 1_000_000, 12*time.Minute)
		var reason string
		var at time.Time
		var devices, sessions int
		if err := acctd.QueryRow(ctx, `SELECT reason, at, devices, sessions FROM iam_v2.p6_expire_entitlement($1)`,
			ent).Scan(&reason, &at, &devices, &sessions); err != nil {
			t.Fatalf("a crossed quota was not expired: %v", err)
		}
		if reason != "DATA" {
			t.Fatalf("reason %q", reason)
		}
		if age := time.Since(at); age < 10*time.Minute || age > 14*time.Minute {
			t.Fatalf("ended %s ago, not at the crossing ~12 minutes ago", age)
		}
	})

	t.Run("exhausted aggregate budget", func(t *testing.T) {
		f, ent, _ := seedAggregate(t, owner, 60, 30*time.Minute)
		// The tick stamps the crossing and caps consumption; the writer then finds it.
		if _, err := New(owner).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
			t.Fatal(err)
		}
		status, reason, consumed, _ := terminalState(t, owner, ent)
		if status != "TERMINATED" || reason != "TIME" || consumed != 60 {
			t.Fatalf("state after the sweep: %s/%s consumed=%d", status, reason, consumed)
		}
		var cause string
		if err := owner.QueryRow(ctx,
			`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
			ent).Scan(&cause); err != nil {
			t.Fatalf("no evidence: %v", err)
		}
		if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
			t.Fatalf("evidence says %q", cause)
		}
	})
}

// The writer is atomic with its caller: without a commit neither the transition nor the revocation may
// survive, else devices stay authorized for access the record says is over.
func TestIntegration_Phase6_ExpiryWriterRollsBackWithItsCaller(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	past := time.Now().Add(-30 * time.Minute)
	_, ent, ses := seedValidityWindow(t, p, &past)

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reason string
	var at time.Time
	var devices, sessions int
	if err := tx.QueryRow(ctx, `SELECT reason, at, devices, sessions FROM iam_v2.p6_expire_entitlement($1)`,
		ent).Scan(&reason, &at, &devices, &sessions); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("the writer failed on a genuinely due entitlement: %v", err)
	}
	if sessions == 0 {
		_ = tx.Rollback(ctx)
		t.Fatal("the writer ended nothing, so the rollback would prove nothing")
	}
	// The caller fails after the write, as in a crash between the two.
	_ = tx.Rollback(ctx)

	status, _, _, _ := terminalState(t, p, ent)
	if status == "TERMINATED" {
		t.Fatal("the terminal transition survived its caller's rollback")
	}
	var live, authorized int
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')),
		 (SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$2 AND status='AUTHORIZED')`,
		ses, ent).Scan(&live, &authorized); err != nil {
		t.Fatal(err)
	}
	if live != 1 || authorized == 0 {
		t.Fatalf("the revocation survived the rollback: live=%d authorized=%d", live, authorized)
	}
}

// seedDataQuotaOnly gives the entitlement a quota it has not crossed plus one small sample.
func seedDataQuotaOnly(t *testing.T, p *pgxpool.Pool, f fixture, ent, ses string, quota, used int64) {
	t.Helper()
	ctx := context.Background()
	var rev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		 (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
		  device_limit_policy, time_accounting_mode, time_quota_seconds, data_quota_bytes)
		 SELECT gen_random_uuid(), r.tenant_id, r.site_id, r.service_plan_id, r.revision_no + 1,
		        r.down_kbps, r.up_kbps, r.max_concurrent_devices, r.device_limit_policy,
		        r.time_accounting_mode, r.time_quota_seconds, $2
		   FROM iam_v2.service_plan_revisions r
		  WHERE r.id = (SELECT service_plan_revision_id FROM iam_v2.entitlements WHERE id=$1)
		 RETURNING id::text`, ent, quota).Scan(&rev); err != nil {
		t.Fatalf("publish a revision with the quota: %v", err)
	}
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET service_plan_revision_id=$2 WHERE id=$1`, ent, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.accounting_records
		 (tenant_id, site_id, session_id, sample_seq, sampled_at, bytes_up, bytes_down)
		 VALUES ($1,$2,$3, 1, now() - interval '1 minute', $4, 0)`,
		f.tenant, f.site, ses, used); err != nil {
		t.Fatalf("seed a small sample: %v", err)
	}
}

// A restore rewinds the watermark, leaving an interval with no record or evidence. The rule for uncertain
// intervals fails toward the guest: rebaseline, record what was skipped, undercharge; never charge unobserved
// time or an interval twice. The bound enforces it, so this uses acctd's real bound (a minute), not a generous value.
func TestIntegration_Phase6_RestoreToAnOlderWatermarkDoesNotRecharge(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 20*time.Minute)

	const bound = 60 // aggregateChargeBoundSeconds for the default tick interval

	if _, err := New(p).WithAggregateOnlineTime(bound).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var beforeRestore int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&beforeRestore); err != nil {
		t.Fatal(err)
	}

	// The restore: rows are replaced as a dump restore does (the monotonic trigger guards running-system UPDATEs).
	// Consumption cannot be rewound in place (only audited entitlement_adjustments can decrease it), so what
	// the restore leaves observable is a watermark in the past, which is what drives charging.
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET consumed_online_seconds=0 WHERE id=$1`, ent); err == nil {
		t.Fatal("consumption was rewound in place, with no adjustment record")
	}
	restoredConsumption := beforeRestore
	if _, err := p.Exec(ctx, `DELETE FROM iam_v2.session_online_watermarks WHERE session_id=$1`, ses); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.session_online_watermarks
		 (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
		 VALUES ($1,$2,$3, now() - interval '15 minutes', 0)`, f.tenant, f.site, ses); err != nil {
		t.Fatal(err)
	}
	skippedBefore := countRows(t, p, `SELECT count(*) FROM iam_v2.online_time_skipped_intervals WHERE entitlement_id=$1`, ent)

	if _, err := New(p).WithAggregateOnlineTime(bound).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}

	var afterRestore int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&afterRestore); err != nil {
		t.Fatal(err)
	}
	// At most one bound: the unaccounted minutes are not evidence, so the guest is undercharged.
	if afterRestore > restoredConsumption+bound+2 {
		t.Fatalf("a restore charged %ds for an interval nobody observed (bound is %ds)",
			afterRestore-restoredConsumption, bound)
	}
	skippedAfter := countRows(t, p, `SELECT count(*) FROM iam_v2.online_time_skipped_intervals WHERE entitlement_id=$1`, ent)
	if skippedAfter <= skippedBefore {
		t.Fatal("the unobserved post-restore interval was neither charged nor recorded as skipped")
	}

	// A second sweep must not catch up: the skipped interval stays skipped.
	if _, err := New(p).WithAggregateOnlineTime(bound).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var settled int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if settled-afterRestore > 2 {
		t.Fatalf("a later sweep charged another %ds of the restored gap", settled-afterRestore)
	}
}

// A running system cannot rewind a watermark; an UPDATE moving one backwards is a bug or attack.
func TestIntegration_Phase6_ARunningSystemCannotRewindAWatermark(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, _, ses := seedAggregate(t, p, 86400, 10*time.Minute)
	if _, err := New(p).WithAggregateOnlineTime(60).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.session_online_watermarks
		   SET accounted_through = accounted_through - interval '1 hour' WHERE session_id=$1`, ses); err == nil {
		t.Fatal("a watermark was moved backwards by an ordinary update")
	}
}

func countRows(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A crash between the tick's stamp and the termination must not strand the entitlement: a sweep with accrual
// disabled still ends it at the stamped instant.
func TestIntegration_Phase6_AStrandedExhaustionIsFoundByTheNextSweep(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 60, 30*time.Minute)

	// Stamp it as the tick does, then leave it live.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.entitlements
		   SET consumed_online_seconds = 60, online_time_exhausted_at = now() - interval '20 minutes'
		 WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	found := false
	for _, x := range due {
		if x.EntitlementID == ent {
			found = true
			if x.Reason != "TIME" {
				t.Fatalf("reason %q", x.Reason)
			}
			if age := time.Since(x.At); age < 18*time.Minute || age > 22*time.Minute {
				t.Fatalf("ended %s ago, not at the stamped crossing ~20 minutes ago", age)
			}
		}
	}
	if !found {
		t.Fatal("an entitlement stamped exhausted was left live by the sweep")
	}
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no evidence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("evidence says %q", cause)
	}
}

// An exhaustion instant must be provable: falling back to now() would write a historical fact nobody observed
// into the termination, evidence and session end times.

// seedExhaustedWithoutAStamp puts consumption at the budget, optionally without recording the adjustment (undatable).
func seedExhaustedWithoutAStamp(t *testing.T, p *pgxpool.Pool, ent string, withAdjustment bool, adjAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	if withAdjustment {
		var tenant, site string
		if err := p.QueryRow(ctx, `SELECT tenant_id::text, site_id::text FROM iam_v2.entitlements WHERE id=$1`,
			ent).Scan(&tenant, &site); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.entitlement_adjustments
			 (tenant_id, site_id, entitlement_id, field, old_value, new_value, reason, created_at)
			 VALUES ($1,$2,$3,'consumed_online_seconds','0','60','TEST_ADJUSTMENT', now() - $4::interval)`,
			tenant, site, ent, adjAgo.String()); err != nil {
			t.Fatalf("seed adjustment: %v", err)
		}
	}
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET consumed_online_seconds = 60 WHERE id=$1`, ent); err != nil {
		t.Fatalf("raise consumption: %v", err)
	}
}

// Undatable exhaustion is not a termination: nothing is claimed, stamped or evidenced.
func TestIntegration_Phase6_UndatableExhaustionIsNeverFabricated(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 60, 5*time.Minute)
	seedExhaustedWithoutAStamp(t, p, ent, false, 0)

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, x := range due {
		if x.EntitlementID == ent && x.Reason != "SUSPENDED_OVER_BUDGET" {
			t.Fatalf("an exhaustion nobody can date was turned into a %s termination at %s", x.Reason, x.At)
		}
	}
	// Access is still withheld: the historical claim and the current safety decision are separate.
	if s := statusOf(t, p, ent); s != "SUSPENDED" {
		t.Fatalf("an entitlement over its budget is %s; access must be withheld while the crossing is unknown", s)
	}
	status, _, _, _ := terminalState(t, p, ent)
	if status == "TERMINATED" {
		t.Fatal("the entitlement was terminated on an invented instant")
	}
	var stamped bool
	var evidence int
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT online_time_exhausted_at IS NOT NULL FROM iam_v2.entitlements WHERE id=$1),
		 (SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1)`,
		ent).Scan(&stamped, &evidence); err != nil {
		t.Fatal(err)
	}
	if stamped {
		t.Fatal("a crossing instant was stamped with the clock of the tick that noticed")
	}
	if evidence != 0 {
		t.Fatal("evidence was written for a termination that did not happen")
	}
	if _, err := p.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Fatal("the writer terminated an entitlement whose exhaustion cannot be dated")
	}
	_ = ses
}

// A datable one is honoured: the audited adjustment is the evidence and its timestamp the instant.
func TestIntegration_Phase6_AdjustmentDatedExhaustionIsHonoured(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 60, 5*time.Minute)
	seedExhaustedWithoutAStamp(t, p, ent, true, 25*time.Minute)

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	found := false
	for _, x := range due {
		if x.EntitlementID == ent {
			found = true
			if x.Reason != "TIME" {
				t.Fatalf("reason %q", x.Reason)
			}
			if age := time.Since(x.At); age < 23*time.Minute || age > 27*time.Minute {
				t.Fatalf("ended %s ago, not at the adjustment's instant ~25 minutes ago", age)
			}
		}
	}
	if !found {
		t.Fatal("an exhaustion dated by an audited adjustment was not acted on")
	}
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no evidence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("evidence says %q", cause)
	}
}

// The exhaustion instant must be the actual crossing, not the earliest related adjustment: backdating records
// real access as happening after the entitlement ended, as wrong as inventing now() and harder to spot.

// adjust records an audited counter change, the only way a counter moves outside accrual.
func adjust(t *testing.T, p *pgxpool.Pool, f fixture, ent, field string, from, to int64, ago time.Duration) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.entitlement_adjustments
		 (tenant_id, site_id, entitlement_id, field, old_value, new_value, reason, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,'TEST_ADJUSTMENT', now() - $7::interval)`,
		f.tenant, f.site, ent, field, fmt.Sprint(from), fmt.Sprint(to), ago.String()); err != nil {
		t.Fatalf("adjustment: %v", err)
	}
}

// adjustRaw is adjust for non-numeric fields.
func adjustRaw(t *testing.T, p *pgxpool.Pool, f fixture, ent, field, from, to string, ago time.Duration) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.entitlement_adjustments
		 (tenant_id, site_id, entitlement_id, field, old_value, new_value, reason, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,'TEST_ADJUSTMENT', now() - $7::interval)`,
		f.tenant, f.site, ent, field, from, to, ago.String()); err != nil {
		t.Fatalf("adjustment: %v", err)
	}
}

func exhaustionInstant(t *testing.T, p *pgxpool.Pool, ent string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := p.QueryRow(context.Background(),
		`SELECT iam_v2.p6_exhaustion_instant($1)`, ent).Scan(&at); err != nil {
		t.Fatalf("exhaustion instant: %v", err)
	}
	return at
}

// An earlier, non-exhausting adjustment must not be the answer.
func TestIntegration_Phase6_CrossingIsTheAdjustmentThatActuallyExhausted(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	adjust(t, p, f, ent, "consumed_online_seconds", 0, 100, 4*time.Hour)
	adjust(t, p, f, ent, "consumed_online_seconds", 100, 600, 30*time.Minute)
	if _, err := p.Exec(context.Background(),
		`UPDATE iam_v2.entitlements SET consumed_online_seconds = 600 WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}

	at := exhaustionInstant(t, p, ent)
	if at == nil {
		t.Fatal("a crossing that the history plainly accounts for was reported as undatable")
	}
	age := time.Since(*at)
	if age < 25*time.Minute || age > 35*time.Minute {
		t.Fatalf("dated %s ago; the crossing was the 30-minute-old adjustment, not the 4-hour-old one", age)
	}

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(context.Background(), f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range due {
		if x.EntitlementID == ent {
			if d := time.Since(x.At); d < 25*time.Minute || d > 35*time.Minute {
				t.Fatalf("the entitlement was ended %s ago, backdated to an unrelated adjustment", d)
			}
		}
	}
}

// A history that never crosses leaves it undatable, however exhausted the entitlement looks now.
func TestIntegration_Phase6_UnaccountedExhaustionStaysUndatable(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	adjust(t, p, f, ent, "consumed_online_seconds", 0, 100, 2*time.Hour)
	if _, err := p.Exec(context.Background(),
		`UPDATE iam_v2.entitlements SET consumed_online_seconds = 600 WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}
	if at := exhaustionInstant(t, p, ent); at != nil {
		t.Fatalf("an unexplained exhaustion was dated %s, from a history that never crosses", at)
	}
	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(context.Background(), f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range due {
		if x.EntitlementID == ent && x.Reason != "SUSPENDED_OVER_BUDGET" {
			t.Fatalf("it was terminated anyway, as %s at %s", x.Reason, x.At)
		}
	}
	status, _, _, _ := terminalState(t, p, ent)
	if status == "TERMINATED" {
		t.Fatal("terminated on an instant nothing supports")
	}
	if status != "SUSPENDED" {
		t.Fatalf("an entitlement over its budget is %s", status)
	}
}

// The tick's own stamp always wins; a later adjustment must not move it.
func TestIntegration_Phase6_TheTicksStampOutranksAdjustmentHistory(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 60, 30*time.Minute)
	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(context.Background(), f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var stamped time.Time
	if err := p.QueryRow(context.Background(),
		`SELECT online_time_exhausted_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&stamped); err != nil {
		t.Fatalf("the tick recorded no stamp: %v", err)
	}
	adjust(t, p, f, ent, "consumed_online_seconds", 60, 60, 90*time.Minute)
	at := exhaustionInstant(t, p, ent)
	if at == nil || !at.Equal(stamped) {
		t.Fatalf("the stamp %s was overridden by adjustment history (%v)", stamped, at)
	}
}

// Adjustment history is a lower bound, not a reconstruction: accrual writes no adjustment row, so a recorded
// value at or above the budget proves the true value was too (consumption only rises outside adjustments), while
// below the budget it proves nothing and a budget cut under unrecorded accrual is undatable.

// accrueSilently advances consumption as the tick does, with no adjustment row.
func accrueSilently(t *testing.T, p *pgxpool.Pool, ent string, to int64) {
	t.Helper()
	if _, err := p.Exec(context.Background(),
		`UPDATE iam_v2.entitlements SET consumed_online_seconds = $2 WHERE id = $1`, ent, to); err != nil {
		t.Fatalf("silent accrual: %v", err)
	}
}

// repointBudget publishes a smaller immutable revision and records the audited repoint, as a real budget cut does.
func repointBudget(t *testing.T, p *pgxpool.Pool, f fixture, ent string, budget int64, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	var oldRev, newRev string
	if err := p.QueryRow(ctx, `SELECT service_plan_revision_id::text FROM iam_v2.entitlements WHERE id=$1`,
		ent).Scan(&oldRev); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		 (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
		  device_limit_policy, time_accounting_mode, time_quota_seconds)
		 SELECT gen_random_uuid(), r.tenant_id, r.site_id, r.service_plan_id, r.revision_no + 20,
		        r.down_kbps, r.up_kbps, r.max_concurrent_devices, r.device_limit_policy,
		        r.time_accounting_mode, $2
		   FROM iam_v2.service_plan_revisions r WHERE r.id = $1 RETURNING id::text`,
		oldRev, budget).Scan(&newRev); err != nil {
		t.Fatalf("publish the smaller revision: %v", err)
	}
	adjustRaw(t, p, f, ent, "service_plan_revision_id", oldRev, newRev, ago)
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET service_plan_revision_id=$2 WHERE id=$1`, ent, newRev); err != nil {
		t.Fatal(err)
	}
}

// Case 1: consumption entirely from accrual, then a lower-budget repoint. Nothing recorded the accrual, so the
// cut cannot be claimed as the crossing.
func TestIntegration_Phase6_AccruedConsumptionThenBudgetCutIsUndatable(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	accrueSilently(t, p, ent, 500)
	repointBudget(t, p, f, ent, 400, 20*time.Minute)

	if at := exhaustionInstant(t, p, ent); at != nil {
		t.Fatalf("a budget cut under silently-accrued consumption was dated %s; the history cannot show "+
			"what consumption was at the cut", at)
	}
	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(context.Background(), f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range due {
		if x.EntitlementID == ent && x.Reason == "TIME" {
			t.Fatalf("it was TERMINATED for TIME at %s, on an instant nothing supports", x.At)
		}
	}
	// Over budget now, so access is withheld through SUSPENDED while the historical question stays open.
	if s := statusOf(t, p, ent); s != "SUSPENDED" {
		t.Fatalf("an entitlement over its budget is %s; access must be withheld even when the crossing "+
			"cannot be dated", s)
	}
}

// Case 2: an audited adjustment already at or above the later budget (450 >= 400) makes the cut datable
// whatever accrual did in between.
func TestIntegration_Phase6_RecordedFloorAtOrOverTheNewBudgetIsDatable(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	adjust(t, p, f, ent, "consumed_online_seconds", 0, 450, 3*time.Hour)
	accrueSilently(t, p, ent, 520)
	repointBudget(t, p, f, ent, 400, 25*time.Minute)

	at := exhaustionInstant(t, p, ent)
	if at == nil {
		t.Fatal("a cut below a RECORDED consumption floor should be datable at the cut")
	}
	if age := time.Since(*at); age < 20*time.Minute || age > 30*time.Minute {
		t.Fatalf("dated %s ago; the crossing is the cut ~25 minutes ago", age)
	}
}

// Case 3: the crossing is the first instant the recorded floor reached the budget then in force, not the first or last budget change.
func TestIntegration_Phase6_MultipleBudgetAndConsumptionChangesPickTheFirstRealCrossing(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 1000, 5*time.Minute)
	adjust(t, p, f, ent, "consumed_online_seconds", 0, 300, 5*time.Hour)   // 300 < 1000
	repointBudget(t, p, f, ent, 800, 4*time.Hour)                          // 300 < 800
	adjust(t, p, f, ent, "consumed_online_seconds", 300, 700, 3*time.Hour) // 700 < 800
	repointBudget(t, p, f, ent, 600, 2*time.Hour)                          // 700 >= 600  <-- here
	repointBudget(t, p, f, ent, 500, 1*time.Hour)                          // later, irrelevant
	accrueSilently(t, p, ent, 700)

	at := exhaustionInstant(t, p, ent)
	if at == nil {
		t.Fatal("a history with a genuine crossing was reported as undatable")
	}
	if age := time.Since(*at); age < 105*time.Minute || age > 135*time.Minute {
		t.Fatalf("dated %s ago; the first real crossing was the cut to 600, two hours ago", age)
	}
}

// Case 4: a history that never records a consumption value gives no lower bound, so nothing is claimable.
func TestIntegration_Phase6_NoRecordedConsumptionMeansNoCrossing(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	accrueSilently(t, p, ent, 590)
	repointBudget(t, p, f, ent, 500, 30*time.Minute)
	if at := exhaustionInstant(t, p, ent); at != nil {
		t.Fatalf("dated %s from a history that never records a consumption value", at)
	}
}

// Malformed history fails safe without aborting the sweep; a raise would take down the whole site's expiry pass.
func TestIntegration_Phase6_MalformedAdjustmentHistoryFailsSafe(t *testing.T) {
	p := pool(t)
	ctx := context.Background()

	for name, seed := range map[string]func(f fixture, ent string){
		"unparseable budget seed": func(f fixture, ent string) {
			adjustRaw(t, p, f, ent, "time_quota_seconds", "not-a-number", "400", 30*time.Minute)
		},
		"unparseable revision seed": func(f fixture, ent string) {
			adjustRaw(t, p, f, ent, "service_plan_revision_id", "not-a-uuid", "also-not-a-uuid", 30*time.Minute)
		},
		"unparseable consumption value": func(f fixture, ent string) {
			adjustRaw(t, p, f, ent, "consumed_online_seconds", "0", "six hundred", 30*time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
			accrueSilently(t, p, ent, 600) // exhausted, so the function reaches the history at all
			seed(f, ent)

			var at *time.Time
			if err := p.QueryRow(ctx, `SELECT iam_v2.p6_exhaustion_instant($1)`, ent).Scan(&at); err != nil {
				t.Fatalf("a malformed history raised instead of failing safe: %v", err)
			}
			if at != nil {
				t.Fatalf("a malformed history produced an instant: %s", at)
			}
			if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
				t.Fatalf("the sweep aborted because one entitlement's history was unreadable: %v", err)
			}
			status, _, _, _ := terminalState(t, p, ent)
			if status == "TERMINATED" {
				t.Fatal("terminated on an unreadable history")
			}
		})
	}
}

// Historical truth and current safety are separate: an undatable over-budget entitlement is withheld through
// SUSPENDED (admission refuses non-ACTIVE and the shaping plan selects only ACTIVE) with no invented end time.

// overBudgetUndatable has consumption at the budget through silent accrual and a live session.
func overBudgetUndatable(t *testing.T, p *pgxpool.Pool) (fixture, string, string) {
	t.Helper()
	f, ent, ses := seedAggregate(t, p, 600, 5*time.Minute)
	accrueSilently(t, p, ent, 600)
	return f, ent, ses
}

func statusOf(t *testing.T, p *pgxpool.Pool, ent string) string {
	t.Helper()
	var s string
	if err := p.QueryRow(context.Background(),
		`SELECT status FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Access is withheld and no history is fabricated.
func TestIntegration_Phase6_UndatableOverBudgetLosesAccessWithoutFabricatedHistory(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := overBudgetUndatable(t, p)

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var got *Expiry
	for i := range due {
		if due[i].EntitlementID == ent {
			got = &due[i]
		}
	}
	if got == nil {
		t.Fatal("an entitlement over its budget kept its access because its crossing could not be dated")
	}
	if got.Reason != "SUSPENDED_OVER_BUDGET" {
		t.Fatalf("reported as %q; it is withheld, not ended", got.Reason)
	}

	// (1) Access is gone: no live session, no authorized binding.
	var live, authorized int
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')),
		 (SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$2 AND status='AUTHORIZED')`,
		ses, ent).Scan(&live, &authorized); err != nil {
		t.Fatal(err)
	}
	if live != 0 || authorized != 0 {
		t.Fatalf("access survived: live sessions=%d authorized devices=%d", live, authorized)
	}

	// (1b) Child rows must not claim the entitlement ended; support reads the session row first.
	var devReason, sessReason string
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT disconnected_reason FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 LIMIT 1),
		 (SELECT end_reason FROM iam_v2.sessions WHERE id=$2)`, ent, ses).Scan(&devReason, &sessReason); err != nil {
		t.Fatal(err)
	}
	if devReason != "ENTITLEMENT_SUSPENDED" || sessReason != "ENTITLEMENT_SUSPENDED" {
		t.Fatalf("child rows claim the entitlement ended: device=%q session=%q", devReason, sessReason)
	}

	// (2) No historical claim: not terminated, no terminal reason, terminated_at or TIME evidence.
	var status, reason string
	var terminatedAt *time.Time
	var evidence int
	if err := p.QueryRow(ctx, `SELECT status, COALESCE(terminal_reason,''), terminated_at,
		 (SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1)
		   FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&status, &reason, &terminatedAt, &evidence); err != nil {
		t.Fatal(err)
	}
	if status != "SUSPENDED" {
		t.Fatalf("status is %s; SUSPENDED is the state that withholds access without ending anything", status)
	}
	if reason != "" || terminatedAt != nil || evidence != 0 {
		t.Fatalf("a historical ending was fabricated: reason=%q terminated_at=%v evidence=%d",
			reason, terminatedAt, evidence)
	}

	// (3) The admission primitive refuses a non-ACTIVE entitlement.
	var dev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices (id,tenant_id,site_id,appliance_id,mac,last_seen)
		 VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),
		         ('02:00:00:'||substr(md5(random()::text),1,2)||':'||substr(md5(random()::text),3,2)||':'||
		          substr(md5(random()::text),5,2))::macaddr, now()) RETURNING id::text`,
		f.tenant, f.site).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, dev); err == nil {
		t.Fatal("a suspended over-budget entitlement admitted a new device")
	}

	// (4) Not in the shaping plan, so netd forwards nothing.
	plan, err := New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, sh := range plan.Shape {
		if sh.SessionID == ses {
			t.Fatal("a suspended entitlement's session is still in the shaping plan")
		}
	}

	// (5) One transition with a machine-readable reason, not one per sweep.
	for i := 0; i < 3; i++ {
		if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
			t.Fatal(err)
		}
	}
	var transitions int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_state_transitions
		  WHERE entitlement_id=$1 AND to_state='SUSPENDED' AND reason='AGGREGATE_OVER_BUDGET'`,
		ent).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("%d suspension transitions after four sweeps; the evidence must be bounded", transitions)
	}
	if statusOf(t, p, ent) != "SUSPENDED" {
		t.Fatal("a later sweep changed the status")
	}
}

// When evidence later establishes the true crossing, the entitlement terminates through the one boundary path at that instant.
func TestIntegration_Phase6_SuspendedConvergesWhenEvidenceArrives(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := overBudgetUndatable(t, p)
	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, p, ent) != "SUSPENDED" {
		t.Fatal("the fixture did not reach the suspended state")
	}

	adjust(t, p, f, ent, "consumed_online_seconds", 0, 600, 40*time.Minute)

	due, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range due {
		if x.EntitlementID == ent && x.Reason == "TIME" {
			found = true
			if age := time.Since(x.At); age < 35*time.Minute || age > 45*time.Minute {
				t.Fatalf("terminated %s ago rather than at the newly-evidenced crossing", age)
			}
		}
	}
	if !found {
		t.Fatal("evidence arrived and the entitlement did not converge to a termination")
	}
	status, reason, _, _ := terminalState(t, p, ent)
	if status != "TERMINATED" || reason != "TIME" {
		t.Fatalf("converged state is %s/%s", status, reason)
	}
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no evidence after convergence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("evidence says %q", cause)
	}
}

// Six overlapping sweeps leave one suspension transition.
func TestIntegration_Phase6_ConcurrentSweepsSuspendOnce(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := overBudgetUndatable(t, p)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site)
		}()
	}
	close(start)
	wg.Wait()

	var transitions int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_state_transitions
		  WHERE entitlement_id=$1 AND to_state='SUSPENDED'`, ent).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("%d suspension transitions from six concurrent sweeps", transitions)
	}
	if statusOf(t, p, ent) != "SUSPENDED" {
		t.Fatal("the concurrent sweeps left it unsuspended")
	}
}

// A below-budget entitlement is untouched; otherwise a sweep that suspends everything would pass the tests above.
func TestIntegration_Phase6_BelowBudgetIsUnaffected(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 5*time.Minute)

	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(t, p, ent); s != "ACTIVE" {
		t.Fatalf("a below-budget entitlement is %s", s)
	}
	var live, authorized int
	if err := p.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')),
		 (SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$2 AND status='AUTHORIZED')`,
		ses, ent).Scan(&live, &authorized); err != nil {
		t.Fatal(err)
	}
	if live != 1 || authorized == 0 {
		t.Fatalf("an ordinary guest lost access: live=%d authorized=%d", live, authorized)
	}
	plan, err := New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	inPlan := false
	for _, sh := range plan.Shape {
		if sh.SessionID == ses {
			inPlan = true
		}
	}
	if !inPlan {
		t.Fatal("an ordinary guest's session left the shaping plan")
	}
}
