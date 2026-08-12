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

// The Phase-4 payment runtime matrix. It needs a disposable PostgreSQL 16 carrying 0011-0016
// (scripts/phase4-pg-integration.sh builds one) reachable via PHASE4_TEST_DSN.
//
// No provider is ever contacted: every provider here is a deterministic in-process double, and the DARK
// tests assert that even a double which WOULD capture is never reached.

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

// ---------------------------------------------------------------- fixture

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

// seedPaidChain builds a complete paid purchase: tenant, site, package, quote, auth context, purchase and
// an ONLINE_PAYMENT settlement. The quote and auth context matter because the grant path reads its pins
// from them, exactly as the free path does.
func seedPaidChain(t *testing.T, p *pgxpool.Pool) scope {
	t.Helper()
	ctx := context.Background()
	u := time.Now().UnixNano()
	s := scope{amount: 1000, currency: "USD", exponent: 2}

	if err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	// The site's authoritative payment identity. Nothing can create a payment intent without it, which is
	// the corrected behaviour: no configuration means no charge, never an invented merchant account.
	s.merchant = scan1[string](t, p, `INSERT INTO iam_v2.payment_provider_accounts
		(tenant_id,site_id,provider,merchant_account_ref,status,is_default)
		VALUES ($1,$2,'test-double','acct_'||substr(md5(random()::text),1,12),'ACTIVE',true)
		RETURNING id::text`, s.tenant, s.site)
	gn := scan1[string](t, p, `INSERT INTO public.guest_networks(id,tenant_id,site_id) VALUES (gen_random_uuid(),$1,$2)
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
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
		VALUES ($1,$2,$3,1,$4,'GENERAL',1000,'USD',2) RETURNING id::text`, s.tenant, s.site, pkg, planRev)

	// a voucher subject, so the grant has exactly one subject to pin
	keyGen := scan1[string](t, p, `INSERT INTO iam_v2.voucher_code_key_generations
		(tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		VALUES ($1,$2,1,'\x00','{}',gen_random_uuid()) RETURNING id::text`, s.tenant, s.site)
	voucher := scan1[string](t, p, `INSERT INTO iam_v2.vouchers
		(tenant_id,site_id,package_revision_id,code_hmac,code_ciphertext,code_nonce,code_key_generation_id,code_last4)
		VALUES ($1,$2,$3,decode(md5(random()::text||clock_timestamp()::text),'hex'),'\x01','\x01',$4,'1234')
		RETURNING id::text`, s.tenant, s.site, pkgRev, keyGen)

	ac := scan1[string](t, p, `INSERT INTO iam_v2.auth_contexts
		(tenant_id,site_id,method,voucher_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'VOUCHER',$3,$4,$5, now()+interval '10 minutes') RETURNING id::text`,
		s.tenant, s.site, voucher, dev, gn)
	snap := `{"version":` + strconv.Itoa(iamv2.GrantSnapshotVersion) + `,"service_plan_revision_id":"` + planRev +
		`","package_revision_id":"` + pkgRev + `","max_concurrent_devices":2,` +
		`"time_accounting_mode":"VALIDITY_WINDOW","end_mode":"VALIDITY_WINDOW","validity_seconds":3600}`
	quote := scan1[string](t, p, `INSERT INTO iam_v2.offer_quotes
		(tenant_id,site_id,auth_context_id,package_revision_id,price_minor,currency,currency_exponent,grant_snapshot,expires_at)
		VALUES ($1,$2,$3,$4,1000,'USD',2,$5::jsonb, now()+interval '5 minutes') RETURNING id::text`,
		s.tenant, s.site, ac, pkgRev, snap)

	s.purchase = scan1[string](t, p, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,trigger,
		 amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,'GUEST_SELECTION',1000,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		s.tenant, s.site, pkgRev, quote, ac)
	s.settlement = scan1[string](t, p, `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'ONLINE_PAYMENT','REQUIRED') RETURNING id::text`, s.tenant, s.site, s.purchase)
	return s
}

// fakeGranter records grant calls without writing anything, for tests about the payment machine itself.
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
	darkCfg = Config{MasterEnabled: true, PaymentEnabled: true}                        // domain on, provider OFF
	liveCfg = Config{MasterEnabled: true, PaymentEnabled: true, ProviderEnabled: true} // doubles only
)

// idem builds a per-run idempotency key. The run nonce matters: an idempotency key is globally unique by
// design, so two runs against the same database must not collide over a name they legitimately share.
var runNonce = strconv.FormatInt(time.Now().UnixNano(), 36)

