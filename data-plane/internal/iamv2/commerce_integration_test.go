package iamv2

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Phase-2 commerce integration tests (C2/C3/C4) against a disposable iam_v2 database. Set
// PHASE2_TEST_DSN to run; skipped otherwise.

func p2DB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE2_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE2_TEST_DSN not set; skipping Phase-2 commerce integration")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// clean commerce/session/entitlement subtree this test writes (disposable DB)
	_, err = db.Exec(context.Background(), `TRUNCATE
		iam_v2.entitlement_devices, iam_v2.entitlement_adjustments, iam_v2.entitlements,
		iam_v2.settlements, iam_v2.purchases, iam_v2.offer_quotes, iam_v2.auth_contexts,
		iam_v2.package_grant_tiers, iam_v2.package_eligibility_rules,
		iam_v2.internet_package_revisions, iam_v2.internet_packages,
		iam_v2.service_plan_revisions, iam_v2.service_plans,
		iam_v2.devices, iam_v2.guest_access_accounts, public.guest_networks CASCADE`)
	if err != nil {
		t.Fatalf("truncate (schema applied?): %v", err)
	}
	return db
}

const (
	p2Tenant = "11111111-1111-1111-1111-111111111111"
	p2Site   = "22222222-2222-2222-2222-222222222222"
	p2Appl   = "33333333-3333-3333-3333-333333333333"
	p2GN     = "44444444-4444-4444-4444-444444444444"
)

type seed struct {
	packageID, planRevID, pkgRevID, accountID, deviceID, authCtxID string
}

