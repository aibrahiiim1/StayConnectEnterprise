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
		"iam_v2.begin_payment_execution(uuid)":             true,
		"iam_v2.p4_resolve_payment_account(uuid,uuid)":     true,
		"iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid)": true,
		"iam_v2.p4_financial_recovery_active(uuid,uuid)":   true,
		// Reviewed (0023). It reads two signals and compares them; the ONLY outcome it can produce is more
		// holding. Nothing in it releases an epoch, resolves a hold or unholds a rail, which is what makes
		// accepting a caller-supplied marker generation safe -- see the abuse test below.
		"iam_v2.p4_reconcile_financial_epoch_v2(uuid,uuid,text,bigint,boolean)": true,
		// Reviewed (0023). Read-only: it returns a bigint and writes nothing.
		"iam_v2.p4_current_restore_generation(uuid,uuid)": true,
		"iam_v2.begin_controlled_operation(text)":         true,
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

// THE OUTCOME-AUTHORITY SPLIT (0024).
//
// T0039 recorded a residual: the execution credential could still lie about a payment it started, because
// the same role both executed payments and asserted their outcomes. One stolen DSN was therefore sufficient
// to fabricate money end to end. 0024 removes that: asserting an outcome is a different authority.
//
// This is the adversarial proof of the split, from both sides.
func TestIntegrationDefinerAbuse_TheExecutionRoleCannotAssertAnOutcome(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	op := outcomePool(t)
	ctx := context.Background()
	s := seedPaidChain(t, owner)

	// The execution role does everything up to the boundary...
	e := NewEngineWithOutcome(liveCfg, rp, op, NewScriptedProvider(Result{Outcome: OutcomeCaptured}),
		SQLGranter{Pool: rp})
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatalf("the execution role could not create an intent: %v", err)
	}
	if err := beginExecution(t, rp, in.ID); err != nil {
		t.Fatalf("the execution role could not cross the durable boundary: %v", err)
	}

	// ...and then cannot say what happened. This is the property the split exists for: a stolen execution
	// credential can start payments and stop there.
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,'forged','x','CAPTURED',NULL,'{}'::jsonb)`,
		in.ClientRef); err == nil {
		t.Fatal("the execution credential asserted a provider outcome")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("refused, but not by privilege: %v", err)
	}
	if st := scan1[string](t, owner, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "IN_PROGRESS" {
		t.Fatalf("the settlement moved to %s without an outcome authority", st)
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 0 {
		t.Fatal("a payment with no asserted outcome granted access")
	}

	// The OUTCOME credential can assert it -- so the split discriminates rather than simply blocking.
	if _, err := op.Exec(ctx,
		`SELECT iam_v2.p4_apply_provider_outcome($1,'evt-legit','x','CAPTURED',NULL,'{}'::jsonb)`,
		in.ClientRef); err != nil {
		t.Fatalf("the outcome credential could not assert an outcome: %v", err)
	}
	if st := scan1[string](t, owner, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "SETTLED" {
		t.Fatalf("the sanctioned outcome path did not settle: %s", st)
	}
}

// And the mirror: a stolen OUTCOME credential has nothing of its own to assert about.
func TestIntegrationDefinerAbuse_TheOutcomeRoleCannotStartOrGrantAnything(t *testing.T) {
	owner := pool(t)
	op := outcomePool(t)
	ctx := context.Background()
	s := seedPaidChain(t, owner)

	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"create a payment intent",
			`INSERT INTO iam_v2.payment_transactions
			   (tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,
			    idempotency_key,amount_minor,currency,currency_exponent,status)
			 VALUES ($1,$2,$3,$4,'CHARGE','test-double','sc_forged','idem-forged',100,'USD',2,'CREATED')`,
			[]any{s.tenant, s.site, s.settlement, s.merchant}},
		{"cross the durable execution boundary",
			`SELECT iam_v2.begin_payment_execution(gen_random_uuid())`, nil},
		{"grant an entitlement",
			`SELECT * FROM iam_v2.p4_grant_paid_entitlement($1::uuid,$2::uuid,$3::uuid)`,
			[]any{s.tenant, s.site, s.settlement}},
		{"grant a free entitlement",
			`SELECT * FROM iam_v2.p4_grant_quoted_entitlement($1::uuid,$2::uuid,$3::uuid)`,
			[]any{s.tenant, s.site, s.purchase}},
		{"call the grant kernel directly",
			`SELECT * FROM iam_v2.p4_entitlement_grant_kernel($1::uuid,$2::uuid,$3::uuid,gen_random_uuid(),
			   NULL,NULL,'{"version":1,"service_plan_revision_id":"00000000-0000-0000-0000-000000000001"}'::jsonb,
			   gen_random_uuid(),gen_random_uuid())`,
			[]any{s.tenant, s.site, s.purchase}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := op.Exec(ctx, tc.sql, tc.args...); err == nil {
				t.Fatalf("the outcome credential could %s", tc.name)
			}
		})
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 0 {
		t.Fatal("the outcome credential created an entitlement")
	}
}

// The GRANT KERNEL is the only thing in the schema that writes an entitlement, and no runtime role may
// call it directly -- only the two high-level entry points, each with its own authorization.
func TestIntegrationDefinerAbuse_TheGrantKernelIsUnreachableFromEveryRuntimeRole(t *testing.T) {
	owner := pool(t)
	ctx := context.Background()
	for _, role := range []string{"sc_payment_runtime", "sc_commerce_runtime", "sc_payment_outcome",
		"sc_financial_operator", "sc_financial_readonly"} {
		var can bool
		if err := owner.QueryRow(ctx,
			`SELECT has_function_privilege($1,
			   'iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid)','EXECUTE')`,
			role).Scan(&can); err != nil {
			t.Fatal(err)
		}
		if can {
			t.Errorf("%s can call the grant kernel directly, bypassing every authorization", role)
		}
	}
	// and the free entry point is not reachable from the PAYMENT runtime, nor the paid one from commerce
	for _, tc := range []struct{ role, fn string }{
		{"sc_payment_runtime", "iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid)"},
		{"sc_commerce_runtime", "iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid)"},
		{"sc_commerce_runtime", "iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb)"},
	} {
		var can bool
		if err := owner.QueryRow(ctx,
			`SELECT has_function_privilege($1,$2,'EXECUTE')`, tc.role, tc.fn).Scan(&can); err != nil {
			t.Fatal(err)
		}
		if can {
			t.Errorf("%s can call %s, which belongs to a different service", tc.role, tc.fn)
		}
	}
}

// The reconciliation entry point accepts a marker generation from its caller. That is safe for one reason
// and one reason only: the worst a liar can do is hold their own site.
func TestIntegrationDefinerAbuse_TheMarkerParameterCannotBeUsedToUNHOLD(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	ctx := context.Background()
	s := recoverySite(t, owner)
	e := NewEngine(liveCfg, owner, NewScriptedProvider(), &fakeGranter{})

	// A caller claiming a HIGHER generation puts its own site into recovery. Undesirable, not dangerous.
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.p4_reconcile_financial_epoch_v2($1::uuid,$2::uuid,'whatever',9999,true)`,
		s.tenant, s.site); err != nil {
		t.Fatalf("the reconciliation entry point errored: %v", err)
	}
	if active, _ := e.RecoveryActive(ctx, s.tenant, s.site); !active {
		t.Fatal("a claimed marker generation did not hold the site")
	}

	// Now the important half: no claim can get back OUT. Zero, absent, enormous -- none of them release.
	for _, tc := range []struct {
		name    string
		gen     int64
		present bool
	}{
		{"claiming generation zero", 0, true},
		{"claiming no marker at all", 0, false},
		{"claiming an enormous generation", 1 << 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rp.Exec(ctx,
				`SELECT iam_v2.p4_reconcile_financial_epoch_v2($1::uuid,$2::uuid,'whatever',$3,$4)`,
				s.tenant, s.site, tc.gen, tc.present); err != nil {
				t.Fatalf("errored: %v", err)
			}
			if active, _ := e.RecoveryActive(ctx, s.tenant, s.site); !active {
				t.Fatalf("%s released the hold", tc.name)
			}
		})
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.financial_epochs
		WHERE tenant_id=$1 AND site_id=$2 AND released_at IS NOT NULL AND reason <> 'INITIAL'`,
		s.tenant, s.site); n != 0 {
		t.Fatal("a claimed marker generation released an epoch")
	}
	// and the runtime cannot record a supported restore either: that needs a verified manifest, and the
	// verification happens in the restore tool, not here.
	if _, err := rp.Exec(ctx,
		`SELECT iam_v2.p4_record_supported_restore($1::uuid,$2::uuid,99,repeat('a',64),now(),'forged')`,
		s.tenant, s.site); err == nil {
		t.Fatal("the payment runtime recorded a supported restore")
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
