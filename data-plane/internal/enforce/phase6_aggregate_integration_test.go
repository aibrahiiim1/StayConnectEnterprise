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

// AGGREGATE_ONLINE_TIME, THROUGH THE SWEEP THAT ACTUALLY ENDS ACCESS.
//
// The database gate proves the accrual arithmetic. What it cannot prove is the part that decides whether a
// guest is actually disconnected: that exhaustion reaches the SAME termination path a window elapse or a data
// crossing reaches, in the same transaction, at the instant the budget ran out -- and that it revokes.
//
// The two failure modes worth naming, because both look fine in isolation:
//
//   * accrual runs, reports exhaustion, and nothing ends the access. The guest keeps browsing on a budget
//     that is gone, and the durable state says ACTIVE with consumption pinned at the budget.
//   * exhaustion terminates through a SECOND path of its own. Two paths mean two sets of rules about
//     revocation, evidence and the true instant, and they will not stay in step.

// seedAggregate builds one AGGREGATE_ONLINE_TIME entitlement with `budget` seconds and one active session
// that has already been OBSERVED for `observed`.
//
// It reuses this package's own seed/grant helpers rather than rebuilding the chain: an entitlement needs a
// purchase, a stay, a PMS interface and a plan, and a hand-rolled fixture that skipped any of them would be
// testing a shape the product cannot produce. Only the two things this mode actually changes are set
// afterwards -- the plan revision's time mode and quota, and the entitlement's mode.
//
// The watermark is seeded rather than accrued, because a watermark cannot be moved backwards (the monotonic
// trigger refuses it, correctly) and a test cannot wait ten real minutes to observe something.
func seedAggregate(t *testing.T, p *pgxpool.Pool, budget int, observed time.Duration) (fixture, string, string) {
	t.Helper()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	ent, ses := grant(t, p, f, nil, time.Now().Add(-observed))

	// A NEW revision, because plan revisions are IMMUTABLE -- which is the property that makes "existing
	// revisions are never reinterpreted" true by construction rather than by a compatibility branch. The
	// fixture therefore does what a product change would do: publish revision 2 in the new mode and point
	// this entitlement at it.
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

// EXHAUSTION ENDS ACCESS, at the instant the budget ran out, through the one boundary path.
func TestIntegration_Phase6_ExhaustionTerminatesThroughTheSweep(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// 60 seconds of budget, and ten minutes of observed online time waiting to be charged.
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
	// The instant is when the budget ran out -- roughly nine minutes ago, since 60 of the ten observed
	// minutes were all it had -- NOT when this sweep ran.
	if age := time.Since(got.At); age < 7*time.Minute {
		t.Fatalf("the ending was dated %s ago; it should be dated when the budget ran out", age)
	}

	// Durable state: terminated, capped, and the access actually revoked.
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

	// WHICH time rule ran out is answerable, without new terminal vocabulary.
	var cause string
	if err := p.QueryRow(ctx,
		`SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`,
		ent).Scan(&cause); err != nil {
		t.Fatalf("no termination evidence: %v", err)
	}
	if cause != "AGGREGATE_ONLINE_TIME_EXHAUSTED" {
		t.Fatalf("the evidence says %q", cause)
	}

	// Idempotent: a second sweep changes nothing and reports nothing.
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

// THE OUTER WINDOW STILL WINS WHEN IT COMES FIRST, and produces its own evidence. An aggregate entitlement
// has two clocks; whichever runs out first ends it, exactly once.
func TestIntegration_Phase6_OuterWindowEndsAnAggregateEntitlement(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// A budget it is nowhere near spending, and a window that elapsed an hour ago.
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

// ---- first terminal condition: DATA against aggregate TIME ------------------------------------------------
//
// The sweep now evaluates three terminal conditions and the contract says the FIRST reached ends the
// entitlement, once. These prove both orderings against real accounting rows, and prove that the losing
// condition contributes nothing -- not consumption, not a watermark, not evidence.

// seedDataCrossing gives the entitlement a data quota and enough attributed usage to have crossed it at
// `ago`, through the same accounting_records the sweep's own crossing query reads.
func seedDataCrossing(t *testing.T, p *pgxpool.Pool, f fixture, ent, ses string, quota int64, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	// Plan revisions are IMMUTABLE, so the quota arrives the only way it can: a new revision carrying it,
	// with the entitlement pointed at it -- the same thing publishing a changed plan would do.
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
	// One sample, at the instant the quota was crossed. The sweep derives the crossing from the running sum
	// over attributed samples, so this is the same evidence a real counter would have produced.
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

// DATA CROSSED FIRST: data wins, and aggregate accounting stops exactly at the crossing.
func TestIntegration_Phase6_DataCrossingBoundsAggregateAccrual(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// A budget it could not spend in the observed time, so nothing but DATA can end it.
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

	// ACCRUAL STOPPED AT THE CROSSING. Ten of the thirty observed minutes preceded it, so ~600 seconds are
	// billable and the remaining twenty minutes are not: they are after the guest's access ended.
	status, reason, consumed, _ := terminalState(t, p, ent)
	if status != "TERMINATED" || reason != "DATA" {
		t.Fatalf("durable state is %s/%s", status, reason)
	}
	if consumed < 570 || consumed > 630 {
		t.Fatalf("aggregate consumption is %ds; only the ~600s before the data crossing were billable", consumed)
	}
	// ...and the watermark did not move past it either.
	var wm time.Time
	if err := p.QueryRow(ctx,
		`SELECT accounted_through FROM iam_v2.session_online_watermarks WHERE session_id=$1`, ses).Scan(&wm); err != nil {
		t.Fatal(err)
	}
	if time.Since(wm) < 19*time.Minute {
		t.Fatalf("the watermark advanced to %s ago, past the data crossing", time.Since(wm))
	}
	// No TIME evidence was written: the minutes did not run out.
	var timeEvidence int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`, ent).Scan(&timeEvidence); err != nil {
		t.Fatal(err)
	}
	if timeEvidence != 0 {
		t.Fatal("a DATA termination recorded time-termination evidence")
	}

	// A later sweep changes nothing at all.
	before := consumed
	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	_, _, after, _ := terminalState(t, p, ent)
	if after != before {
		t.Fatalf("a later sweep changed consumption from %d to %d", before, after)
	}
}

// THE OPPOSITE ORDERING: the budget runs out before the data quota is reached, so TIME wins and carries its
// own evidence.
func TestIntegration_Phase6_AggregateExhaustionBeatsALaterDataCrossing(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// 60 seconds of budget against thirty observed minutes: the budget ran out ~29 minutes ago. The data
	// quota is crossed only 5 minutes ago -- later.
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

// ---- both conditions already elapsed: the EARLIEST wins ---------------------------------------------------

// DATA at T1, the outer window at T2 > T1, swept after both. The candidate query used to answer "the window
// is elapsed, so TIME at T2" without comparing the two instants -- which dated the ending an hour late, for
// the wrong reason, and handed the aggregate tick T2 as its billing ceiling so the guest was charged for a
// stretch during which their access had already ended.
func TestIntegration_Phase6_EarliestOfDataAndWindowWins_DataFirst(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 60*time.Minute)
	seedDataCrossing(t, p, f, ent, ses, 1_000_000, 45*time.Minute) // T1
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET window_ends_at = now() - interval '20 minutes' WHERE id=$1`, ent); err != nil {
		t.Fatal(err) // T2, later than T1, and already past when the sweep runs
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

	// ACCOUNTING STOPPED AT THE DATA CROSSING, not at the window and not at the sweep. Fifteen of the sixty
	// observed minutes preceded T1.
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
	// THE LOSING CONDITION LEFT NO EVIDENCE. No time-termination row may claim the window ended it.
	var evidence int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`, ent).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != 0 {
		t.Fatal("a DATA termination wrote time-termination evidence")
	}

	// Repeated sweeps change nothing.
	if _, err := New(p).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	_, _, after, _ := terminalState(t, p, ent)
	if after != consumed {
		t.Fatalf("a later sweep moved consumption from %d to %d", consumed, after)
	}
}

// The mirror image: the window elapsed FIRST and the data crossing came later. TIME must win, with the
// window's own evidence, and consumption must stop at the window.
func TestIntegration_Phase6_EarliestOfDataAndWindowWins_WindowFirst(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 60*time.Minute)
	seedDataCrossing(t, p, f, ent, ses, 1_000_000, 20*time.Minute) // later
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.entitlements SET window_ends_at = now() - interval '45 minutes' WHERE id=$1`, ent); err != nil {
		t.Fatal(err) // earlier
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

// ---- the COMPLETE sweep, run as svc_acctd ------------------------------------------------------------------
//
// WHY A CATALOG AUDIT WAS NOT ENOUGH. Migration 0039 proved svc_acctd may execute the accrual tick and may
// NOT execute terminate_entitlement_at_boundary or p6_record_time_termination. Both facts are true and both
// are wanted -- and together they made the real sweep impossible to run as that role, because the Go path
// called those very operations three statements after the tick, plus two direct UPDATEs. A privilege check
// measures what a role MAY do; only running the whole flow as the role measures whether it CAN do its job.
//
// So these tests connect AS svc_acctd and run EnforceExpiries end to end: the candidate reads, the aggregate
// tick, the first-terminal selection, the terminal transition, the Phase-6 evidence and the revocation. Then
// they check the other half of least privilege -- that the same role still cannot do anything else.

// acctdPool returns a pool whose every connection has already become svc_acctd. The role switch happens at
// connect time, so nothing in the flow runs as the owner by accident.
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

// A TIME exhaustion, performed entirely by the accounting role.
func TestIntegration_Phase6_TheWholeSweepRunsAsSvcAcctd_TimeExhaustion(t *testing.T) {
	owner := pool(t) // fixtures are built by the owner; the SWEEP is what must run as svc_acctd
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

// A DATA termination, likewise -- a different reason through the same writer.
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

// THE OTHER HALF OF LEAST PRIVILEGE. Being able to run the sweep must not mean being able to do anything
// else: every one of these is a capability the accounting role must NOT have gained.
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

	// ...and the sanctioned writer takes no reason and no instant to argue about: it refuses a HEALTHY
	// entitlement outright, because it establishes the condition itself. The boundary is in the function,
	// not in the caller's good manners.
	if _, err := acctd.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Error("svc_acctd ended a healthy entitlement through the expiry writer")
	}
	refused("establish the terminal condition itself",
		`SELECT * FROM iam_v2.p6_due_terminal($1)`, ent)
	// The entitlement is untouched by all of that.
	status, _, _, _ := terminalState(t, owner, ent)
	if status == "TERMINATED" {
		t.Fatal("one of the refused operations went through")
	}
}

// ---- self-review: what removing the candidate lock could have broken -------------------------------------
//
// Dropping FOR UPDATE from the candidate read was necessary (it demands UPDATE on entitlements, which the
// accounting role must never hold) but it is exactly the kind of change that trades a visible privilege
// problem for an invisible concurrency one. So these attack it directly, rather than asserting that it looks
// fine.

// CONCURRENT SWEEPS MUST PRODUCE ONE TERMINAL TRANSITION. Without the candidate lock, every sweep sees the
// same expired entitlement; only the writer's own row lock and its idempotence stand between that and a
// double ending, double consumption or two evidence rows.
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
			<-start // release them together, so they genuinely overlap
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
		// A serialization failure is an acceptable outcome for a loser; a wrong answer is not.
		if err != nil && !strings.Contains(err.Error(), "SQLSTATE 40001") && !strings.Contains(err.Error(), "deadlock") {
			t.Fatalf("sweep %d failed: %v", i, err)
		}
	}

	// EXACTLY ONE terminal transition in the durable record, whatever the sweeps each reported.
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

// CONCURRENT ACCRUAL MUST NOT DOUBLE-CHARGE. Same shape, but with a budget nobody can exhaust, so what is
// measured is the arithmetic rather than the termination: six overlapping ticks over one observed interval
// must charge that interval once.
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
	// Ten observed minutes, charged once. Six sweeps charging it each would be 3600.
	if consumed < 590 || consumed > 615 {
		t.Fatalf("six concurrent sweeps charged %ds for a ten-minute interval", consumed)
	}
}

// A RESTART CHARGES NOTHING EXTRA. A fresh Enforcer over the same durable state is what a service restart
// looks like from the database's side, and the watermark is what makes it a no-op.
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
	// "restart": a brand-new Enforcer, new transaction, same rows.
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

// ---- safe operational disable semantics -------------------------------------------------------------------
//
// THE FAILURE THIS PREVENTS IS WORSE THAN THE FEATURE BEING OFF. If accrual were gated by the Phase-6
// aggregate flag, an appliance that had already granted aggregate entitlements and then lost the flag -- a
// rollback, a config change, a half-applied deployment -- would stop consuming their budgets. Nothing would
// exhaust. A guest holding a finite two-hour package would silently hold unlimited access, and the durable
// record would show consumption frozen with no evidence that anything had stopped.
//
// So the flag gates ACQUISITION and the SURFACES; accrual is data-driven. These prove both halves: an
// appliance with no aggregate entitlements is untouched, and one that HAS them keeps accounting for them.

// A live aggregate entitlement keeps consuming and still exhausts, with no Phase-6 flag set anywhere in the
// environment. This is the regression that would fail if accrual were ever re-gated on the flag.
func TestIntegration_Phase6_DisablingTheCapabilityDoesNotMakeLiveAccessUnlimited(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// Granted earlier, while the capability was on: 60 seconds of budget, half an hour observed.
	f, ent, ses := seedAggregate(t, p, 60, 30*time.Minute)

	// The environment now has NO Phase-6 flags at all -- which is what a rollback looks like.
	for _, k := range []string{"STAYCONNECT_PHASE6_MASTER", "STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"} {
		t.Setenv(k, "")
	}
	// acctd's wiring, reproduced: the bound comes from the tick interval, and the flag does NOT decide
	// whether accrual happens.
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

// THE OTHER HALF: an appliance with no aggregate entitlements is untouched, which is what dark means in
// practice. Every appliance today is this one, because the acquisition gate makes creating such an
// entitlement impossible while the capability is off.
func TestIntegration_Phase6_NoAggregateEntitlementsMeansNoWritesAtAll(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	// An ordinary VALIDITY_WINDOW fixture with a live session -- the shape of every appliance today.
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

// ---- the writer establishes the condition itself ----------------------------------------------------------
//
// THE AUTHORIZATION QUESTION IS NOT "is this reason spelled correctly". Until now the writer accepted an
// entitlement id, an instant and a reason, and checked only the vocabulary -- so a role holding EXECUTE could
// end ANY live entitlement it could name by calling it DATA. That is generic destructive termination
// authority wearing an approved label.
//
// The writer now takes only an id and derives the condition from authoritative state. These tests are the
// proof, and they are written from the attacker's side: every one of the refusals below is a real
// entitlement, healthy, whose id svc_acctd knows.

// seedValidityWindow builds an ordinary healthy VALIDITY_WINDOW entitlement with a live session.
func seedValidityWindow(t *testing.T, p *pgxpool.Pool, window *time.Time) (fixture, string, string) {
	t.Helper()
	f := seed(t, p, 1000, 1000, 0)
	ent, ses := grant(t, p, f, window, time.Now().Add(-30*time.Minute))
	return f, ent, ses
}

// A HEALTHY VALIDITY_WINDOW ENTITLEMENT CANNOT BE ENDED, by anyone holding the writer.
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

// A HEALTHY AGGREGATE ENTITLEMENT -- budget intact, window open -- likewise.
func TestIntegration_Phase6_WriterRefusesAnUnexhaustedAggregateEntitlement(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	_, ent, _ := seedAggregate(t, owner, 86400, 5*time.Minute) // hours of budget, minutes observed
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

// AN ENTITLEMENT WITH A DATA QUOTA IT HAS NOT CROSSED cannot be ended as DATA either -- the writer computes
// the crossing rather than believing in one.
func TestIntegration_Phase6_WriterRefusesWhenTheQuotaIsNotCrossed(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, owner, 86400, 5*time.Minute)
	// A quota of a gigabyte, and one small sample nowhere near it.
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

// THE POSITIVE CONTROLS, one per condition. A refusal test proves nothing unless the same role, through the
// same call, can end something that genuinely IS due -- and is given the right reason and instant.
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
		// The tick is what stamps the crossing instant and caps consumption; the writer then finds it.
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

// THE WRITER IS ATOMIC WITH ITS CALLER. If the caller's transaction does not commit, neither the transition
// nor the revocation may survive -- an entitlement recorded as ended whose devices are still authorized would
// keep forwarding traffic for access the record says is over.
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
	// The caller fails after the write, which is the case that matters: a crash between the two.
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

// seedDataQuotaOnly gives the entitlement a quota it has NOT crossed, plus one small sample -- the shape of
// an ordinary guest partway through their allowance.
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

// ---- restore from backup: an OLDER watermark -------------------------------------------------------------
//
// A RESTART IS NOT A RESTORE, and conflating them is how a system that looks idempotent starts charging twice.
// A restart keeps every durable row: the watermark still says what was already charged, so the next tick
// charges nothing. A RESTORE rewinds the database -- the watermark goes backwards, consumption goes
// backwards, and the interval between the restored point and now is time the system has NO record of and NO
// evidence about. It may have been delivered; it may not.
//
// The plan's rule for uncertain intervals is to fail toward the guest: rebaseline, record what was skipped,
// undercharge. Never charge time nobody observed, and never charge an interval twice because a backup made it
// look unaccounted.
//
// The bound is what enforces that, so this test uses the bound acctd actually computes (a minute for the
// default one-second tick) rather than a generous test value -- a proof that only holds at 86400 would be a
// proof about the test.
func TestIntegration_Phase6_RestoreToAnOlderWatermarkDoesNotRecharge(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 86400, 20*time.Minute)

	const bound = 60 // what aggregateChargeBoundSeconds gives for the default tick interval

	// Normal operation: the sweep charges what it observed, bounded.
	if _, err := New(p).WithAggregateOnlineTime(bound).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var beforeRestore int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&beforeRestore); err != nil {
		t.Fatal(err)
	}

	// THE RESTORE. A backup taken fifteen minutes ago is put back: the watermark and the consumption both
	// rewind. This is done by replacing the rows, exactly as restoring a dump does -- the monotonic trigger
	// guards UPDATEs by a running system, and a restore is not one.
	// The CONSUMPTION counter cannot be rewound in place, and that is a pre-existing invariant worth stating
	// here rather than working around: a decrease is only expressible through entitlement_adjustments, which
	// is audited. A physical restore replaces the whole file and runs no trigger; what it leaves behind that
	// this system can observe is a watermark pointing into the past, which is exactly what drives charging
	// and therefore exactly what this test rewinds.
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

	// The sweep runs against the restored state.
	if _, err := New(p).WithAggregateOnlineTime(bound).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}

	var afterRestore int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&afterRestore); err != nil {
		t.Fatal(err)
	}
	// AT MOST ONE BOUND. The fifteen unaccounted minutes are not evidence of anything, so they are not
	// charged -- the guest is under-charged, which is the direction that cannot be undone unfairly.
	if afterRestore > restoredConsumption+bound+2 {
		t.Fatalf("a restore charged %ds for an interval nobody observed (bound is %ds)",
			afterRestore-restoredConsumption, bound)
	}
	// ...and what was not charged is RECORDED, so the under-charge is visible rather than silent.
	skippedAfter := countRows(t, p, `SELECT count(*) FROM iam_v2.online_time_skipped_intervals WHERE entitlement_id=$1`, ent)
	if skippedAfter <= skippedBefore {
		t.Fatal("the unobserved post-restore interval was neither charged nor recorded as skipped")
	}

	// A SECOND SWEEP DOES NOT CATCH UP. The watermark moved to the ceiling, so the skipped interval stays
	// skipped instead of being charged one bound at a time on every subsequent tick.
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

// A RUNNING SYSTEM STILL CANNOT REWIND A WATERMARK. The restore above works by replacing rows, which is what
// a dump does; an UPDATE that moves one backwards is a bug or an attack and stays refused.
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

// A CRASH BETWEEN THE STAMP AND THE TERMINATION MUST NOT STRAND THE ENTITLEMENT. The tick stamps the
// crossing instant durably and reports it; if the process dies before the writer runs, the stamp is all that
// survives -- and the next sweep has to find it. This runs a sweep with NO accrual at all (the tick disabled
// entirely, which is what a crashed or misconfigured accrual arm looks like) and proves the stamped
// entitlement is still ended, at its original instant.
func TestIntegration_Phase6_AStrandedExhaustionIsFoundByTheNextSweep(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, _ := seedAggregate(t, p, 60, 30*time.Minute)

	// Stamp it the way the tick does, then leave it live -- the state a crash between the two would leave.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.entitlements
		   SET consumed_online_seconds = 60, online_time_exhausted_at = now() - interval '20 minutes'
		 WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}

	// A sweep with the accrual arm OFF: only the candidate query and the writer run.
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

// ---- an exhaustion instant must be provable ---------------------------------------------------------------
//
// The dangerous state is an aggregate entitlement whose consumption has reached the budget with no stamp: an
// audited adjustment, or a restore that carried the counter forward. Both the tick and the writer used to
// fall back to now(), which writes a historical fact nobody observed -- and then the termination, the
// evidence row and every session end time inherit the invention.

// seedExhaustedWithoutAStamp puts consumption at the budget through the audited adjustment path, optionally
// WITHOUT recording the adjustment -- the undatable case.
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

// UNDATABLE EXHAUSTION IS NOT A TERMINATION. Nothing may be claimed, stamped or evidenced.
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
		if x.EntitlementID == ent {
			t.Fatalf("an exhaustion nobody can date was turned into a %s termination at %s", x.Reason, x.At)
		}
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
	// The writer refuses it directly too, for the same reason.
	if _, err := p.Exec(ctx, `SELECT * FROM iam_v2.p6_expire_entitlement($1)`, ent); err == nil {
		t.Fatal("the writer terminated an entitlement whose exhaustion cannot be dated")
	}
	_ = ses
}

// ...AND A DATABLE ONE IS. The audited adjustment is the evidence, and its own timestamp is the instant.
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

// ---- the exhaustion instant is the ACTUAL crossing ---------------------------------------------------------
//
// Taking the earliest related adjustment answers "when did anything relevant happen", which is a different
// question. An entitlement corrected harmlessly on Monday and actually spent on Friday would be dated
// Monday, and four days of real access would be recorded as having happened after the entitlement ended.
// Backdating is not a smaller error than inventing now(); it is the same error pointing the other way, and it
// is harder to spot because the instant looks like evidence.

// adjust records an audited counter change with its own before/after and instant, which is the only way
// either counter moves outside accrual.
func adjust(t *testing.T, p *pgxpool.Pool, f fixture, ent, field string, from, to int64, ago time.Duration) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.entitlement_adjustments
		 (tenant_id, site_id, entitlement_id, field, old_value, new_value, reason, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,'TEST_ADJUSTMENT', now() - $7::interval)`,
		f.tenant, f.site, ent, field, fmt.Sprint(from), fmt.Sprint(to), ago.String()); err != nil {
		t.Fatalf("adjustment: %v", err)
	}
}

// adjustRaw is the same audited record for fields whose values are not numbers.
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

// AN EARLIER, NON-EXHAUSTING ADJUSTMENT MUST NOT BE THE ANSWER.
func TestIntegration_Phase6_CrossingIsTheAdjustmentThatActuallyExhausted(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute) // 600s budget
	// Monday: a harmless correction, nowhere near the budget.
	adjust(t, p, f, ent, "consumed_online_seconds", 0, 100, 4*time.Hour)
	// Friday: the adjustment that actually spends it.
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

	// ...and the sweep ends it there, not four hours earlier.
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

// A BUDGET CUT IS A CROSSING TOO, and is dated at the cut rather than at whenever consumption last moved.
func TestIntegration_Phase6_LoweringTheBudgetUnderConsumptionIsTheCrossing(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	adjust(t, p, f, ent, "consumed_online_seconds", 0, 500, 3*time.Hour) // still under 600
	if _, err := p.Exec(context.Background(),
		`UPDATE iam_v2.entitlements SET consumed_online_seconds = 500 WHERE id=$1`, ent); err != nil {
		t.Fatal(err)
	}
	// A budget cut is a REPOINT to another immutable revision -- revisions themselves never change -- and the
	// repoint is what the audit records.
	var oldRev, newRev string
	if err := p.QueryRow(context.Background(),
		`SELECT service_plan_revision_id::text FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&oldRev); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(context.Background(), `INSERT INTO iam_v2.service_plan_revisions
		 (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
		  device_limit_policy, time_accounting_mode, time_quota_seconds)
		 SELECT gen_random_uuid(), r.tenant_id, r.site_id, r.service_plan_id, r.revision_no + 10,
		        r.down_kbps, r.up_kbps, r.max_concurrent_devices, r.device_limit_policy,
		        r.time_accounting_mode, 400
		   FROM iam_v2.service_plan_revisions r WHERE r.id = $1 RETURNING id::text`, oldRev).Scan(&newRev); err != nil {
		t.Fatalf("publish the smaller revision: %v", err)
	}
	adjustRaw(t, p, f, ent, "service_plan_revision_id", oldRev, newRev, 20*time.Minute)
	if _, err := p.Exec(context.Background(),
		`UPDATE iam_v2.entitlements SET service_plan_revision_id=$2 WHERE id=$1`, ent, newRev); err != nil {
		t.Fatal(err)
	}
	at := exhaustionInstant(t, p, ent)
	if at == nil {
		t.Fatal("a budget cut below existing consumption was not recognised as a crossing")
	}
	if age := time.Since(*at); age < 15*time.Minute || age > 25*time.Minute {
		t.Fatalf("dated %s ago; the crossing was the budget cut 20 minutes ago", age)
	}
}

// A HISTORY THAT NEVER CROSSES LEAVES IT UNDATABLE, however exhausted the entitlement looks now.
func TestIntegration_Phase6_UnaccountedExhaustionStaysUndatable(t *testing.T) {
	p := pool(t)
	f, ent, _ := seedAggregate(t, p, 600, 5*time.Minute)
	// An adjustment that leaves it well below budget, and then a consumption value nothing explains.
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
		if x.EntitlementID == ent {
			t.Fatalf("it was terminated anyway, at %s", x.At)
		}
	}
	status, _, _, _ := terminalState(t, p, ent)
	if status == "TERMINATED" {
		t.Fatal("terminated on an instant nothing supports")
	}
}

// THE TICK'S OWN STAMP ALWAYS WINS. It was computed inside the tick that crossed, which is better evidence
// than any reconstruction, and a later adjustment must not move it.
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
	adjust(t, p, f, ent, "consumed_online_seconds", 60, 60, 90*time.Minute) // an older, irrelevant record
	at := exhaustionInstant(t, p, ent)
	if at == nil || !at.Equal(stamped) {
		t.Fatalf("the stamp %s was overridden by adjustment history (%v)", stamped, at)
	}
}