// seedFreeCommerce builds a full free-package chain + an unconsumed ACCOUNT auth_context. `rules`/
// `tiers` are optional JSON specs; opts adjust price/settlement/visibility for negative cases.
func seedFreeCommerce(t *testing.T, db *pgxpool.Pool, opts func(*seedOpts)) seed {
	t.Helper()
	o := seedOpts{price: 0, currency: "USD", exp: 2, settlement: "{NOT_REQUIRED}", tiers: `[{"order":10,"grant":{"down_kbps":5000}}]`}
	if opts != nil {
		opts(&o)
	}
	ctx := context.Background()
	ex := func(sql string, args ...any) {
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	one := func(sql string, args ...any) string {
		var id string
		if err := db.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("seed scan: %v\nSQL: %s", err, sql)
		}
		return id
	}
	var s seed
	ex(`INSERT INTO public.guest_networks (id,tenant_id,site_id,name) VALUES ($1,$2,$3,'net') ON CONFLICT (id) DO NOTHING`, p2GN, p2Tenant, p2Site)
	s.deviceID = one(`INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,'02:00:00:00:00:01') RETURNING id::text`, p2Tenant, p2Site, p2Appl)
	s.accountID = one(`INSERT INTO iam_v2.guest_access_accounts (tenant_id,site_id,username,password_hash,enabled) VALUES ($1,$2,'alice','x',true) RETURNING id::text`, p2Tenant, p2Site)
	// plan + revision (current)
	planID := one(`INSERT INTO iam_v2.service_plans (tenant_id,site_id,code) VALUES ($1,$2,'PLAN1') RETURNING id::text`, p2Tenant, p2Site)
	s.planRevID = one(`INSERT INTO iam_v2.service_plan_revisions (tenant_id,site_id,service_plan_id,revision_no,name,down_kbps,up_kbps,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'p',5000,2000,2,'VALIDITY_WINDOW',1000000000) RETURNING id::text`, p2Tenant, p2Site, planID)
	ex(`UPDATE iam_v2.service_plans SET current_revision_id=$1 WHERE id=$2`, s.planRevID, planID)
	// package + revision (current, free)
	s.packageID = one(`INSERT INTO iam_v2.internet_packages (tenant_id,site_id,code,active) VALUES ($1,$2,'PKG1',$3) RETURNING id::text`, p2Tenant, p2Site, o.active())
	s.pkgRevID = one(`INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,visible_from,visible_until,display)
		VALUES ($1,$2,$3,1,$4,'GENERAL',$5,$6,$7,$8::text[],$9,$10,'{"name":"Free WiFi"}'::jsonb) RETURNING id::text`,
		p2Tenant, p2Site, s.packageID, s.planRevID, o.price, o.currency, o.exp, o.settlement, o.visFrom, o.visUntil)
	ex(`UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, s.pkgRevID, s.packageID)
	if o.rules != "" {
		ex(`INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value)
			SELECT $1,$2,$3, (r->>'type'), (r->'value') FROM jsonb_array_elements($4::jsonb) r`, p2Tenant, p2Site, s.pkgRevID, o.rules)
	}
	if o.tiers != "" {
		ex(`INSERT INTO iam_v2.package_grant_tiers (tenant_id,site_id,package_revision_id,tier_order,grant_value)
			SELECT $1,$2,$3, (r->>'order')::int, (r->'grant') FROM jsonb_array_elements($4::jsonb) r`, p2Tenant, p2Site, s.pkgRevID, o.tiers)
	}
	// auth_context (ACCOUNT), unconsumed, future TTL
	s.authCtxID = one(`INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`, p2Tenant, p2Site, s.accountID, s.deviceID, p2GN)
	return s
}

type seedOpts struct {
	price      int64
	currency   string
	exp        int
	settlement string
	rules      string
	tiers      string
	inactive   bool
	visFrom    *time.Time
	visUntil   *time.Time
}

func (o seedOpts) active() bool { return !o.inactive }

func newEngine(t *testing.T, db *pgxpool.Pool, ttl time.Duration) *CommerceEngine {
	e, err := NewCommerceEngine(CommerceConfig{MasterEnabled: true, PortalEnabled: true}, NewPgCommerceRepository(db), NopObserver{}, WithQuoteTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func req(s seed) QuoteRequest {
	return QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: s.authCtxID, PackageID: s.packageID, DeviceID: s.deviceID, GuestNetworkID: p2GN}
}

// C2: valid free quote + confirm -> GRANTED + one entitlement; auth-context not consumed by quote.
func TestC2QuoteAndFreePurchase(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" || q.Reason != "ok" {
		t.Fatalf("quote: %+v err=%v", q, err)
	}
	// quote must NOT have consumed the auth context
	var consumed *time.Time
	db.QueryRow(ctx, `SELECT consumed_at FROM iam_v2.auth_contexts WHERE id=$1`, s.authCtxID).Scan(&consumed)
	if consumed != nil {
		t.Fatal("CreateQuote must not consume the auth context")
	}
	pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if err != nil || pr.Reason != "granted" || pr.PurchaseID == "" || pr.EntitlementID == "" {
		t.Fatalf("confirm: %+v err=%v", pr, err)
	}
	var state string
	db.QueryRow(ctx, `SELECT state FROM iam_v2.purchases WHERE id=$1`, pr.PurchaseID).Scan(&state)
	if state != "GRANTED" {
		t.Fatalf("purchase state %s want GRANTED", state)
	}
	var setl, ent int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.settlements WHERE purchase_id=$1 AND method='NOT_REQUIRED' AND status='NOT_REQUIRED'`, pr.PurchaseID).Scan(&setl)
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE purchase_id=$1 AND status='ACTIVE'`, pr.PurchaseID).Scan(&ent)
	if setl != 1 || ent != 1 {
		t.Fatalf("want 1 settlement + 1 active entitlement, got %d/%d", setl, ent)
	}
	// replay: second confirm on the consumed quote -> deny, no new purchase
	pr2, _ := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if pr2.Reason == "granted" {
		t.Fatal("replay must not grant a second purchase")
	}
	var purchases int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.purchases`).Scan(&purchases)
	if purchases != 1 {
		t.Fatalf("replay produced %d purchases, want 1", purchases)
	}
}

// C2: quote denials — wrong device/network, cross-tenant, ineligible, no tier, not free, out of window.
func TestC2QuoteDenials(t *testing.T) {
	db := p2DB(t)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()

	s := seedFreeCommerce(t, db, nil)
	// wrong device
	r := req(s)
	r.DeviceID = "99999999-9999-9999-9999-999999999999"
	if q, _ := e.CreateQuote(ctx, r); q.QuoteID != "" || q.Reason != "auth_context_mismatch" {
		t.Fatalf("wrong device must deny: %+v", q)
	}
	// cross-tenant
	r = req(s)
	r.TenantID = "88888888-8888-8888-8888-888888888888"
	if q, _ := e.CreateQuote(ctx, r); q.QuoteID != "" {
		t.Fatalf("cross-tenant must deny: %+v", q)
	}

	// ineligible (voucher-only rule, account subject)
	db2 := p2DB(t)
	si := seedFreeCommerce(t, db2, func(o *seedOpts) { o.rules = `[{"type":"SUBJECT_KIND","value":{"kinds":["VOUCHER"]}}]` })
	ei := newEngine(t, db2, 5*time.Minute)
	if q, _ := ei.CreateQuote(ctx, req(si)); q.QuoteID != "" || q.Reason == "ok" {
		t.Fatalf("ineligible must deny: %+v", q)
	}

	// not free (priced) -> deny
	db3 := p2DB(t)
	sp := seedFreeCommerce(t, db3, func(o *seedOpts) { o.price = 500 })
	ep := newEngine(t, db3, 5*time.Minute)
	if q, _ := ep.CreateQuote(ctx, req(sp)); q.QuoteID != "" || q.Reason != "not_free" {
		t.Fatalf("priced package must deny not_free: %+v", q)
	}

	// no matching tier -> deny
	db4 := p2DB(t)
	sn := seedFreeCommerce(t, db4, func(o *seedOpts) {
		o.tiers = `[{"order":1,"grant":{"match":{"type":"SUBJECT_KIND","kinds":["VOUCHER"]}}}]`
	})
	en := newEngine(t, db4, 5*time.Minute)
	if q, _ := en.CreateQuote(ctx, req(sn)); q.QuoteID != "" || q.Reason != "no_matching_grant_tier" {
		t.Fatalf("no tier must deny: %+v", q)
	}
}

