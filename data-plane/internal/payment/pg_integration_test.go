//go:build integration

package payment

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Phase-4 payment runtime matrix; needs a disposable PostgreSQL 16 (scripts/phase4-pg-integration.sh) via
// PHASE4_TEST_DSN. Every provider is an in-process double, and the DARK tests assert that even a double that would
// capture is never reached.

// outcomePool is the separate outcome-authority credential; assertions about provider outcomes must use it.
func outcomePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE4_OUTCOME_DSN")
	if dsn == "" {
		t.Skip("PHASE4_OUTCOME_DSN not set; skipping the outcome-authority matrix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the outcome role: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE4_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE4_TEST_DSN not set; skipping the phase-4 payment integration matrix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

type scope struct {
	tenant, site string
	purchase     string
	settlement   string
	merchant     string
	amount       int64
	currency     string
	exponent     int16
}

func scan1[T any](t *testing.T, p *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query: %v\n%s", err, sql)
	}
	return v
}

// seedPaidChain: the quote and auth context matter because the grant path reads its pins from them.
func seedPaidChain(t *testing.T, p *pgxpool.Pool) scope {
	t.Helper()
	ctx := context.Background()
	u := time.Now().UnixNano()
	s := scope{amount: 1000, currency: "USD", exponent: 2}

	if err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	// No payment identity, no intent: no configuration means no charge, never an invented merchant account.
	s.merchant = scan1[string](t, p, `INSERT INTO iam_v2.payment_provider_accounts
		(tenant_id,site_id,provider,merchant_account_ref,status,is_default)
		VALUES ($1,$2,'test-double','acct_'||substr(md5(random()::text),1,12),'ACTIVE',true)
		RETURNING id::text`, s.tenant, s.site)
	gn := scan1[string](t, p, `INSERT INTO public.guest_networks
		(id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr)
		SELECT g,$1,$2,'net','p'||substr(md5(g::text),1,12),'b'||substr(md5(g::text),1,12),
		       '10.99.0.1/24','10.99.0.1','10.99.0.0/24' FROM gen_random_uuid() g
		RETURNING id::text`, s.tenant, s.site)
	dev := scan1[string](t, p, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac)
		VALUES ($1,$2,gen_random_uuid(),$3) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("02:00:00:%02x:%02x:%02x", u&0xff, (u>>8)&0xff, (u>>16)&0xff))
	plan := scan1[string](t, p, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code) VALUES ($1,$2,$3)
		RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("P%d", u))
	planRev := scan1[string](t, p, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, s.tenant, s.site, plan)
	pkg := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code) VALUES ($1,$2,$3)
		RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("K%d", u))
	pkgRev := scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
		VALUES ($1,$2,$3,1,$4,'GENERAL',1000,'USD',2,'{ONLINE_PAYMENT,PREPAID}') RETURNING id::text`, s.tenant, s.site, pkg, planRev)
	// A voucher is issued only against the current revision of an active package (migration 0095).
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1, active=true WHERE id=$2`, pkgRev, pkg); err != nil {
		t.Fatalf("current revision: %v", err)
	}

	keyGen := scan1[string](t, p, `INSERT INTO iam_v2.voucher_code_key_generations
		(tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		VALUES ($1,$2,1,'\x00','{}',gen_random_uuid()) RETURNING id::text`, s.tenant, s.site)
	voucher := scan1[string](t, p, `INSERT INTO iam_v2.vouchers
		(tenant_id,site_id,package_revision_id,code_hmac,code_ciphertext,code_nonce,code_key_generation_id,code_last4)
		VALUES ($1,$2,$3,decode(md5(random()::text||clock_timestamp()::text),'hex'),'\x01','\x01',$4,'1234')
		RETURNING id::text`, s.tenant, s.site, pkgRev, keyGen)

	ac := guarded(t, p, "auth_context", `INSERT INTO iam_v2.auth_contexts
		(tenant_id,site_id,method,voucher_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'VOUCHER',$3,$4,$5, now()+interval '10 minutes') RETURNING id::text`,
		s.tenant, s.site, voucher, dev, gn)
	snap := `{"version":` + strconv.Itoa(iamv2.GrantSnapshotVersion) + `,"service_plan_revision_id":"` + planRev +
		`","package_revision_id":"` + pkgRev + `","max_concurrent_devices":2,` +
		`"time_accounting_mode":"VALIDITY_WINDOW","end_mode":"VALIDITY_WINDOW","validity_seconds":3600}`
	quote := guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.offer_quotes
		(tenant_id,site_id,auth_context_id,package_revision_id,price_minor,currency,currency_exponent,grant_snapshot,expires_at)
		VALUES ($1,$2,$3,$4,1000,'USD',2,$5::jsonb, now()+interval '5 minutes') RETURNING id::text`,
		s.tenant, s.site, ac, pkgRev, snap)

	s.purchase = guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,trigger,
		 amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,'GUEST_SELECTION',1000,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		s.tenant, s.site, pkgRev, quote, ac)
	s.settlement = guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'ONLINE_PAYMENT','REQUIRED') RETURNING id::text`, s.tenant, s.site, s.purchase)
	return s
}

