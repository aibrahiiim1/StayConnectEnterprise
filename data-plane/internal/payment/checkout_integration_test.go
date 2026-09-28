//go:build integration

package payment

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// scriptedHosted is a HostedCheckoutProvider whose answers the test controls. It records every call so a test
// can prove a status query never creates anything.
type scriptedHosted struct {
	mu        sync.Mutex
	createErr error
	status    StatusResult
	statusErr error
	creates   int
	queries   int
	lastRef   string
}

func (p *scriptedHosted) Name() string { return "stripe" }
func (p *scriptedHosted) CreateCheckout(_ context.Context, r CheckoutRequest) (Checkout, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	p.lastRef = r.ClientRef
	if p.createErr != nil {
		return Checkout{}, p.createErr
	}
	return Checkout{ProviderSessionRef: "cs_test_" + r.ClientRef, RedirectURL: "https://checkout.stripe.com/c/pay/" + r.ClientRef, ExpiresAt: r.ExpiresAt}, nil
}
func (p *scriptedHosted) QueryStatus(_ context.Context, q StatusQuery) (StatusResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries++
	return p.status, p.statusErr
}
func (p *scriptedHosted) RequiredHostedDomains() []string { return []string{"checkout.stripe.com"} }
func (p *scriptedHosted) CredentialKeys() []CredentialKey {
	return []CredentialKey{{Key: "secret_key", Label: "Secret key", Secret: true, Required: true}}
}
func (p *scriptedHosted) TestConnection(context.Context, Mode, string, Credentials) error { return nil }

func rolePool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s not set", env)
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func testKey(t *testing.T) PaymentKey {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	k, err := NewPaymentKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// cardFixture seeds a REQUIRED card-payment settlement whose default account is a configured Stripe TEST
// account with sealed credentials.
func cardFixture(t *testing.T, mode Mode) (scope, *CheckoutEngine, *scriptedHosted, *pgxpool.Pool) {
	t.Helper()
	admin := pool(t)
	s := seedCardChain(t, admin)
	ctx := context.Background()
	k := testKey(t)
	id, err := SaveAccount(ctx, admin, k, s.tenant, s.site, "", "stripe", "acct_"+runNonce+s.site[:8], "Stripe", "USD", mode,
		"ACTIVE", true, Credentials{"secret_key": "sk_test_x"}, "test-operator", "fixture")
	if err != nil {
		t.Fatalf("save account: %v", err)
	}
	s.merchant = id
	prov := &scriptedHosted{status: StatusResult{State: CheckoutOpen}}
	e := &CheckoutEngine{Runtime: rolePool(t, "PHASE4_RUNTIME_DSN"), Outcome: rolePool(t, "PHASE4_OUTCOME_DSN"),
		Adapters: map[string]HostedCheckoutProvider{"stripe": prov}, Key: k, Health: NewProviderHealth()}
	return s, e, prov, admin
}

func count1(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%v: %s", err, sql)
	}
	return n
}

