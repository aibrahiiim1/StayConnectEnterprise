package iamv2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func engineWithMethods(t *testing.T, db *pgxpool.Pool, m SiteMethods) *CommerceEngine {
	t.Helper()
	e, err := NewCommerceEngine(CommerceConfig{MasterEnabled: true, PortalEnabled: true}, NewPgCommerceRepository(db),
		NopObserver{}, WithQuoteTTL(5*time.Minute), WithMethodGate(func(context.Context) SiteMethods { return m }))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func execErr(t *testing.T, db *pgxpool.Pool, sql string, args ...any) error {
	t.Helper()
	_, err := db.Exec(context.Background(), sql, args...)
	return err
}

// An issued voucher keeps redeeming the immutable revision it was printed for, through a republish AND a
// normal deactivation of its package. It settles PREPAID, is burned exactly once, and a second use fails.
func TestVoucherSurvivesRepublishAndDeactivation(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()

	voucherID, vdev, vac := newVoucherChain(t, db, s.pkgRevID)

	// Republish (a new current revision) and deactivate the package.
	rev2 := scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy)
		VALUES ($1,$2,$3,2,$4,'GENERAL',0,'USD',2,'{NOT_REQUIRED}','{"end_mode":"MANUAL_END"}'::jsonb) RETURNING id::text`,
		p2Tenant, p2Site, s.packageID, s.planRevID)
	if err := execErr(t, db, `UPDATE iam_v2.internet_packages SET current_revision_id=$1, active=false WHERE id=$2`, rev2, s.packageID); err != nil {
		t.Fatal(err)
	}

	list, err := e.ListEligiblePackages(ctx, PackageListRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: vac, DeviceID: vdev, GuestNetworkID: p2GN})
	if err != nil || len(list.Packages) != 1 || list.Packages[0].Methods[0] != AcquireVoucher {
		t.Fatalf("a voucher is offered its pinned revision by Voucher: %+v %v", list, err)
	}
	q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: vac, DeviceID: vdev, GuestNetworkID: p2GN})
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	pr, err := e.ConfirmPurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: vdev, GuestNetworkID: p2GN})
	if err != nil || pr.Reason != "granted" || pr.EntitlementID == "" {
		t.Fatalf("confirm: %+v %v", pr, err)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.settlements WHERE purchase_id=$1 AND method='PREPAID' AND status='SETTLED'`, pr.PurchaseID); n != 1 {
		t.Fatalf("voucher redemption settles PREPAID/SETTLED, got %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.purchases WHERE id=$1 AND trigger='VOUCHER_REDEMPTION' AND state='GRANTED'`, pr.PurchaseID); n != 1 {
		t.Fatalf("purchase must be a granted VOUCHER_REDEMPTION")
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND package_revision_id=$2`, pr.EntitlementID, s.pkgRevID); n != 1 {
		t.Fatal("the entitlement must be for the pinned revision, not the republished one")
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.vouchers WHERE id=$1 AND state='REDEEMED'`, voucherID); n != 1 {
		t.Fatal("voucher must be burned")
	}
	// A second sign-in with the same (now spent) voucher cannot grant again.
	ac2 := scan1Guarded(t, db, "auth_context", `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,voucher_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'VOUCHER',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`, p2Tenant, p2Site, voucherID, vdev, p2GN)
	q2, _ := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac2, DeviceID: vdev, GuestNetworkID: p2GN})
	if q2.QuoteID != "" {
		pr2, _ := e.ConfirmPurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q2.QuoteID, DeviceID: vdev, GuestNetworkID: p2GN})
		if pr2.Reason == "granted" && !(pr2.EntitlementID == pr.EntitlementID) {
			t.Fatalf("a spent voucher granted a second entitlement: %+v", pr2)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE voucher_id=$1`, voucherID); n != 1 {
		t.Fatalf("exactly one entitlement per voucher, got %d", n)
	}
}

// A voucher is ISSUED only against the current revision of an active package that accepts Voucher.
func TestVoucherIssuanceGate(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, func(o *seedOpts) { o.settlement = "{NOT_REQUIRED}" })
	gen := scan1(t, db, `INSERT INTO iam_v2.voucher_code_key_generations (tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		VALUES ($1,$2,$3,'\x00','{}'::jsonb, gen_random_uuid()) RETURNING id::text`, p2Tenant, p2Site, 9000+nextSeq())
	ins := func(rev string) error {
		return execErr(t, db, `INSERT INTO iam_v2.vouchers (tenant_id,site_id,package_revision_id,code_hmac,code_ciphertext,code_nonce,code_key_generation_id,code_last4)
			VALUES ($1,$2,$3::uuid, gen_random_bytes(16), '\x00','\x00', $4::uuid, '0000')`, p2Tenant, p2Site, rev, gen)
	}
	if err := ins(s.pkgRevID); err == nil || !strings.Contains(err.Error(), "VOUCHER_NOT_ACCEPTED") {
		t.Fatalf("a package without Voucher must refuse issuance: %v", err)
	}
	rev2 := scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
		VALUES ($1,$2,$3,2,$4,'GENERAL',0,'USD',2,'{NOT_REQUIRED,PREPAID}') RETURNING id::text`, p2Tenant, p2Site, s.packageID, s.planRevID)
	if err := ins(rev2); err == nil || !strings.Contains(err.Error(), "VOUCHER_REVISION_NOT_CURRENT") {
		t.Fatalf("a non-current revision must refuse issuance: %v", err)
	}
	if err := execErr(t, db, `UPDATE iam_v2.internet_packages SET current_revision_id=$1, active=false WHERE id=$2`, rev2, s.packageID); err != nil {
		t.Fatal(err)
	}
	if err := ins(rev2); err == nil || !strings.Contains(err.Error(), "VOUCHER_PACKAGE_INACTIVE") {
		t.Fatalf("an inactive package must refuse issuance: %v", err)
	}
	if err := execErr(t, db, `UPDATE iam_v2.internet_packages SET active=true WHERE id=$1`, s.packageID); err != nil {
		t.Fatal(err)
	}
	if err := ins(rev2); err != nil {
		t.Fatalf("current, active, Voucher-accepting revision must issue: %v", err)
	}
}

