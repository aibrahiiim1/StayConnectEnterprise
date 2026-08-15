//go:build integration && phase6

package enforce

import (
	"context"
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

// WITH THE FLAG OFF NOTHING ACCRUES. This is what "DARK" has to mean for a sweep that every appliance already
// runs: same query, same terminations, and no watermark, no consumption and no evidence anywhere.
func TestIntegration_Phase6_SweepIsUnchangedWhileDark(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f, ent, ses := seedAggregate(t, p, 60, 10*time.Minute)
	tenant, site := f.tenant, f.site

	// The ordinary constructor -- no WithAggregateOnlineTime, which is every caller today.
	due, err := New(p).EnforceExpiries(ctx, tenant, site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, x := range due {
		if x.EntitlementID == ent {
			t.Fatal("a dark sweep terminated an aggregate entitlement")
		}
	}
	var consumed int64
	if err := p.QueryRow(ctx,
		`SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 0 {
		t.Fatalf("a dark sweep charged %d seconds", consumed)
	}
	var wm time.Time
	if err := p.QueryRow(ctx,
		`SELECT accounted_through FROM iam_v2.session_online_watermarks WHERE session_id=$1`, ses).Scan(&wm); err != nil {
		t.Fatal(err)
	}
	if time.Since(wm) < 9*time.Minute {
		t.Fatal("a dark sweep advanced a watermark")
	}
	var evidence int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`, ent).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != 0 {
		t.Fatal("a dark sweep wrote Phase-6 evidence")
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

	// ...and the sanctioned writer refuses the reasons that are not its business, even to the role that may
	// call it. The boundary is in the function, not in the caller's good manners.
	if _, err := acctd.Exec(ctx,
		`SELECT iam_v2.p6_expire_entitlement($1, now(), 'ADMIN', NULL)`, ent); err == nil {
		t.Error("svc_acctd ended an entitlement for ADMIN through the expiry writer")
	}
	if _, err := acctd.Exec(ctx,
		`SELECT iam_v2.p6_expire_entitlement($1, now() + interval '1 hour', 'TIME', NULL)`, ent); err == nil {
		t.Error("the expiry writer accepted a terminal instant in the future")
	}
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

// THE WRITER IS ATOMIC WITH ITS CALLER. If anything after the terminal transition fails, the transition must
// not survive -- an entitlement recorded as ended whose devices are still authorized would keep forwarding
// traffic for access that the record says is over.
func TestIntegration_Phase6_ExpiryWriterRollsBackWholly(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	_, ent, _ := seedAggregate(t, p, 86400, 5*time.Minute)

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A cause the evidence table refuses: the write fails INSIDE the writer, after the transition.
	var devices, sessions int
	err = tx.QueryRow(ctx, `SELECT devices, sessions FROM iam_v2.p6_expire_entitlement($1, now(), 'TIME', $2)`,
		ent, "NOT_A_REAL_CAUSE").Scan(&devices, &sessions)
	if err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("the writer accepted an unknown time cause")
	}
	_ = tx.Rollback(ctx)

	status, _, _, _ := terminalState(t, p, ent)
	if status == "TERMINATED" {
		t.Fatal("the terminal transition survived a failure inside the same writer call")
	}
	var authorized int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND status='AUTHORIZED'`,
		ent).Scan(&authorized); err != nil {
		t.Fatal(err)
	}
	if authorized == 0 {
		t.Fatal("the revocation survived a rolled-back writer call")
	}
}

// A POSITIVE CONTROL FOR THE REFUSAL TEST. Every "svc_acctd cannot do X" assertion passes trivially for a
// role that can do nothing at all -- which is exactly the state the role was in before this work granted it
// USAGE on the schema. So the same role must be shown DOING its sanctioned operation.
func TestIntegration_Phase6_SvcAcctdCanStillDoItsOwnJob(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	_, ent, _ := seedAggregate(t, owner, 86400, 5*time.Minute)
	acctd := acctdPool(t)

	var devices, sessions int
	if err := acctd.QueryRow(ctx,
		`SELECT devices, sessions FROM iam_v2.p6_expire_entitlement($1, now() - interval '1 minute', 'DATA', NULL)`,
		ent).Scan(&devices, &sessions); err != nil {
		t.Fatalf("svc_acctd could not perform its own sanctioned operation: %v", err)
	}
	if sessions == 0 {
		t.Fatal("the sanctioned operation ended no sessions, so the refusals above prove nothing")
	}
	status, reason, _, _ := terminalState(t, owner, ent)
	if status != "TERMINATED" || reason != "DATA" {
		t.Fatalf("durable state after the sanctioned operation: %s/%s", status, reason)
	}
	// ...and re-running it is a no-op rather than a second ending.
	if err := acctd.QueryRow(ctx,
		`SELECT devices, sessions FROM iam_v2.p6_expire_entitlement($1, now() - interval '1 minute', 'DATA', NULL)`,
		ent).Scan(&devices, &sessions); err != nil {
		t.Fatalf("the writer is not idempotent: %v", err)
	}
	if devices != 0 || sessions != 0 {
		t.Fatalf("a repeat call revoked %d devices and %d sessions", devices, sessions)
	}
	var transitions int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlement_state_transitions WHERE entitlement_id=$1 AND to_state='TERMINATED'`,
		ent).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("%d terminal transitions after a repeated call", transitions)
	}
}
