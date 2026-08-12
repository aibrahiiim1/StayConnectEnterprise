//go:build integration

package payment

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// THE DEFINER-ABUSE MATRIX.
//
// T0038 proved the restricted runtime holds no direct table DML and reported that honestly. It was not
// enough. A SECURITY DEFINER function is a privilege escalation by design, so a role that cannot write a
// table but CAN call the function that writes it has not been restricted -- it has been given a different
// door. Every grant of a low-level primitive is therefore a grant of whatever that primitive can do.
//
// These tests attack through the door. For every SECURITY DEFINER function the runtime role can execute,
// they attempt the outcome an attacker would actually want, as the real restricted role:
//
//   * manufacture a CAPTURED outcome without an authenticated provider notification;
//   * create an entitlement by calling a low-level helper directly;
//   * mark an unpaid purchase GRANTED;
//   * substitute tenant, site, subject, package or policy evidence;
//   * bypass the SETTLED / AWAITING_SETTLEMENT gates.
//
// The first assertion is structural and matters most: the ENUMERATION test below fails if the role is ever
// granted a primitive again, so this matrix cannot silently stop covering the surface.

// executableDefiners lists every SECURITY DEFINER function the given role may call.
func executableDefiners(t *testing.T, role string) []string {
	t.Helper()
	rows, err := pool(t).Query(context.Background(), `
SELECT p.oid::regprocedure::text
  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'iam_v2' AND p.prosecdef
   AND has_function_privilege($1, p.oid, 'EXECUTE')
 ORDER BY 1`, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sig string
		if err := rows.Scan(&sig); err != nil {
			t.Fatal(err)
		}
		out = append(out, sig)
	}
	return out
}

// The surface is CLOSED. If a future change grants the runtime another definer function, this fails and
// whoever made the change has to decide, explicitly, what that function lets a compromised role do.
func TestIntegrationDefinerAbuse_TheRuntimeDefinerSurfaceIsExactlyWhatWeThinkItIs(t *testing.T) {
	_ = runtimePool(t) // skip cleanly when the restricted matrix is not configured
	got := executableDefiners(t, "sc_payment_runtime")
	want := map[string]bool{
		"iam_v2.begin_payment_execution(uuid)":                             true,
		"iam_v2.p4_resolve_payment_account(uuid,uuid)":                     true,
		"iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid)":                 true,
		"iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb)": true,
		"iam_v2.p4_financial_recovery_active(uuid,uuid)":                   true,
		"iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text)":              true,
		"iam_v2.begin_controlled_operation(text)":                          true,
	}
	for _, sig := range got {
		if !want[sig] {
			t.Errorf("the payment runtime can execute an UNREVIEWED definer function: %s\n"+
				"Every such grant hands a compromised role whatever that function can do. Either remove the "+
				"grant or add it here with an abuse test that shows what it cannot be used for.", sig)
		}
		delete(want, sig)
	}
	for sig := range want {
		t.Errorf("an expected runtime operation is missing: %s", sig)
	}
	// the primitives specifically must be gone
	for _, forbidden := range []string{
		"apply_payment_callback_v2", "p4_insert_entitlement",
		"p4_terminate_live_entitlement_for_subject", "p4_mark_purchase_granted",
		"apply_entitlement_transition", "record_posting_review_action",
	} {
		for _, sig := range got {
			if strings.Contains(sig, forbidden) {
				t.Errorf("the payment runtime still holds the low-level primitive %s", sig)
			}
		}
	}
}