func idem(t *testing.T, suffix string) string {
	return "idem-" + runNonce + "-" + t.Name() + "-" + suffix
}

// ---------------------------------------------------------------- DARK

// The positive no-egress proof: the runtime genuinely runs, crosses the durable boundary, and is stopped at
// the provider boundary with a double that WOULD have captured.
func TestIntegrationPayment_DarkNeverReachesTheProvider(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	prov := NewScriptedProvider(Result{Outcome: OutcomeCaptured})
	e := NewEngine(darkCfg, p, prov, &fakeGranter{})

	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); CodeOf(err) != ErrDarkNoEgress {
			t.Fatalf("pass %d: expected a DARK refusal, got %v", i, err)
		}
	}
	if got := len(prov.Requests()); got != 0 {
		t.Fatalf("the provider double was reached %d times while DARK", got)
	}
	if e.ProviderRefusals() != 3 {
		t.Fatalf("expected 3 recorded refusals, got %d", e.ProviderRefusals())
	}
	// A preflight refusal is provably NOT_SENT, so it must leave NOTHING behind. The intent stays CREATED
	// and the settlement stays REQUIRED -- both still executable once the posture is fixed. The previous
	// behaviour stranded them at PENDING / IN_PROGRESS, describing an execution that had never begun.
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); st != "CREATED" {
		t.Fatalf("a NOT_SENT preflight refusal moved the intent to %s", st)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement); st != "REQUIRED" {
		t.Fatalf("a NOT_SENT preflight refusal moved the settlement to %s", st)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.payment_transaction_events
		WHERE payment_transaction_id=$1`, in.ID); n != 0 {
		t.Fatalf("a NOT_SENT refusal recorded %d provider events", n)
	}
}

func TestIntegrationPayment_UncorrelatableAdapterIsRefused(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, UncorrelatableProvider{}, &fakeGranter{})
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); CodeOf(err) != ErrConfig {
		t.Fatalf("an adapter without the correlation capability must be refused, got %v", err)
	}
}

// ---------------------------------------------------------------- server-pinned money

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
	// and the purchase price itself cannot be moved out from under the pinned quote at all
	if _, err := p.Exec(ctx, `UPDATE iam_v2.purchases SET amount_minor=999999 WHERE id=$1`, s.purchase); err == nil {
		t.Fatal("a purchase amount was edited away from its pinned quote")
	}
	if got := scan1[int64](t, p, `SELECT amount_minor FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); got != s.amount {
		t.Fatalf("the intent no longer carries the pinned amount: %d", got)
	}
}

// ---------------------------------------------------------------- the capture path and the grant

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
	// the correlation actually round-tripped
	ref := scan1[string](t, p, `SELECT provider_txn_ref FROM iam_v2.payment_transactions WHERE id=$1`, in.ID)
	if ref != "prv_"+in.ClientRef {
		t.Fatalf("the provider reference did not round-trip: %q", ref)
	}
	// replaying the same execution must not grant again
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err == nil {
		t.Fatal("a terminal intent must not be executable again")
	}
	if g.calls > 2 {
		t.Fatalf("the grant path was entered %d times", g.calls)
	}
}

// A duplicate callback, a concurrent callback and a replay all converge on ONE entitlement.
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

	// the same provider event delivered many times, concurrently
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

// ---------------------------------------------------------------- UNKNOWN

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
	// nothing retries it, and a second execution is structurally refused
	for i := 0; i < 3; i++ {
		if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); CodeOf(err) != ErrNotExecutable {
			t.Fatalf("an UNKNOWN intent must not be re-executable, got %v", err)
		}
	}
	if n := len(prov.Requests()); n != 1 {
		t.Fatalf("UNKNOWN was retried: %d provider requests", n)
	}
	// and a new charge cannot be admitted against the same settlement while the UNKNOWN one is live
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
	// (0016) a terminal settlement admits no further charge
	if _, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "2")); CodeOf(err) != ErrNotExecutable {
		t.Fatalf("a FAILED settlement must not admit another charge, got %v", err)
	}
}

// ---------------------------------------------------------------- refunds

func TestIntegrationPayment_RefundMovesTheSameSettlementAtomically(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}

	// a partial refund
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
	// the remainder
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
	// and the bound holds
	if _, err := e.CreateRefundIntent(ctx, s.tenant, s.site, in.ID, 1, idem(t, "r3")); CodeOf(err) != ErrNotExecutable {
		t.Fatalf("the cumulative refund bound must refuse a further refund, got %v", err)
	}
}

