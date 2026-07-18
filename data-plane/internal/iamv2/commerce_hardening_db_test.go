package iamv2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Phase-2 commerce DB-backed hardening tests (items 6/7/8/9/10/11) against the disposable iam_v2 DB.
// Skipped unless PHASE2_TEST_DSN is set.

var p2Seq int64

func nextSeq() int64 { return atomic.AddInt64(&p2Seq, 1) }

func scan1(t *testing.T, db *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("scan1: %v\nSQL: %s", err, sql)
	}
	return id
}

func count(t *testing.T, db *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\nSQL: %s", err, sql)
	}
	return n
}

// newAccountChain creates a fresh guest account + device + unconsumed ACCOUNT auth_context bound to the
// already-seeded package, so multiple independent quotes/confirms can run in one test.
func newAccountChain(t *testing.T, db *pgxpool.Pool) (accountID, deviceID, authCtxID string) {
	t.Helper()
	i := nextSeq()
	deviceID = scan1(t, db, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,$4) RETURNING id::text`,
		p2Tenant, p2Site, p2Appl, fmt.Sprintf("02:00:00:00:%02x:%02x", (i>>8)&0xff, i&0xff))
	accountID = scan1(t, db, `INSERT INTO iam_v2.guest_access_accounts (tenant_id,site_id,username,password_hash,enabled) VALUES ($1,$2,$3,'x',true) RETURNING id::text`,
		p2Tenant, p2Site, fmt.Sprintf("acct-%d", i))
	authCtxID = scan1(t, db, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`,
		p2Tenant, p2Site, accountID, deviceID, p2GN)
	return
}

// addAuthContext adds another unconsumed ACCOUNT auth_context (fresh device) for an EXISTING account.
func addAuthContext(t *testing.T, db *pgxpool.Pool, accountID string) (deviceID, authCtxID string) {
	t.Helper()
	i := nextSeq()
	deviceID = scan1(t, db, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,$4) RETURNING id::text`,
		p2Tenant, p2Site, p2Appl, fmt.Sprintf("02:00:00:01:%02x:%02x", (i>>8)&0xff, i&0xff))
	authCtxID = scan1(t, db, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`,
		p2Tenant, p2Site, accountID, deviceID, p2GN)
	return
}

// mkPrincipal + a fresh OTP auth_context bound to it.
func newPrincipalChain(t *testing.T, db *pgxpool.Pool) (principalID, deviceID, authCtxID string) {
	t.Helper()
	i := nextSeq()
	principalID = scan1(t, db, `INSERT INTO iam_v2.guest_principals (tenant_id,display_name) VALUES ($1,$2) RETURNING id::text`, p2Tenant, fmt.Sprintf("guest-%d", i))
	deviceID = scan1(t, db, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,$4) RETURNING id::text`,
		p2Tenant, p2Site, p2Appl, fmt.Sprintf("02:00:00:02:%02x:%02x", (i>>8)&0xff, i&0xff))
	authCtxID = scan1(t, db, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_principal_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'OTP',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`,
		p2Tenant, p2Site, principalID, deviceID, p2GN)
	return
}

// mkVoucher + a fresh VOUCHER auth_context bound to it (seeds the code-key generation the FK needs).
func newVoucherChain(t *testing.T, db *pgxpool.Pool, pkgRevID string) (voucherID, deviceID, authCtxID string) {
	t.Helper()
	i := nextSeq()
	gen := scan1(t, db, `INSERT INTO iam_v2.voucher_code_key_generations (tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		VALUES ($1,$2,$3,'\x00','{}'::jsonb, gen_random_uuid()) RETURNING id::text`, p2Tenant, p2Site, i)
	voucherID = scan1(t, db, `INSERT INTO iam_v2.vouchers (tenant_id,site_id,package_revision_id,code_hmac,code_ciphertext,code_nonce,code_key_generation_id,code_last4)
		VALUES ($1,$2,$3::uuid, $4, '\x00','\x00', $5::uuid, '0000') RETURNING id::text`,
		p2Tenant, p2Site, pkgRevID, fmt.Appendf(nil, "hmac-%d", i), gen)
	deviceID = scan1(t, db, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,$4) RETURNING id::text`,
		p2Tenant, p2Site, p2Appl, fmt.Sprintf("02:00:00:03:%02x:%02x", (i>>8)&0xff, i&0xff))
	authCtxID = scan1(t, db, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,voucher_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'VOUCHER',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`,
		p2Tenant, p2Site, voucherID, deviceID, p2GN)
	return
}