func TestCheckoutCapturedGrantsExactlyOnce(t *testing.T) {
	s, e, prov, admin := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://portal/return", "http://portal/cancel")
	if err != nil || !strings.HasPrefix(st.RedirectURL, "https://") {
		t.Fatalf("start: %+v %v", st, err)
	}
	// The browser returning "success" proves nothing: while the provider says OPEN, nothing is granted.
	r, _ := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if r.EntitlementID != "" || r.TransactionStatus != "PENDING" {
		t.Fatalf("an open checkout must grant nothing: %+v", r)
	}
	// The provider now says captured, at the pinned amount.
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_1", AmountMinor: s.amount, Currency: "usd"}
	for i := 0; i < 3; i++ { // the return URL and the reconciler may both ask, any number of times
		r, err = e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
		if err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if r.EntitlementID == "" || r.SettlementStatus != "SETTLED" || r.PurchaseState != "GRANTED" {
		t.Fatalf("a verified capture settles and grants: %+v", r)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 1 {
		t.Fatalf("exactly one entitlement, got %d", n)
	}
	if prov.creates != 1 {
		t.Fatalf("a checkout is created once, got %d", prov.creates)
	}
	// A second start for the same settlement is refused: it is no longer REQUIRED.
	if _, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c"); err == nil {
		t.Fatal("a settled card payment must not start another checkout")
	}
	if prov.creates != 1 {
		t.Fatal("no second checkout may be created")
	}
}

func TestCheckoutAmountMismatchGoesToReview(t *testing.T) {
	s, e, prov, admin := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if err != nil {
		t.Fatal(err)
	}
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_2", AmountMinor: s.amount - 1, Currency: "usd"}
	r, _ := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if r.EntitlementID != "" || r.SettlementStatus != "MANUAL_REVIEW" || r.PurchaseState != "MANUAL_REVIEW" {
		t.Fatalf("a mismatched capture must go to review and grant nothing: %+v", r)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.entitlements WHERE purchase_id=$1`, s.purchase); n != 0 {
		t.Fatal("no entitlement on a mismatch")
	}
}

func TestCheckoutNotCreatedFailsWithoutMoney(t *testing.T) {
	s, e, prov, _ := cardFixture(t, ModeTest)
	prov.createErr = errors.Join(ErrCheckoutNotCreated, errors.New("400 invalid"))
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if !errors.Is(err, ErrCheckoutUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
	r, _ := e.state(ctx, txnRow{id: st.TransactionID})
	if r.SettlementStatus != "FAILED" || r.PurchaseState != "FAILED" {
		t.Fatalf("a provably-not-created checkout fails the settlement and purchase: %+v", r)
	}
}

func TestCheckoutAmbiguousIsResolvedByQueryNeverByRecreating(t *testing.T) {
	s, e, prov, _ := cardFixture(t, ModeTest)
	prov.createErr = errors.New("timeout after send")
	ctx := context.Background()
	st, err := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if !errors.Is(err, ErrCheckoutAmbiguous) {
		t.Fatalf("want ambiguous, got %v", err)
	}
	// The provider did create it and the client paid: the reconciler finds it by client reference.
	prov.createErr = nil
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_3", AmountMinor: s.amount, Currency: "USD"}
	r, err := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if err != nil || r.EntitlementID == "" {
		t.Fatalf("an ambiguous creation that was paid is granted by reconciliation: %+v %v", r, err)
	}
	if prov.creates != 1 {
		t.Fatalf("reconciliation never creates, creates=%d", prov.creates)
	}
}

func TestCheckoutExpiredAndUnresolved(t *testing.T) {
	s, e, prov, _ := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, _ := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	// Provider unreachable, and time is past expiry + grace: UNKNOWN (manual review), never a retry.
	prov.statusErr = errors.New("provider down")
	e.Now = func() time.Time { return time.Now().Add(4 * time.Hour) }
	r, _ := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if r.TransactionStatus != "UNKNOWN" || r.SettlementStatus != "MANUAL_REVIEW" {
		t.Fatalf("unresolved after grace must be UNKNOWN / manual review: %+v", r)
	}
	if prov.creates != 1 {
		t.Fatal("no retry of the checkout")
	}
}

func TestCheckoutDeclinedAtExpiryFails(t *testing.T) {
	s, e, prov, _ := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, _ := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	prov.status = StatusResult{State: CheckoutExpired}
	r, _ := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if r.SettlementStatus != "FAILED" || r.EntitlementID != "" {
		t.Fatalf("an expired checkout fails: %+v", r)
	}
}

func TestLiveModeRefusedWithoutAuthorisation(t *testing.T) {
	s, e, prov, _ := cardFixture(t, ModeLive)
	_, err := e.StartCheckout(context.Background(), s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	if !errors.Is(err, ErrLiveNotAllowed) || prov.creates != 0 {
		t.Fatalf("LIVE mode must be refused before any provider call: %v creates=%d", err, prov.creates)
	}
}

func TestProviderRefundsAreRecordedNeverInitiated(t *testing.T) {
	s, e, prov, admin := cardFixture(t, ModeTest)
	ctx := context.Background()
	st, _ := e.StartCheckout(ctx, s.tenant, s.site, s.settlement, "WiFi", "http://p/r", "http://p/c")
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_4", AmountMinor: s.amount, Currency: "USD"}
	if _, err := e.Reconcile(ctx, s.tenant, s.site, st.TransactionID); err != nil {
		t.Fatal(err)
	}
	// Paymob-style cumulative refund: 300, then the same 300 again, then 1000 in total.
	sweep := func(total int64) {
		prov.status.Events = []ProviderEvent{{EventID: "refund:" + st.TransactionID + ":" + strconv.FormatInt(total, 10),
			Kind: "REFUND", AmountMinor: total, ProviderRef: "pi_4", Cumulative: true}}
		e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	}
	sweep(300)
	if got := count1(t, admin, `SELECT COALESCE(sum(amount_minor),0)::int FROM iam_v2.payment_transactions WHERE parent_transaction_id=$1 AND status='CAPTURED'`, st.TransactionID); got != 300 {
		t.Fatalf("first refund recorded as 300, got %d", got)
	}
	sweep(300)
	if got := count1(t, admin, `SELECT COALESCE(sum(amount_minor),0)::int FROM iam_v2.payment_transactions WHERE parent_transaction_id=$1 AND status='CAPTURED'`, st.TransactionID); got != 300 {
		t.Fatalf("a repeated cumulative total adds nothing, got %d", got)
	}
	var ss string
	_ = admin.QueryRow(ctx, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement).Scan(&ss)
	if ss != "PARTIALLY_REVERSED" {
		t.Fatalf("settlement after a partial refund: %s", ss)
	}
	sweep(s.amount)
	_ = admin.QueryRow(ctx, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settlement).Scan(&ss)
	if ss != "REVERSED" {
		t.Fatalf("settlement after a full refund: %s", ss)
	}
	if prov.creates != 1 {
		t.Fatal("recording a refund never touches checkout creation")
	}
}

func TestSealedCredentialsAreBoundToTheirOwner(t *testing.T) {
	k := testKey(t)
	sealed, err := SealCredentials(k, "t", "s", "a", "g", Credentials{"secret_key": "sk_test_1"})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := OpenCredentials(k, "t", "s", "a", "g", sealed.KeyID, sealed.Nonce, sealed.Ciphertext); err != nil || c["secret_key"] != "sk_test_1" {
		t.Fatalf("round trip: %v %v", c, err)
	}
	for _, bad := range [][4]string{{"t2", "s", "a", "g"}, {"t", "s2", "a", "g"}, {"t", "s", "a2", "g"}, {"t", "s", "a", "g2"}} {
		if _, err := OpenCredentials(k, bad[0], bad[1], bad[2], bad[3], sealed.KeyID, sealed.Nonce, sealed.Ciphertext); err == nil {
			t.Fatalf("a ciphertext moved to %v must not open", bad)
		}
	}
	other := testKey(t)
	if _, err := OpenCredentials(other, "t", "s", "a", "g", sealed.KeyID, sealed.Nonce, sealed.Ciphertext); err == nil {
		t.Fatal("another appliance key must not open it")
	}
}

// guarded runs one statement inside a controlled-writer scope, as production code does.
func guarded(t *testing.T, p *pgxpool.Pool, family, sql string, args ...any) string {
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
	var id string
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v -- %s", family, err, sql)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedCardChain builds, on the FULL appliance schema, a priced Card-payment package, an ACCOUNT subject, a
// quote, an AWAITING_SETTLEMENT purchase and its REQUIRED ONLINE_PAYMENT settlement.
func seedCardChain(t *testing.T, p *pgxpool.Pool) scope {
	t.Helper()
	ctx := context.Background()
	u := time.Now().UnixNano()
	s := scope{amount: 2500, currency: "USD", exponent: 2}
	if err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM (SELECT gen_random_uuid() g) x RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, (SELECT gen_random_uuid() g) x RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	oct := func(sh uint) int64 { return (u >> sh) & 0xff }
	gn := scan1[string](t, p, `INSERT INTO public.guest_networks (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr)
		VALUES (gen_random_uuid(),$1,$2,'net',$3,$4,'10.99.0.1/24','10.99.0.1','10.99.0.0/24') RETURNING id::text`,
		s.tenant, s.site, fmt.Sprintf("eth%d", u%100000000), fmt.Sprintf("b%x", u)[:15])
	dev := scan1[string](t, p, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac)
		VALUES ($1,$2,gen_random_uuid(),$3) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("02:00:00:%02x:%02x:%02x", oct(0), oct(8), oct(16)))
	acct := scan1[string](t, p, `INSERT INTO iam_v2.guest_access_accounts (tenant_id,site_id,username,password_hash,enabled)
		VALUES ($1,$2,$3,'x',true) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("u%d", u))
	plan := scan1[string](t, p, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code) VALUES ($1,$2,$3) RETURNING id::text`,
		s.tenant, s.site, fmt.Sprintf("P%d", u))
	planRev := scan1[string](t, p, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, s.tenant, s.site, plan)
	pkg := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,active) VALUES ($1,$2,$3,true) RETURNING id::text`,
		s.tenant, s.site, fmt.Sprintf("K%d", u))
	pkgRev := scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
		VALUES ($1,$2,$3,1,$4,'GENERAL',2500,'USD',2,'{ONLINE_PAYMENT}') RETURNING id::text`, s.tenant, s.site, pkg, planRev)
	ac := guarded(t, p, "auth_context", `INSERT INTO iam_v2.auth_contexts
		(tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'ACCOUNT',$3,$4,$5, now()+interval '10 minutes') RETURNING id::text`, s.tenant, s.site, acct, dev, gn)
	snap := `{"version":` + strconv.Itoa(iamv2.GrantSnapshotVersion) + `,"service_plan_revision_id":"` + planRev +
		`","package_revision_id":"` + pkgRev + `","max_concurrent_devices":2,` +
		`"time_accounting_mode":"VALIDITY_WINDOW","end_mode":"MANUAL_END","acquisition_method":"ONLINE_PAYMENT"}`
	quote := guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.offer_quotes
		(tenant_id,site_id,auth_context_id,package_revision_id,price_minor,currency,currency_exponent,grant_snapshot,expires_at)
		VALUES ($1,$2,$3,$4,2500,'USD',2,$5::jsonb, now()+interval '5 minutes') RETURNING id::text`,
		s.tenant, s.site, ac, pkgRev, snap)
	s.purchase = guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,trigger,amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,'GUEST_SELECTION',2500,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		s.tenant, s.site, pkgRev, quote, ac)
	s.settlement = guarded(t, p, "commerce_intent", `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'ONLINE_PAYMENT','REQUIRED') RETURNING id::text`, s.tenant, s.site, s.purchase)
	return s
}
