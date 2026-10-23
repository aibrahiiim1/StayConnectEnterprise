//go:build integration

package payment

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Restricted-role end-to-end proof: other tests connect as the schema owner and prove logic, not privilege. These
// connect as a login role holding only sc_payment_runtime and drive the whole journey; the same role must be unable to
// reach the outcomes any other way.

func runtimePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE4_RUNTIME_DSN")
	if dsn == "" {
		t.Skip("PHASE4_RUNTIME_DSN not set; skipping the restricted-role matrix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the restricted runtime role: %v", err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping as the restricted runtime role: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// assertNotOwner: a green result must not come from a DSN that fell back to the owner.
func assertNotOwner(t *testing.T, rp *pgxpool.Pool) {
	t.Helper()
	var user string
	if err := rp.QueryRow(context.Background(), `SELECT current_user`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if user == "postgres" {
		t.Fatal("the restricted matrix is connected as the superuser; it would prove nothing")
	}
	var canWrite bool
	if err := rp.QueryRow(context.Background(),
		`SELECT has_table_privilege(current_user,'iam_v2.entitlements','INSERT')`).Scan(&canWrite); err != nil {
		t.Fatal(err)
	}
	if canWrite {
		t.Fatal("the runtime role holds INSERT on entitlements; the restriction under test does not exist")
	}
}

func TestIntegrationRestricted_FullPaymentToEntitlementUnderLeastPrivilege(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	assertNotOwner(t, rp)
	ctx := context.Background()
	s := seedPaidChain(t, owner)

	// Two restricted connections, deliberately: the execution and outcome credentials are separate authorities.
	op := outcomePool(t)
	granter := SQLGranter{Pool: rp}
	e := NewEngineWithOutcome(liveCfg, rp, op, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), granter)

	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatalf("the restricted role could not create a payment intent: %v", err)
	}
	if in.MerchantAccountID != s.merchant {
		t.Fatalf("identity resolution under the restricted role returned %s", in.MerchantAccountID)
	}
	out, err := e.Execute(ctx, s.tenant, s.site, in.ID)
	if err != nil {
		t.Fatalf("the restricted role could not execute the payment: %v", err)
	}
	if out.Outcome != OutcomeCaptured || out.EntitlementID == "" {
		t.Fatalf("the restricted journey did not reach a grant: %+v", out)
	}
	if st := scan1[string](t, owner, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "SETTLED" {
		t.Fatalf("settlement is %s", st)
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 1 {
		t.Fatalf("expected exactly one entitlement, got %d", n)
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlement_state_transitions es
		JOIN iam_v2.entitlements en ON en.id = es.entitlement_id WHERE en.purchase_id=$1`, s.purchase); n != 1 {
		t.Fatalf("expected one opening transition, got %d", n)
	}
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification(in.ClientRef, "exec:"+in.ClientRef, OutcomeCaptured, "prv_"+in.ClientRef)); err != nil {
		t.Fatalf("replay under the restricted role errored: %v", err)
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 1 {
		t.Fatalf("a replay under the restricted role created %d entitlements", n)
	}
}

func TestIntegrationRestricted_TheSameRoleCannotBypassThePath(t *testing.T) {
	owner := pool(t)
	rp := runtimePool(t)
	assertNotOwner(t, rp)
	ctx := context.Background()
	s := seedPaidChain(t, owner)

	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"write an entitlement by hand",
			`INSERT INTO iam_v2.entitlements(tenant_id,site_id,status) VALUES ($1,$2,'ACTIVE')`,
			[]any{s.tenant, s.site}},
		{"settle a settlement by hand",
			`UPDATE iam_v2.settlements SET status='SETTLED' WHERE id=$1`, []any{s.settlement}},
		{"mark a payment CAPTURED by hand",
			`UPDATE iam_v2.payment_transactions SET status='CAPTURED' WHERE settlement_id=$1`, []any{s.settlement}},
		{"forge a provider event",
			`INSERT INTO iam_v2.payment_transaction_events
			   (tenant_id,site_id,payment_transaction_id,provider,merchant_account_id,provider_event_id,event_type)
			 VALUES ($1,$2,gen_random_uuid(),'test-double',$3,'forged','x')`,
			[]any{s.tenant, s.site, s.merchant}},
		{"grant a purchase by hand",
			`UPDATE iam_v2.purchases SET state='GRANTED' WHERE id=$1`, []any{s.purchase}},
		{"invent a payment account",
			`INSERT INTO iam_v2.payment_provider_accounts(tenant_id,site_id,provider,merchant_account_ref,status)
			 VALUES ($1,$2,'test-double','forged','ACTIVE')`, []any{s.tenant, s.site}},
		{"record a review decision",
			`INSERT INTO iam_v2.posting_review_actions(tenant_id,site_id,action) VALUES ($1,$2,'CONFIRM_POSTED')`,
			[]any{s.tenant, s.site}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rp.Exec(ctx, tc.sql, tc.args...); err == nil {
				t.Fatalf("the restricted runtime role was able to %s", tc.name)
			}
		})
	}
	if _, err := rp.Exec(ctx, `SELECT count(*) FROM iam_v2.vouchers`); err == nil {
		t.Fatal("the payment runtime can read the voucher table")
	}
}

func TestIntegrationRestricted_OperatorCanReviewAndOnlyReview(t *testing.T) {
	owner := pool(t)
	dsn := os.Getenv("PHASE4_OPERATOR_DSN")
	if dsn == "" {
		t.Skip("PHASE4_OPERATOR_DSN not set")
	}
	ctx := context.Background()
	op, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the operator role: %v", err)
	}
	t.Cleanup(op.Close)

	var canExec bool
	if err := op.QueryRow(ctx,
		`SELECT has_function_privilege(current_user,
		   'iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint)','EXECUTE')`).Scan(&canExec); err != nil {
		t.Fatal(err)
	}
	if !canExec {
		t.Fatal("the operator role cannot execute the sanctioned review operation; it is not operational")
	}
	for _, tbl := range []string{"posting_review_actions", "posting_review_state", "pms_postings",
		"posting_outbox", "settlements", "payment_transactions", "entitlements"} {
		var w bool
		if err := op.QueryRow(ctx,
			`SELECT has_table_privilege(current_user, 'iam_v2.'||$1, 'INSERT') OR
			        has_table_privilege(current_user, 'iam_v2.'||$1, 'UPDATE')`, tbl).Scan(&w); err != nil {
			t.Fatal(err)
		}
		if w {
			t.Fatalf("the operator role holds a direct write on iam_v2.%s", tbl)
		}
	}
	_ = owner
}