// ---- item 6: every Quote/Purchase money pin substitution is rejected by PostgreSQL ----

func TestC6PurchasePinSubstitutionRejected(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()

	// each case: a fresh quote (unique offer_quote_id) whose purchase substitutes exactly ONE pin.
	base := `INSERT INTO iam_v2.purchases
	  (tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,trigger,amount_minor,currency,currency_exponent,tax_code,tax_rate_bp,tax_amount_minor,settlement_mapping_id,state)
	  VALUES ($1,$2,$3,$4,$5,'GUEST_SELECTION',$6,$7,$8,$9,$10,$11,$12,'PENDING')`

	// the correct (matching) pin set for the free quote.
	type pins struct {
		pkgRev, currency any
		amount           int64
		exp              int
		taxCode          any
		taxRate          any
		taxAmt           any
		mapping          any
	}
	for _, c := range []struct {
		name string
		mut  func(*pins)
	}{
		{"amount", func(p *pins) { p.amount = 500 }},
		{"currency", func(p *pins) { p.currency = "EUR" }},
		{"exponent", func(p *pins) { p.exp = 3 }},
		{"tax_code", func(p *pins) { p.taxCode = "VAT" }},
		{"tax_rate_bp", func(p *pins) { p.taxRate = 100 }},
		{"tax_amount_minor", func(p *pins) { p.taxAmt = int64(5) }},
		{"settlement_mapping_id", func(p *pins) { p.mapping = "55555555-5555-5555-5555-555555555555" }},
		{"package_revision_id", func(p *pins) {
			p.pkgRev = scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
				(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
				VALUES ($1,$2,$3,$4,$5,'GENERAL',0,'USD',2,'{NOT_REQUIRED}') RETURNING id::text`,
				p2Tenant, p2Site, s.packageID, 100+nextSeq(), s.planRevID)
		}},
	} {
		dev, ac := addAuthContext(t, db, s.accountID)
		q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac, PackageID: s.packageID, DeviceID: dev, GuestNetworkID: p2GN})
		if err != nil || q.QuoteID == "" {
			t.Fatalf("%s: quote setup: %+v %v", c.name, q, err)
		}
		p := pins{pkgRev: s.pkgRevID, currency: "USD", amount: 0, exp: 2}
		c.mut(&p)
		_, err = db.Exec(ctx, base, p2Tenant, p2Site, p.pkgRev, q.QuoteID, ac, p.amount, p.currency, p.exp, p.taxCode, p.taxRate, p.taxAmt, p.mapping)
		if err == nil {
			t.Fatalf("substituted %s must be rejected by PostgreSQL", c.name)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.purchases`); n != 0 {
		t.Fatalf("no tampered purchase may persist, got %d", n)
	}
}

// ---- item 7: an offer quote is immutable except the one-time consume ----

func TestC7OfferQuoteImmutable(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	// direct mutation of any frozen pin is rejected.
	for _, m := range []string{
		`UPDATE iam_v2.offer_quotes SET price_minor=999 WHERE id=$1`,
		`UPDATE iam_v2.offer_quotes SET currency='EUR' WHERE id=$1`,
		`UPDATE iam_v2.offer_quotes SET currency_exponent=3 WHERE id=$1`,
		`UPDATE iam_v2.offer_quotes SET grant_snapshot='{}'::jsonb WHERE id=$1`,
		`UPDATE iam_v2.offer_quotes SET expires_at=now()+interval '1 day' WHERE id=$1`,
		`UPDATE iam_v2.offer_quotes SET tax_code='VAT' WHERE id=$1`,
	} {
		if _, err := db.Exec(ctx, m, q.QuoteID); err == nil {
			t.Fatalf("mutation must be rejected: %s", m)
		}
	}
	// the ONLY legal update: consume once (NULL -> timestamp).
	if _, err := db.Exec(ctx, `UPDATE iam_v2.offer_quotes SET consumed_at=now() WHERE id=$1`, q.QuoteID); err != nil {
		t.Fatalf("one-time consume must be allowed: %v", err)
	}
	// re-consume / clear / mutate-after-consume all rejected.
	if _, err := db.Exec(ctx, `UPDATE iam_v2.offer_quotes SET consumed_at=NULL WHERE id=$1`, q.QuoteID); err == nil {
		t.Fatal("clearing consumed_at must be rejected")
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.offer_quotes SET consumed_at=now() WHERE id=$1`, q.QuoteID); err == nil {
		t.Fatal("second consume must be rejected (already consumed)")
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.offer_quotes SET price_minor=1 WHERE id=$1`, q.QuoteID); err == nil {
		t.Fatal("post-consume mutation must be rejected")
	}
}

// ---- item 8: the immutable duration/end policy is stamped onto the entitlement ----

func TestC8DurationWindowStamped(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, func(o *seedOpts) { o.duration = `{"end_mode":"VALIDITY_WINDOW","duration_seconds":3600}` })
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if err != nil || pr.Reason != "granted" {
		t.Fatalf("confirm: %+v %v", pr, err)
	}
	var endMode string
	var window *time.Time
	if err := db.QueryRow(ctx, `SELECT end_mode, window_ends_at FROM iam_v2.entitlements WHERE id=$1`, pr.EntitlementID).Scan(&endMode, &window); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if endMode != "VALIDITY_WINDOW" || window == nil {
		t.Fatalf("expected VALIDITY_WINDOW with a stamped window, got %s / %v", endMode, window)
	}
}

// ---- item 9: fault injection at every mutation boundary -> zero partial rows ----

var errFault = errors.New("injected fault")

type faultRepo struct {
	inner CommerceRepository
	at    string
}

func (r *faultRepo) WithTx(ctx context.Context, fn func(CommerceTx) error) error {
	return r.inner.WithTx(ctx, func(tx CommerceTx) error {
		return fn(&faultTx{CommerceTx: tx, at: r.at})
	})
}

type faultTx struct {
	CommerceTx
	at string
}

func (t *faultTx) trip(name string) error {
	if t.at == name {
		return errFault
	}
	return nil
}

func (t *faultTx) ConsumeOfferQuote(ctx context.Context, id string, now time.Time) (bool, error) {
	ok, err := t.CommerceTx.ConsumeOfferQuote(ctx, id, now)
	if err != nil {
		return ok, err
	}
	return ok, t.trip("consume_quote")
}
func (t *faultTx) ConsumeAuthContextByID(ctx context.Context, id string, now time.Time) (bool, error) {
	ok, err := t.CommerceTx.ConsumeAuthContextByID(ctx, id, now)
	if err != nil {
		return ok, err
	}
	return ok, t.trip("consume_auth")
}
func (t *faultTx) InsertPurchase(ctx context.Context, p PurchaseSpec) (string, error) {
	id, err := t.CommerceTx.InsertPurchase(ctx, p)
	if err != nil {
		return id, err
	}
	return id, t.trip("insert_purchase")
}
func (t *faultTx) InsertSettlement(ctx context.Context, tenantID, siteID, purchaseID string) error {
	if err := t.CommerceTx.InsertSettlement(ctx, tenantID, siteID, purchaseID); err != nil {
		return err
	}
	return t.trip("insert_settlement")
}
func (t *faultTx) TerminateLiveEntitlementForSubject(ctx context.Context, tenantID, siteID string, subj CommerceSubject) (string, error) {
	id, err := t.CommerceTx.TerminateLiveEntitlementForSubject(ctx, tenantID, siteID, subj)
	if err != nil {
		return id, err
	}
	return id, t.trip("terminate_entitlement")
}
func (t *faultTx) InsertEntitlement(ctx context.Context, e EntitlementSpec) (string, error) {
	id, err := t.CommerceTx.InsertEntitlement(ctx, e)
	if err != nil {
		return id, err
	}
	return id, t.trip("insert_entitlement")
}
func (t *faultTx) MarkPurchaseGranted(ctx context.Context, purchaseID string) error {
	if err := t.trip("before_granted"); err != nil { // fail BEFORE marking granted
		return err
	}
	return t.CommerceTx.MarkPurchaseGranted(ctx, purchaseID)
}

func TestC2RollbackAtEveryBoundary(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	ctx := context.Background()
	boundaries := []string{"consume_quote", "consume_auth", "insert_purchase", "insert_settlement", "terminate_entitlement", "insert_entitlement", "before_granted"}
	for _, b := range boundaries {
		acct, dev, ac := newAccountChain(t, db)
		// build a quote with the real engine, then confirm through a fault-injecting repo.
		eq := newEngine(t, db, 5*time.Minute)
		q, err := eq.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac, PackageID: s.packageID, DeviceID: dev, GuestNetworkID: p2GN})
		if err != nil || q.QuoteID == "" {
			t.Fatalf("%s: quote: %+v %v", b, q, err)
		}
		ef, err := NewCommerceEngine(CommerceConfig{MasterEnabled: true, PortalEnabled: true}, &faultRepo{inner: NewPgCommerceRepository(db), at: b}, NopObserver{}, WithQuoteTTL(5*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		pr, cerr := ef.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: dev, GuestNetworkID: p2GN})
		if cerr == nil && pr.Reason == "granted" {
			t.Fatalf("%s: fault must abort the grant, got granted", b)
		}
		// zero partial rows for this subject; quote + auth-context NOT consumed.
		if n := count(t, db, `SELECT count(*) FROM iam_v2.purchases WHERE auth_context_id=$1`, ac); n != 0 {
			t.Fatalf("%s: %d purchase rows leaked", b, n)
		}
		if n := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE guest_account_id=$1`, acct); n != 0 {
			t.Fatalf("%s: %d entitlement rows leaked", b, n)
		}
		if n := count(t, db, `SELECT count(*) FROM iam_v2.settlements s JOIN iam_v2.purchases p ON p.id=s.purchase_id WHERE p.auth_context_id=$1`, ac); n != 0 {
			t.Fatalf("%s: %d settlement rows leaked", b, n)
		}
		if n := count(t, db, `SELECT count(*) FROM iam_v2.offer_quotes WHERE id=$1 AND consumed_at IS NOT NULL`, q.QuoteID); n != 0 {
			t.Fatalf("%s: quote was consumed despite rollback", b)
		}
		if n := count(t, db, `SELECT count(*) FROM iam_v2.auth_contexts WHERE id=$1 AND consumed_at IS NOT NULL`, ac); n != 0 {
			t.Fatalf("%s: auth-context was consumed despite rollback", b)
		}
		// after rollback the quote is still confirmable through the clean engine.
		pr2, err := eq.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: dev, GuestNetworkID: p2GN})
		if err != nil || pr2.Reason != "granted" {
			t.Fatalf("%s: quote must remain grantable after rollback: %+v %v", b, pr2, err)
		}
	}
}

// item 9 (tamper): a non-free (tampered/legacy) quote row can never be confirmed.
func TestC2TamperedQuoteRejected(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()
	// build a canonical snapshot to satisfy the snapshot check, then hand-insert a PRICED quote.
	plan := PlanRevisionRow{ID: s.planRevID, DownKbps: 5000, MaxConcurrentDevices: 2, TimeAccountingMode: "VALIDITY_WINDOW"}
	snap, _ := BuildGrantSnapshot(GrantTier{Order: 10, Value: map[string]any{}}, plan, PackageRevisionRow{ID: s.pkgRevID})
	snap.EndMode = "MANUAL_END"
	for _, bad := range []struct {
		name string
		cols string
		vals []any
	}{
		{"priced", `price_minor,currency,currency_exponent`, []any{int64(500), "USD", 2}},
		{"taxed", `price_minor,currency,currency_exponent,tax_code,tax_amount_minor`, []any{int64(0), "USD", 2, "VAT", int64(5)}},
	} {
		dev, ac := addAuthContext(t, db, s.accountID)
		qid := scan1(t, db, `INSERT INTO iam_v2.offer_quotes
			(tenant_id,site_id,auth_context_id,package_revision_id,grant_snapshot,expires_at,`+bad.cols+`)
			VALUES ($1,$2,$3,$4,$5,now()+interval '5 min',`+phArgs(6, len(bad.vals))+`) RETURNING id::text`,
			append([]any{p2Tenant, p2Site, ac, s.pkgRevID, snap.Canonical()}, bad.vals...)...)
		pr, _ := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: qid, DeviceID: dev, GuestNetworkID: p2GN})
		if pr.Reason == "granted" {
			t.Fatalf("%s tampered quote must not grant", bad.name)
		}
		if n := count(t, db, `SELECT count(*) FROM iam_v2.offer_quotes WHERE id=$1 AND consumed_at IS NOT NULL`, qid); n != 0 {
			t.Fatalf("%s tampered quote must not be consumed", bad.name)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.purchases`); n != 0 {
		t.Fatalf("tampered quotes produced %d purchases, want 0", n)
	}
}

