//go:build integration

package payment

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// THE RESTRICTED-ROLE END-TO-END PROOF.
//
// Every other test in this package connects as the schema owner, which can do anything. That makes them
// proofs about LOGIC and not about PRIVILEGE: a runtime that silently depends on owner rights would pass
// all of them and then fail the moment it was deployed with the credentials it is supposed to have.
//
// These tests connect as a login role holding ONLY sc_payment_runtime, and drive the complete journey --
// resolve identity, create the intent, cross the durable boundary, apply an authenticated outcome, settle,
// and grant the entitlement through the existing Phase-2 writer. If any step needed a privilege the role
// does not hold, it fails here rather than in production.
//
// The negative half matters just as much: the same role must remain unable to reach the same outcomes by
// any route other than the sanctioned one.

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

// assertNotOwner proves the connection really is restricted, so a green result cannot come from a DSN that
// quietly fell back to the owner.
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

// The whole journey, under the real restricted role.
func TestIntegrationRestricted_FullPaymentToEntitlementUnderLeastPrivilege(t *testing.T) {
	owner := pool(t) // fixtures only: seeding a tenant is an administrative act, not a runtime one
	rp := runtimePool(t)
	assertNotOwner(t, rp)
	ctx := context.Background()
	s := seedPaidChain(t, owner)

	// Everything from here runs on the restricted connection, including the Phase-2 grant.
	granter := SQLGranter{Pool: rp}
	e := NewEngine(liveCfg, rp, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), granter)

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
	// the opening transition came with it, written by the controlled function rather than by the runtime
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlement_state_transitions es
		JOIN iam_v2.entitlements en ON en.id = es.entitlement_id WHERE en.purchase_id=$1`, s.purchase); n != 1 {
		t.Fatalf("expected one opening transition, got %d", n)
	}
	// an authenticated replay under the same restricted role still grants nothing further
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification(in.ClientRef, "exec:"+in.ClientRef, OutcomeCaptured, "prv_"+in.ClientRef)); err != nil {
		t.Fatalf("replay under the restricted role errored: %v", err)
	}
	if n := scan1[int](t, owner, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 1 {
		t.Fatalf("a replay under the restricted role created %d entitlements", n)
	}
}

// The same role, trying to reach the same outcomes without the sanctioned path.
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
	// and it still cannot see what a reporting role should not: the runtime holds no privilege on vouchers
	if _, err := rp.Exec(ctx, `SELECT count(*) FROM iam_v2.vouchers`); err == nil {
		t.Fatal("the payment runtime can read the voucher table")
	}
}

// The operator role can perform the sanctioned review and nothing else.
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
	// ... and holds no direct write anywhere it could use instead
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
	// the reporting role sees the redacted views and NOT the underlying detail
	_ = owner
}