// C2: expired quote cannot be confirmed.
func TestC2ExpiredQuote(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, -1*time.Second) // quotes are born expired
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	pr, _ := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if pr.Reason == "granted" {
		t.Fatal("expired quote must not grant")
	}
}

// C2: ≥24 concurrent confirmations of ONE quote -> exactly one purchase/settlement/live-entitlement.
func TestC2ConcurrentSingleWinner(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	const n = 24
	var wins int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
			if err == nil && pr.Reason == "granted" {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("single-winner violated: %d granted, want 1", wins)
	}
	var pc, sc, ec int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.purchases`).Scan(&pc)
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.settlements`).Scan(&sc)
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE status='ACTIVE'`).Scan(&ec)
	if pc != 1 || sc != 1 || ec != 1 {
		t.Fatalf("want exactly 1 purchase/settlement/live-entitlement, got %d/%d/%d", pc, sc, ec)
	}
}

// C4: published revisions immutable (mg9 triggers) + 0009 purchase<->quote pin trigger.
func TestC4ImmutabilityAndPinTrigger(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	ctx := context.Background()
	// published plan/package revisions cannot be updated
	if _, err := db.Exec(ctx, `UPDATE iam_v2.service_plan_revisions SET down_kbps=1 WHERE id=$1`, s.planRevID); err == nil {
		t.Fatal("service_plan_revision must be immutable")
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_package_revisions SET price_minor=1 WHERE id=$1`, s.pkgRevID); err == nil {
		t.Fatal("internet_package_revision must be immutable")
	}
	// pin trigger: a purchase whose package_revision_id differs from its quote is rejected (free path,
	// NULL pms/settlement, where the composite FK is not enforced).
	e := newEngine(t, db, 5*time.Minute)
	q, _ := e.CreateQuote(ctx, req(s))
	// second package revision (different id) to substitute
	var otherRev string
	db.QueryRow(ctx, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
		VALUES ($1,$2,$3,2,$4,'GENERAL',0,'USD',2,'{NOT_REQUIRED}') RETURNING id::text`, p2Tenant, p2Site, s.packageID, s.planRevID).Scan(&otherRev)
	_, err := db.Exec(ctx,
		`INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,trigger,amount_minor,state)
		 VALUES ($1,$2,$3,$4,$5,'GUEST_SELECTION',0,'PENDING')`, p2Tenant, p2Site, otherRev, q.QuoteID, s.authCtxID)
	if err == nil {
		t.Fatal("purchase with substituted package_revision must be rejected by the pin trigger")
	}
}

// dark: flags OFF -> disabled result, zero repository use (panic repo would trip).
func TestPhase2DarkNoRepo(t *testing.T) {
	e, err := NewCommerceEngine(DefaultCommerceConfig(), nil, NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if q, err := e.CreateQuote(context.Background(), QuoteRequest{TenantID: "t", SiteID: "s", AuthContextID: "a", PackageID: "p", DeviceID: "d", GuestNetworkID: "g"}); err != nil || !q.Disabled {
		t.Fatalf("dark CreateQuote must be Disabled with no repo: %+v %v", q, err)
	}
	if pr, err := e.ConfirmFreePurchase(context.Background(), ConfirmRequest{TenantID: "t", SiteID: "s", QuoteID: "q", DeviceID: "d", GuestNetworkID: "g"}); err != nil || !pr.Disabled {
		t.Fatalf("dark ConfirmFreePurchase must be Disabled: %+v %v", pr, err)
	}
}