type fakeGranter struct {
	mu    sync.Mutex
	calls int
	id    string
}

func (g *fakeGranter) GrantSettledSettlement(_ context.Context, _, _, _ string) (GrantOutcome, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.id == "" {
		g.id = "ent-1"
		return GrantOutcome{EntitlementID: g.id}, nil
	}
	return GrantOutcome{EntitlementID: g.id, AlreadyGranted: true}, nil
}

var (
	darkCfg = Config{MasterEnabled: true, PaymentEnabled: true}
	liveCfg = Config{MasterEnabled: true, PaymentEnabled: true, ProviderEnabled: true}
)

// idem builds a per-run key: idempotency keys are globally unique, so two runs against one database must not collide.
var runNonce = strconv.FormatInt(time.Now().UnixNano(), 36)

// seedOperator: the financial actor assertion refuses an arbitrary uuid.
func seedOperator(t *testing.T, p *pgxpool.Pool, tenant string) string {
	t.Helper()
	return scan1[string](t, p, `INSERT INTO public.operators(tenant_id,email,display_name,status)
		VALUES ($1,'op-'||substr(md5(random()::text),1,10)||'@test.local','Test Operator','active')
		RETURNING id::text`, tenant)
}

// guardedExec stages state inside a controlled-writer scope; raw writes to controlled tables are refused outside one.
func guardedExec(t *testing.T, p *pgxpool.Pool, family, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
		t.Fatalf("open %s: %v", family, err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v -- %s", family, err, sql)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func idem(t *testing.T, suffix string) string {
	return "idem-" + runNonce + "-" + t.Name() + "-" + suffix
}

func TestIntegrationPayment_AmountComesFromThePinnedPurchase(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(darkCfg, p, NewScriptedProvider(), &fakeGranter{})
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if in.AmountMinor != s.amount || in.Currency != s.currency || in.Exponent != s.exponent {
		t.Fatalf("the intent must carry the pinned purchase money, got %d/%s/%d",
			in.AmountMinor, in.Currency, in.Exponent)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.purchases SET amount_minor=999999 WHERE id=$1`, s.purchase); err == nil {
		t.Fatal("a purchase amount was edited away from its pinned quote")
	}
	if got := scan1[int64](t, p, `SELECT amount_minor FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); got != s.amount {
		t.Fatalf("the intent no longer carries the pinned amount: %d", got)
	}
}

func TestIntegrationPayment_CapturedChargeSettlesAndGrantsExactlyOnce(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	g := &fakeGranter{}
	prov := NewScriptedProvider(Result{Outcome: OutcomeCaptured})
	e := NewEngine(liveCfg, p, prov, g)

	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Execute(ctx, s.tenant, s.site, in.ID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.Outcome != OutcomeCaptured || out.Applied != "APPLIED" {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "SETTLED" {
		t.Fatalf("a captured charge must settle, got %s", st)
	}
	if out.EntitlementID == "" {
		t.Fatal("a settled charge must grant")
	}
	ref := scan1[string](t, p, `SELECT provider_txn_ref FROM iam_v2.payment_transactions WHERE id=$1`, in.ID)
	if ref != "prv_"+in.ClientRef {
		t.Fatalf("the provider reference did not round-trip: %q", ref)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err == nil {
		t.Fatal("a terminal intent must not be executable again")
	}
	if g.calls > 2 {
		t.Fatalf("the grant path was entered %d times", g.calls)
	}
}

func TestIntegrationPayment_DuplicateAndConcurrentCallbacksGrantOnce(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	g := &fakeGranter{}
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), g)
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}
	before := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.payment_transaction_events
		WHERE payment_transaction_id=$1`, in.ID)

	var wg sync.WaitGroup
	applied := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _ := e.HandleProviderNotification(ctx, BuildNotification(in.ClientRef, "evt-dup", OutcomeCaptured, "prv_"+in.ClientRef))
			applied <- o.Applied
		}()
	}
	wg.Wait()
	close(applied)
	nApplied := 0
	for a := range applied {
		if a == "APPLIED" {
			nApplied++
		}
	}
	if nApplied > 1 {
		t.Fatalf("a replayed provider event was applied %d times", nApplied)
	}
	after := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.payment_transaction_events
		WHERE payment_transaction_id=$1`, in.ID)
	if after != before+1 {
		t.Fatalf("expected exactly one new event row, got %d", after-before)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.payment_transactions
		WHERE settlement_id=$1 AND transaction_type='CHARGE'`, s.settlement); n != 1 {
		t.Fatalf("expected one charge, got %d", n)
	}
}

func TestIntegrationPayment_UnknownGoesToManualReviewAndIsNeverRetried(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	g := &fakeGranter{}
	prov := NewScriptedProvider(Result{Outcome: OutcomeUnknown, ReasonCode: "timeout"})
	e := NewEngine(liveCfg, p, prov, g)
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Execute(ctx, s.tenant, s.site, in.ID)
	if CodeOf(err) != ErrProviderUnknown || out.Outcome != OutcomeUnknown {
		t.Fatalf("an indeterminate provider outcome must be UNKNOWN, got %+v %v", out, err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "MANUAL_REVIEW" {
		t.Fatalf("UNKNOWN must route the settlement to manual review, got %s", st)
	}
	if g.calls != 0 {
		t.Fatal("UNKNOWN must never grant access")
	}
	for i := 0; i < 3; i++ {
		if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); CodeOf(err) != ErrNotExecutable {
			t.Fatalf("an UNKNOWN intent must not be re-executable, got %v", err)
		}
	}
	if n := len(prov.Requests()); n != 1 {
		t.Fatalf("UNKNOWN was retried: %d provider requests", n)
	}
	if _, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "2")); err == nil {
		t.Fatal("a settlement with a live UNKNOWN charge must not admit another")
	}
}

func TestIntegrationPayment_DeclinedChargeFailsTheSettlementAndGrantsNothing(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	g := &fakeGranter{}
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeDeclined, ReasonCode: "declined"}), g)
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatalf("a decline is an outcome, not an error: %v", err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "FAILED" {
		t.Fatalf("a declined charge must fail the settlement, got %s", st)
	}
	if g.calls != 0 {
		t.Fatal("a declined charge must not grant")
	}
	if _, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "2")); CodeOf(err) != ErrNotExecutable {
		t.Fatalf("a FAILED settlement must not admit another charge, got %v", err)
	}
}

func TestIntegrationPayment_RefundMovesTheSameSettlementAtomically(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}

	r1, err := e.CreateRefundIntent(ctx, s.tenant, s.site, in.ID, 400, idem(t, "r1"))
	if err != nil {
		t.Fatalf("refund intent: %v", err)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, r1.ID); err != nil {
		t.Fatalf("refund execute: %v", err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "PARTIALLY_REVERSED" {
		t.Fatalf("a partial capture must move the settlement to PARTIALLY_REVERSED, got %s", st)
	}
	r2, err := e.CreateRefundIntent(ctx, s.tenant, s.site, in.ID, 600, idem(t, "r2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, r2.ID); err != nil {
		t.Fatal(err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "REVERSED" {
		t.Fatalf("a full return must move the settlement to REVERSED, got %s", st)
	}
	if _, err := e.CreateRefundIntent(ctx, s.tenant, s.site, in.ID, 1, idem(t, "r3")); CodeOf(err) != ErrNotExecutable {
		t.Fatalf("the cumulative refund bound must refuse a further refund, got %v", err)
	}
}

func TestIntegrationPayment_CorrelationConflictAndForgery(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification("sc_not_a_real_ref", "evt-x", OutcomeCaptured, "prv_x")); CodeOf(err) != ErrUncorrelated {
		t.Fatalf("an uncorrelated notification must be refused, got %v", err)
	}
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification(in.ClientRef, "evt-conflict", OutcomeCaptured, "prv_someone_else")); CodeOf(err) != ErrRefConflict {
		t.Fatalf("a conflicting provider reference must fail closed, got %v", err)
	}
}

// Forgery matrix: each case must fail before anything is recorded; the baseline (the same delivery correctly signed is
// accepted) shows the boundary does not simply reject everything.
func TestIntegrationPayment_ForgedNotificationsAreRefused(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	prov := NewScriptedProvider(Result{Outcome: OutcomeUnknown, ReasonCode: "no_answer"})
	g := &fakeGranter{}
	e := NewEngine(liveCfg, p, prov, g)
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))

	good := BuildNotification(in.ClientRef, "evt-good", OutcomeCaptured, "prv_"+in.ClientRef)

	for _, tc := range []struct {
		name string
		raw  RawNotification
	}{
		{"no signature at all", RawNotification{Body: good.Body}},
		{"a wrong signature", RawNotification{Body: good.Body,
			Headers: map[string]string{"X-Test-Signature": "00"}}},
		{"a valid signature over a DIFFERENT body", RawNotification{
			Body:    []byte(`{"client_ref":"` + in.ClientRef + `","event_id":"evt-swap","outcome":"CAPTURED"}`),
			Headers: good.Headers}},
		{"an empty body with a valid signature for it", func() RawNotification {
			return RawNotification{Body: []byte(``), Headers: map[string]string{"X-Test-Signature": SignNotification([]byte(``))}}
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.HandleProviderNotification(ctx, tc.raw)
			if err == nil {
				t.Fatal("a forged notification was accepted")
			}
			if c := CodeOf(err); c != ErrUntrusted && c != ErrUncorrelated {
				t.Fatalf("expected an untrusted/uncorrelated refusal, got %v", err)
			}
		})
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.payment_transaction_events
		WHERE payment_transaction_id=$1`, in.ID); n != 0 {
		t.Fatalf("forged notifications recorded %d events", n)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); st != "CREATED" {
		t.Fatalf("forged notifications moved the intent to %s", st)
	}
	if g.calls != 0 {
		t.Fatal("a forged notification reached the grant path")
	}
	if err := beginExecution(t, p, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HandleProviderNotification(ctx, good); err != nil {
		t.Fatalf("a correctly signed notification was refused: %v", err)
	}
}

func beginExecution(t *testing.T, p *pgxpool.Pool, txnID string) error {
	t.Helper()
	_, err := p.Exec(context.Background(), `SELECT iam_v2.begin_payment_execution($1)`, txnID)
	return err
}

func TestIntegrationPayment_IdentityIsResolvedNotSupplied(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})

	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if in.MerchantAccountID != s.merchant {
		t.Fatalf("the intent did not resolve the site's configured account: %s", in.MerchantAccountID)
	}
	if in.Provider != "test-double" {
		t.Fatalf("the intent resolved provider %q", in.Provider)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.payment_provider_accounts
		(tenant_id,site_id,provider,merchant_account_ref,status) VALUES ($1,$2,'none','x','ACTIVE')`,
		s.tenant, s.site); err == nil {
		t.Fatal("a placeholder provider identity was accepted into configuration")
	}

	b := seedPaidChain(t, p)
	if _, err := p.Exec(ctx, `DELETE FROM iam_v2.payment_provider_accounts WHERE id=$1`, b.merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateChargeIntent(ctx, b.tenant, b.site, b.settlement, idem(t, "2")); CodeOf(err) != ErrNoAccount {
		t.Fatalf("a site with no configured account must fail closed, got %v", err)
	}

	c := seedPaidChain(t, p)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.payment_provider_accounts SET is_default=false, status='DISABLED'
		WHERE id=$1`, c.merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateChargeIntent(ctx, c.tenant, c.site, c.settlement, idem(t, "3")); CodeOf(err) != ErrNoAccount {
		t.Fatalf("a disabled account must not resolve, got %v", err)
	}
}
