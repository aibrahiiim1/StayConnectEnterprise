//go:build integration

package enforce

// Outage continuity against the real schema (migrations 0198 and 0199, the producer's queries): the setting's bounds and audit,
// the revocation probe netd uses, and the rows the producer derives its budgets from.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIntegration_Continuity_SettingDefaultBoundsAndAudit(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	if got := scalar[int](t, p, `SELECT outage_continuity_minutes FROM public.enforcement_policy`); got != 60 {
		t.Fatalf("default = %d, want 60", got)
	}
	before := scalar[int](t, p, `SELECT count(*)::int FROM public.enforcement_policy_changes WHERE field='outage_continuity_minutes' AND changed_by='test-operator'`)
	for _, v := range []string{"0", "120", "45"} {
		if _, err := p.Exec(ctx, `SELECT public.enforcement_policy_set('outage_continuity_minutes',$1,'test-operator','integration test of the bounds')`, v); err != nil {
			t.Fatalf("value %s refused: %v", v, err)
		}
	}
	for _, v := range []string{"121", "-1", "abc", "", "1000"} {
		_, err := p.Exec(ctx, `SELECT public.enforcement_policy_set('outage_continuity_minutes',$1,'test-operator','integration test of the bounds')`, v)
		if err == nil || !strings.Contains(err.Error(), "between 0 and 120") {
			t.Fatalf("value %q must be refused with the bounds message, got %v", v, err)
		}
	}
	if _, err := p.Exec(ctx, `UPDATE public.enforcement_policy SET outage_continuity_minutes = 121`); err == nil {
		t.Fatal("the table constraint must refuse 121 even for a direct write")
	}
	// Every accepted change is in the append-only history.
	n := scalar[int](t, p, `SELECT count(*)::int FROM public.enforcement_policy_changes WHERE field='outage_continuity_minutes' AND changed_by='test-operator'`) - before
	if n != 3 {
		t.Fatalf("audit rows added = %d, want 3", n)
	}
	if _, err := p.Exec(ctx, `SELECT public.enforcement_policy_set('outage_continuity_minutes','60','test-operator','restore the default')`); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_Continuity_NetdProbeReportsExactlyTheSessionsNoLongerEntitled(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 8000, 3000, 0)
	win := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &win, time.Now().Add(-time.Hour))
	other := seed(t, p, 8000, 3000, 0)
	_, otherSess := grant(t, p, other, &win, time.Now().Add(-time.Hour))

	netd := rolePool(t, "svc_netd")
	ask := func(tenant, site string, ids ...string) []string {
		rows, err := netd.Query(ctx, `SELECT x::text FROM iam_v2.continuity_not_entitled($1::uuid,$2::uuid,$3::uuid[]) x`, tenant, site, ids)
		if err != nil {
			t.Fatalf("svc_netd cannot ask: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	if got := ask(f.tenant, f.site, sess); len(got) != 0 {
		t.Fatalf("an active, entitled session was reported: %v", got)
	}
	unknown := "00000000-0000-0000-0000-00000000dead"
	if got := ask(f.tenant, f.site, sess, unknown); len(got) != 1 || got[0] != unknown {
		t.Fatalf("an unknown session must count as not entitled: %v", got)
	}
	if got := ask(f.tenant, f.site, otherSess); len(got) != 1 {
		t.Fatalf("a session of another tenant/site must count as not entitled: %v", got)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,now(),'ADMIN')`, ent); err != nil {
		t.Fatal(err)
	}
	if got := ask(f.tenant, f.site, sess); len(got) != 1 || got[0] != sess {
		t.Fatalf("a terminated entitlement's session must be reported: %v", got)
	}
	// It agrees with the plan derivation, which is the point of repeating the predicate.
	plan, err := New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Shape) != 0 {
		t.Fatalf("plan and probe disagree: plan still shapes %d", len(plan.Shape))
	}
	// The probe changes nothing and netd holds no table write because of it.
	if _, err := netd.Exec(ctx, `UPDATE iam_v2.sessions SET state='active' WHERE id=$1`, sess); err == nil {
		t.Fatal("svc_netd must not be able to write sessions")
	}
}

func TestIntegration_Continuity_ProducerRowsAndBudget(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	const quota = int64(500 << 20)
	f := seed(t, p, 8000, 3000, quota)
	win := time.Now().Add(2 * time.Hour)
	ent, sess := grant(t, p, f, &win, time.Now().Add(-10*time.Minute))
	if _, err := p.Exec(ctx, `UPDATE iam_v2.entitlements SET consumed_data_bytes = $2 WHERE id=$1`, ent, int64(100<<20)); err != nil {
		// consumed_data_bytes is maintained by the ingestion operation; a direct write may be refused by design.
		t.Skipf("cannot seed consumed bytes directly: %v", err)
	}
	enf := New(p)
	plan, err := enf.PlanForSite(ctx, f.tenant, f.site)
	if err != nil || len(plan.Shape) != 1 {
		t.Fatalf("plan: %v %+v", err, plan)
	}
	asOf := time.Now()
	blk, err := enf.ContinuityFor(ctx, f.tenant, f.site, plan, time.Hour)
	if err != nil || blk == nil {
		t.Fatalf("block: %v", err)
	}
	if len(blk.Sessions) != 1 || blk.Sessions[0].SessionID != sess || blk.Sessions[0].HardEnd == nil {
		t.Fatalf("sessions: %+v", blk.Sessions)
	}
	if blk.Sessions[0].HardEnd.Sub(win) > time.Second || win.Sub(*blk.Sessions[0].HardEnd) > time.Second {
		t.Fatalf("the hard end must be the entitlement window: %v vs %v", blk.Sessions[0].HardEnd, win)
	}
	margin := int64(8000+3000) * 125 * BudgetMarginSeconds
	if len(blk.Budgets) != 1 || blk.Budgets[0].RemainingBytes != quota-int64(100<<20)-margin {
		t.Fatalf("budget %+v, want %d", blk.Budgets, quota-int64(100<<20)-margin)
	}
	if blk.AsOf.After(asOf.Add(time.Second)) || blk.AsOf.Before(asOf.Add(-5*time.Second)) {
		t.Fatalf("as-of %v is not stamped at the read", blk.AsOf)
	}
}