func phArgs(start, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("$%d", start+i)
	}
	return out
}

// ---- item 10: one live entitlement per subject; supersession; cross-subject isolation ----

func confirmFor(t *testing.T, db *pgxpool.Pool, e *CommerceEngine, packageID, dev, ac string) PurchaseResult {
	t.Helper()
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac, PackageID: packageID, DeviceID: dev, GuestNetworkID: p2GN})
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: dev, GuestNetworkID: p2GN})
	if err != nil || pr.Reason != "granted" {
		t.Fatalf("confirm: %+v %v", pr, err)
	}
	return pr
}

func TestC3SubjectUniquenessAndSupersession(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()

	// ACCOUNT subject: two sequential grants -> second supersedes first, exactly one live.
	dev2, ac2 := addAuthContext(t, db, s.accountID)
	first := confirmFor(t, db, e, s.packageID, s.deviceID, s.authCtxID)
	second := confirmFor(t, db, e, s.packageID, dev2, ac2)
	if second.Superseded != first.EntitlementID {
		t.Fatalf("second grant must supersede the first (%s), got %q", first.EntitlementID, second.Superseded)
	}
	var oldStatus, newStatus string
	db.QueryRow(ctx, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, first.EntitlementID).Scan(&oldStatus)
	db.QueryRow(ctx, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, second.EntitlementID).Scan(&newStatus)
	if oldStatus != "TERMINATED" || newStatus != "ACTIVE" {
		t.Fatalf("old must be TERMINATED / new ACTIVE, got %s / %s", oldStatus, newStatus)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE guest_account_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, s.accountID); n != 1 {
		t.Fatalf("account must have exactly one live entitlement, got %d", n)
	}

	// VOUCHER subject: independent live entitlement (does not touch the account's).
	_, vdev, vac := newVoucherChain(t, db, s.pkgRevID)
	confirmFor(t, db, e, s.packageID, vdev, vac)
	// PRINCIPAL (OTP) subject: independent live entitlement.
	_, pdev, pac := newPrincipalChain(t, db)
	confirmFor(t, db, e, s.packageID, pdev, pac)

	// cross-subject isolation: account=1 live, voucher=1 live, principal=1 live; none superseded another.
	if n := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE status='ACTIVE'`); n != 3 {
		t.Fatalf("three distinct subjects must yield 3 live entitlements, got %d", n)
	}
}

// item 10 (concurrency): distinct quotes for the SAME subject, confirmed concurrently, converge on
// exactly one live entitlement with no deadlock/orphan.
func TestC3ConcurrentDistinctQuotesOneLive(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()

	const n = 12
	quotes := make([]string, n)
	devs := make([]string, n)
	// first quote reuses the seeded auth-context; the rest get fresh ones for the same account.
	dev0, ac0 := s.deviceID, s.authCtxID
	q0, _ := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac0, PackageID: s.packageID, DeviceID: dev0, GuestNetworkID: p2GN})
	quotes[0], devs[0] = q0.QuoteID, dev0
	for i := 1; i < n; i++ {
		d, a := addAuthContext(t, db, s.accountID)
		q, _ := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: a, PackageID: s.packageID, DeviceID: d, GuestNetworkID: p2GN})
		quotes[i], devs[i] = q.QuoteID, d
	}
	var wg sync.WaitGroup
	var granted int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: quotes[i], DeviceID: devs[i], GuestNetworkID: p2GN})
			if err == nil && pr.Reason == "granted" {
				atomic.AddInt64(&granted, 1)
			}
		}(i)
	}
	wg.Wait()
	if granted < 1 {
		t.Fatal("at least one distinct-quote confirm must grant")
	}
	// regardless of how many granted (each supersedes the prior), the subject ends with exactly ONE live.
	if live := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE guest_account_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, s.accountID); live != 1 {
		t.Fatalf("subject must converge on exactly one live entitlement, got %d", live)
	}
}

// ---- item 11 (C5): non-PMS revision lifecycle — new revision, pointer move, old pins frozen,
// activation toggle without history rewrite, and NO PMS settlement artifacts anywhere. ----

func TestC5RevisionLifecycleNonPMS(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	ctx := context.Background()

	// snapshot the published revision's pins.
	var oldPrice int64
	var oldRevNo int
	db.QueryRow(ctx, `SELECT price_minor, revision_no FROM iam_v2.internet_package_revisions WHERE id=$1`, s.pkgRevID).Scan(&oldPrice, &oldRevNo)

	// publish a NEWER revision of the same package and move the current-revision pointer.
	newRev := scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy,display)
		VALUES ($1,$2,$3,$4,$5,'GENERAL',0,'USD',2,'{NOT_REQUIRED}','{"end_mode":"MANUAL_END"}'::jsonb,'{"name":"v2"}'::jsonb) RETURNING id::text`,
		p2Tenant, p2Site, s.packageID, oldRevNo+1, s.planRevID)
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, newRev, s.packageID); err != nil {
		t.Fatalf("pointer move: %v", err)
	}
	// same-parent/tenant/site invariant for the pointed revision.
	if n := count(t, db, `SELECT count(*) FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id
		WHERE p.id=$1 AND r.package_id=p.id AND r.tenant_id=p.tenant_id AND r.site_id=p.site_id`, s.packageID); n != 1 {
		t.Fatal("current revision must share parent/tenant/site")
	}
	// old revision pins are unchanged (and immutable).
	var chkPrice int64
	db.QueryRow(ctx, `SELECT price_minor FROM iam_v2.internet_package_revisions WHERE id=$1`, s.pkgRevID).Scan(&chkPrice)
	if chkPrice != oldPrice {
		t.Fatalf("old revision pins mutated: %d -> %d", oldPrice, chkPrice)
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_package_revisions SET price_minor=1 WHERE id=$1`, s.pkgRevID); err == nil {
		t.Fatal("published revision must be immutable")
	}

	// deactivate + reactivate the package: a package-row toggle, no revision history rewrite.
	before := count(t, db, `SELECT count(*) FROM iam_v2.internet_package_revisions WHERE package_id=$1`, s.packageID)
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_packages SET active=false WHERE id=$1`, s.packageID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_packages SET active=true WHERE id=$1`, s.packageID); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if after := count(t, db, `SELECT count(*) FROM iam_v2.internet_package_revisions WHERE package_id=$1`, s.packageID); after != before {
		t.Fatalf("activation toggle rewrote revision history: %d -> %d", before, after)
	}

	// NO PMS settlement artifacts were created or used by any Phase-2 path.
	if n := count(t, db, `SELECT count(*) FROM iam_v2.package_settlement_mappings`); n != 0 {
		t.Fatalf("no settlement mapping may exist in Phase 2, got %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.settlements WHERE method <> 'NOT_REQUIRED'`); n != 0 {
		t.Fatalf("no non-NOT_REQUIRED settlement may exist, got %d", n)
	}
}
