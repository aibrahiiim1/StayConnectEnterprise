//go:build integration && phase6

package enforce

import (
	"context"
	"testing"
	"time"

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