// Free access on demand, attempted through every route the role still has.
func TestIntegrationDefinerAbuse_CannotFabricateAnEntitlement(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	assertNotOwner(t, rp)
	ctx := context.Background()
	s := seedPaidChain(t, owner)
	victim := seedPaidChain(t, owner)

	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"call the low-level entitlement writer directly",
			`SELECT iam_v2.p4_insert_entitlement($1::uuid,$2::uuid,NULL,NULL,gen_random_uuid(),$3::uuid,
			   '{"version":1}'::jsonb,gen_random_uuid(),gen_random_uuid(),'VALIDITY_WINDOW','MANUAL_END',
			   NULL,NULL)`,
			[]any{s.tenant, s.site, s.purchase}},
		{"terminate someone else's live entitlement",
			`SELECT iam_v2.p4_terminate_live_entitlement_for_subject($1::uuid,$2::uuid,gen_random_uuid(),NULL,NULL)`,
			[]any{victim.tenant, victim.site}},
		{"mark an unpaid purchase granted",
			`SELECT iam_v2.p4_mark_purchase_granted($1::uuid)`, []any{s.purchase}},
		{"drive an entitlement transition directly",
			`SELECT iam_v2.apply_entitlement_transition(gen_random_uuid(),'ACTIVE',now(),'GRANTED')`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rp.Exec(ctx, tc.sql, tc.args...)
			if err == nil {
				t.Fatalf("the restricted runtime could %s", tc.name)
			}
			if !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("refused, but not by privilege -- the grant may still exist: %v", err)
			}
		})
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements
		WHERE purchase_id IN ($1,$2)`, s.purchase, victim.purchase); n != 0 {
		t.Fatalf("the abuse matrix created %d entitlements", n)
	}
	if st := scan1[string](t, owner, `SELECT state FROM iam_v2.purchases WHERE id=$1`, s.purchase); st == "GRANTED" {
		t.Fatal("an unpaid purchase was marked GRANTED")
	}
}

// The high-level grant operation is the one thing the role CAN call. It must be unusable for anything but
// the case it exists for -- so these are the substitution attempts against the operation itself.
func TestIntegrationDefinerAbuse_TheGrantOperationRefusesEverySubstitution(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	ctx := context.Background()
	a := seedPaidChain(t, owner)
	b := seedPaidChain(t, owner)

	grant := func(tenant, site, settlement string) error {
		_, err := rp.Exec(ctx,
			`SELECT * FROM iam_v2.p4_grant_paid_entitlement($1::uuid,$2::uuid,$3::uuid)`,
			tenant, site, settlement)
		return err
	}

	for _, tc := range []struct{ name, tenant, site, settlement string }{
		{"another tenant's settlement", a.tenant, a.site, b.settlement},
		{"another site of the same tenant", b.tenant, a.site, b.settlement},
		{"an invented settlement", a.tenant, a.site, "11111111-2222-3333-4444-555555555555"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := grant(tc.tenant, tc.site, tc.settlement); err == nil {
				t.Fatalf("the grant operation accepted %s", tc.name)
			}
		})
	}
	// an unsettled settlement, the ordinary case the gate exists for
	if err := grant(a.tenant, a.site, a.settlement); err == nil {
		t.Fatal("the grant operation granted against an unsettled settlement")
	}
	// a PMS_POSTING settlement is the wrong rail entirely
	if _, err := owner.Exec(ctx, `UPDATE iam_v2.settlements SET method='PMS_POSTING' WHERE id=$1`, b.settlement); err == nil {
		if err := grant(b.tenant, b.site, b.settlement); err == nil {
			t.Fatal("the grant operation granted against a PMS posting settlement")
		}
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements
		WHERE purchase_id IN ($1,$2)`, a.purchase, b.purchase); n != 0 {
		t.Fatalf("substitution attempts created %d entitlements", n)
	}
}