// Card payment: the confirm creates the purchase and a REQUIRED settlement and grants NOTHING. The paid grant
// entry point refuses until the settlement is SETTLED.
func TestCardConfirmGrantsNothing(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, func(o *seedOpts) { o.price = 2500; o.settlement = "{ONLINE_PAYMENT}" })
	e := engineWithMethods(t, db, SiteMethods{PaidAccess: true, Card: true})
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: s.authCtxID, PackageID: s.packageID,
		DeviceID: s.deviceID, GuestNetworkID: p2GN, Method: AcquireCard})
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	pr, err := e.ConfirmPurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if err != nil || !pr.AwaitingSettlement || pr.EntitlementID != "" || pr.SettlementID == "" {
		t.Fatalf("card confirm must await settlement and grant nothing: %+v %v", pr, err)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.entitlements WHERE purchase_id=$1`, pr.PurchaseID); n != 0 {
		t.Fatal("no entitlement may exist before the money is verified")
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.purchases WHERE id=$1 AND state='AWAITING_SETTLEMENT' AND amount_minor=2500`, pr.PurchaseID); n != 1 {
		t.Fatal("purchase must await settlement at the pinned price")
	}
	err = execErr(t, db, `SELECT * FROM iam_v2.p4_grant_paid_entitlement($1,$2,$3)`, p2Tenant, p2Site, pr.SettlementID)
	if err == nil || !strings.Contains(err.Error(), "GRANT_NOT_SETTLED") {
		t.Fatalf("the paid grant must refuse an unsettled card payment: %v", err)
	}
}

// The site gates are re-checked at confirm: a method switched off after the quote is refused and nothing is
// consumed.
func TestMethodRecheckedAtConfirm(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, func(o *seedOpts) { o.price = 2500; o.settlement = "{ONLINE_PAYMENT}" })
	on := SiteMethods{PaidAccess: true, Card: true}
	gate := on
	e, err := NewCommerceEngine(CommerceConfig{MasterEnabled: true, PortalEnabled: true}, NewPgCommerceRepository(db),
		NopObserver{}, WithMethodGate(func(context.Context) SiteMethods { return gate }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q, _ := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: s.authCtxID, PackageID: s.packageID,
		DeviceID: s.deviceID, GuestNetworkID: p2GN, Method: AcquireCard})
	if q.QuoteID == "" {
		t.Fatalf("quote: %+v", q)
	}
	gate = SiteMethods{PaidAccess: true, Card: false} // e.g. the provider became unreachable
	pr, _ := e.ConfirmPurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if pr.Reason != "method_not_available" {
		t.Fatalf("confirm must re-check the method: %+v", pr)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.offer_quotes WHERE id=$1 AND consumed_at IS NULL`, q.QuoteID); n != 1 {
		t.Fatal("a refused confirm must consume nothing")
	}
}

// The database refuses an incoherent package or settlement, whatever the caller.
func TestAcquisitionDatabaseRules(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	rev := func(price int, methods string) error {
		return execErr(t, db, `INSERT INTO iam_v2.internet_package_revisions
			(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
			VALUES ($1,$2,$3,$4,$5,'GENERAL',$6,'USD',2,$7::text[])`, p2Tenant, p2Site, s.packageID, 500+nextSeq(), s.planRevID, price, methods)
	}
	for _, c := range []struct {
		price   int
		methods string
		ok      bool
	}{
		{0, "{NOT_REQUIRED}", true}, {0, "{PREPAID}", true}, {0, "{NOT_REQUIRED,PREPAID}", true},
		{0, "{ONLINE_PAYMENT}", false}, {0, "{PMS_POSTING}", false},
		{100, "{NOT_REQUIRED}", false}, {100, "{ONLINE_PAYMENT,PMS_POSTING,PREPAID}", true},
		{100, "{MANUAL_APPROVAL}", false}, {100, "{CASH}", false}, {100, "{}", false},
	} {
		err := rev(c.price, c.methods)
		if (err == nil) != c.ok {
			t.Fatalf("price %d methods %s: ok=%v err=%v", c.price, c.methods, c.ok, err)
		}
	}
	// A settlement cannot be born SETTLED on a money rail, nor PREPAID outside a voucher redemption.
	pid := scan1Guarded(t, db, "commerce_intent", `INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,auth_context_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'ADMIN_GRANT',0,'PENDING') RETURNING id::text`, p2Tenant, p2Site, s.pkgRevID, s.authCtxID)
	for _, bad := range [][2]string{{"ONLINE_PAYMENT", "SETTLED"}, {"PMS_POSTING", "SETTLED"}, {"PREPAID", "SETTLED"}, {"NOT_REQUIRED", "SETTLED"}} {
		if err := execGuarded(t, db, "commerce_intent", `INSERT INTO iam_v2.settlements (tenant_id,site_id,purchase_id,method,status) VALUES ($1,$2,$3,$4,$5)`,
			p2Tenant, p2Site, pid, bad[0], bad[1]); err == nil || !strings.Contains(err.Error(), "SETTLEMENT_BIRTH") {
			t.Fatalf("%s/%s must be refused at birth: %v", bad[0], bad[1], err)
		}
	}
}