// ---------------------------------------------------------------- correlation

func TestIntegrationPayment_CorrelationConflictAndForgery(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}
	// an unknown client reference correlates to nothing and is refused, not guessed
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification("sc_not_a_real_ref", "evt-x", OutcomeCaptured, "prv_x")); CodeOf(err) != ErrUncorrelated {
		t.Fatalf("an uncorrelated notification must be refused, got %v", err)
	}
	// a CONFLICTING provider reference for an already-pinned intent fails closed
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification(in.ClientRef, "evt-conflict", OutcomeCaptured, "prv_someone_else")); CodeOf(err) != ErrRefConflict {
		t.Fatalf("a conflicting provider reference must fail closed, got %v", err)
	}
}

// The forgery matrix. Each case is a way a caller might try to manufacture a CAPTURED, and each must fail
// BEFORE anything is recorded. The baseline matters: the same delivery, correctly signed, is accepted --
// otherwise these would pass against a boundary that simply rejects everything.
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
	// the baseline: the correctly signed delivery IS accepted, so the refusals above are about the forgery
	if err := beginExecution(t, p, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HandleProviderNotification(ctx, good); err != nil {
		t.Fatalf("a correctly signed notification was refused: %v", err)
	}
}

// beginExecution crosses the durable boundary directly, for tests that need an executing intent without
// going through a provider double.
func beginExecution(t *testing.T, p *pgxpool.Pool, txnID string) error {
	t.Helper()
	_, err := p.Exec(context.Background(), `SELECT iam_v2.begin_payment_execution($1)`, txnID)
	return err
}

// An adapter that cannot authenticate notifications must not be able to deliver an outcome at all. This is
// the difference between "we check a signature" and "there is no unauthenticated path".
func TestIntegrationPayment_AdapterWithoutAuthenticationCannotDeliverAnOutcome(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	e := NewEngine(liveCfg, p, &DeafProvider{}, &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	raw := BuildNotification(in.ClientRef, "evt-1", OutcomeCaptured, "prv_x")
	if _, err := e.HandleProviderNotification(ctx, raw); CodeOf(err) != ErrUntrusted {
		t.Fatalf("an adapter that cannot authenticate must not deliver an outcome, got %v", err)
	}
	// and with no adapter at all
	e2 := NewEngine(liveCfg, p, nil, &fakeGranter{})
	if _, err := e2.HandleProviderNotification(ctx, raw); CodeOf(err) != ErrUntrusted {
		t.Fatalf("no adapter must mean no notification path, got %v", err)
	}
}

// Financial identity is configuration. A site without it cannot create an intent, and a DISABLED account
// cannot take money.
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
	// no placeholder identity is representable in the financial record
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.payment_provider_accounts
		(tenant_id,site_id,provider,merchant_account_ref,status) VALUES ($1,$2,'none','x','ACTIVE')`,
		s.tenant, s.site); err == nil {
		t.Fatal("a placeholder provider identity was accepted into configuration")
	}

	// a site with NO configured account fails closed rather than inventing one
	b := seedPaidChain(t, p)
	if _, err := p.Exec(ctx, `DELETE FROM iam_v2.payment_provider_accounts WHERE id=$1`, b.merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateChargeIntent(ctx, b.tenant, b.site, b.settlement, idem(t, "2")); CodeOf(err) != ErrNoAccount {
		t.Fatalf("a site with no configured account must fail closed, got %v", err)
	}

	// a DISABLED account is not a default and cannot be resolved
	c := seedPaidChain(t, p)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.payment_provider_accounts SET is_default=false, status='DISABLED'
		WHERE id=$1`, c.merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateChargeIntent(ctx, c.tenant, c.site, c.settlement, idem(t, "3")); CodeOf(err) != ErrNoAccount {
		t.Fatalf("a disabled account must not resolve, got %v", err)
	}
}

// A production build has no adapter, so it must be unable to charge at all rather than charging as "none".
func TestIntegrationPayment_ProductionBuildCannotPersistAFakeProvider(t *testing.T) {
	pr, err := ProductionProviderFor(Config{})
	if err != nil {
		t.Fatalf("the delivered posture must construct: %v", err)
	}
	if pr != nil {
		t.Fatalf("this build has no adapter; it must not present one (%s)", pr.Name())
	}
}