// Money out of nothing: the outcome operation must not be usable as a general "assert CAPTURED" primitive.
func TestIntegrationDefinerAbuse_CannotManufactureACapture(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	ctx := context.Background()
	s := seedPaidChain(t, owner)
	e := NewEngine(liveCfg, rp, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), SQLGranter{Pool: rp})

	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}

	// the low-level primitive is simply not callable
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.apply_payment_callback_v2($1::uuid,'test-double',$2::uuid,$3,'forged','x','CAPTURED',NULL,'{}'::jsonb)`,
		s.tenant, s.merchant, in.ClientRef); err == nil {
		t.Fatal("the restricted runtime called the low-level callback primitive")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("refused, but not by privilege: %v", err)
	}

	// the high-level one refuses an intent that never began executing
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,'forged','x','CAPTURED',NULL,'{}'::jsonb)`,
		in.ClientRef); err == nil {
		t.Fatal("a CAPTURED was manufactured for an intent that never executed")
	}
	if st := scan1[string](t, owner, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "REQUIRED" {
		t.Fatalf("the settlement moved to %s without an execution", st)
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 0 {
		t.Fatal("a manufactured capture granted access")
	}

	// and it accepts only the three contractual outcomes, so it is not a general status setter
	if err := beginExecution(t, owner, in.ID); err != nil {
		t.Fatal(err)
	}
	for _, bogus := range []string{"SETTLED", "GRANTED", "PENDING", "CREATED", "EXPIRED"} {
		if _, err := rp.Exec(ctx,
			`SELECT iam_v2.p4_apply_provider_outcome($1,$2,'x',$3,NULL,'{}'::jsonb)`,
			in.ClientRef, "evt-"+bogus, bogus); err == nil {
			t.Fatalf("the outcome operation accepted %q as a provider outcome", bogus)
		}
	}
}

// What a compromised runtime CAN still do, stated as a test so the boundary is documented by measurement
// rather than by assertion. It can lie about the outcome of a payment IT started -- because proving
// provider authenticity requires a signing secret the database does not hold, and the trusted-computing
// boundary for that is the payment process. Bounding the blast radius is the achievable property.
func TestIntegrationDefinerAbuse_TheResidualBlastRadiusIsBoundedAndKnown(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	ctx := context.Background()
	s := seedPaidChain(t, owner)
	e := NewEngine(liveCfg, rp, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), SQLGranter{Pool: rp})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err := beginExecution(t, owner, in.ID); err != nil {
		t.Fatal(err)
	}

	// KNOWN AND ACCEPTED: for a payment already executing, the role can assert an outcome. The database
	// cannot distinguish this from a genuine authenticated notification, and this test records that.
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,'evt-residual','x','CAPTURED',NULL,'{}'::jsonb)`,
		in.ClientRef); err != nil {
		t.Fatalf("the sanctioned path stopped working: %v", err)
	}

	// BOUNDED: it cannot reach any OTHER payment. Every other intent in the estate is untouched, because
	// there is no execution in flight for them.
	others := seedPaidChain(t, owner)
	otherIntent, _ := e.CreateChargeIntent(ctx, others.tenant, others.site, others.settlement, idem(t, "2"))
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,'evt-other','x','CAPTURED',NULL,'{}'::jsonb)`,
		otherIntent.ClientRef); err == nil {
		t.Fatal("the blast radius extends to payments that never began executing")
	}
	if st := scan1[string](t, owner, `SELECT status FROM iam_v2.settlements WHERE id=$1`,
		others.settlement); st != "REQUIRED" {
		t.Fatalf("an unrelated settlement moved to %s", st)
	}
}

// Scope and actor authority: a definer function that writes an audited fact must not let an arbitrary
// caller nominate the tenant, the site or the author.
func TestIntegrationDefinerAbuse_CannotForgeActorOrScope(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	ctx := context.Background()
	a := seedPaidChain(t, owner)

	// The runtime role holds no recovery-decision grant at all -- those belong to the operator.
	for _, sql := range []string{
		`SELECT iam_v2.p4_resolve_recovery_hold(gen_random_uuid(),'CONFIRMED_COMPLETED',gen_random_uuid(),'forged decision text')`,
		`SELECT iam_v2.p4_release_financial_recovery($1::uuid,$2::uuid,gen_random_uuid(),'forged release text')`,
		`SELECT iam_v2.p4_declare_financial_recovery($1::uuid,$2::uuid,gen_random_uuid(),'forged declaration')`,
	} {
		args := []any{}
		if strings.Contains(sql, "$1") {
			args = []any{a.tenant, a.site}
		}
		if _, err := rp.Exec(ctx, sql, args...); err == nil {
			t.Fatalf("the payment runtime performed a recovery decision: %s", sql)
		}
	}

	// And an invented actor is refused even for a caller that IS allowed to record decisions: the
	// author must be a real active operator of the tenant.
	var ok bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n
		ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_assert_financial_actor')`).
		Scan(&ok); err != nil || !ok {
		t.Fatal("the actor assertion does not exist")
	}
	if _, err := owner.Exec(ctx, `SELECT iam_v2.p4_assert_financial_actor($1::uuid, gen_random_uuid())`,
		a.tenant); err == nil {
		t.Fatal("an invented actor was accepted as a financial author")
	}
	if _, err := owner.Exec(ctx, fmt.Sprintf(
		`SELECT iam_v2.p4_assert_financial_actor('%s'::uuid, NULL)`, a.tenant)); err == nil {
		t.Fatal("a NULL actor was accepted as a financial author")
	}
}
