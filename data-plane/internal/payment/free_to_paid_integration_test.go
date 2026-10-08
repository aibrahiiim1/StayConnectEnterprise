//go:build integration

package payment

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// IDENTITY -> ELIGIBILITY -> FREE -> FREE SPENT -> CARD -> PROVIDER-VERIFIED CAPTURE -> GRANT, END TO END.
//
// Contract: docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §4-§6 over the acquisition contract
// §6.3. One verified Client (an email code) on one device, on the real SQL pipeline, with the payment provider
// scripted (no network, no money): the browser never proves anything, only the provider's answer does.
//
// Runs under scripts/phase4-pg-integration.sh (PHASE4_TEST_DSN + the runtime/outcome role pools).

func TestIntegration_FreeToPaidThroughTheExistingPipeline(t *testing.T) {
	admin := pool(t)
	ctx := context.Background()
	s := seedFreeToPaid(t, admin)

	engine, err := iamv2.NewCommerceEngine(iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true},
		iamv2.NewPgCommerceRepository(admin), iamv2.NopObserver{}, iamv2.WithQuoteTTL(5*time.Minute),
		iamv2.WithMethodGate(func(context.Context) iamv2.SiteMethods { return iamv2.SiteMethods{PaidAccess: true, Card: true} }))
	if err != nil {
		t.Fatal(err)
	}
	list := func(ac string) iamv2.PackageListResult {
		t.Helper()
		l, err := engine.ListEligiblePackages(ctx, iamv2.PackageListRequest{TenantID: s.tenant, SiteID: s.site, AuthContextID: ac, DeviceID: s.device, GuestNetworkID: s.network})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return l
	}
	offered := func(l iamv2.PackageListResult) map[string][]string {
		m := map[string][]string{}
		for _, p := range l.Packages {
			m[p.PackageID] = p.Methods
		}
		return m
	}

	// 1. The Client's first visit: the free package and the priced package are both offered.
	l := list(s.ctx1)
	o := offered(l)
	if l.FreeAllowanceUsed || o[s.freePkg] == nil || o[s.cardPkg] == nil {
		t.Fatalf("first visit must offer free and card: %+v", l)
	}
	if strings.Join(o[s.cardPkg], ",") != "ONLINE_PAYMENT" {
		t.Fatalf("the priced package is acquired by card only here: %v", o[s.cardPkg])
	}
	// 2. Free is taken: a purchase, settlement NOT_REQUIRED, an ACTIVE entitlement, no money anywhere.
	q, err := engine.CreateQuote(ctx, iamv2.QuoteRequest{TenantID: s.tenant, SiteID: s.site, AuthContextID: s.ctx1, PackageID: s.freePkg, DeviceID: s.device, GuestNetworkID: s.network})
	if err != nil || q.QuoteID == "" {
		t.Fatalf("free quote: %+v %v", q, err)
	}
	free, err := engine.ConfirmPurchase(ctx, iamv2.ConfirmRequest{TenantID: s.tenant, SiteID: s.site, QuoteID: q.QuoteID, DeviceID: s.device, GuestNetworkID: s.network})
	if err != nil || free.EntitlementID == "" || free.AwaitingSettlement {
		t.Fatalf("free confirm: %+v %v", free, err)
	}
	if st := scan1[string](t, admin, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, free.EntitlementID); st != "ACTIVE" {
		t.Fatalf("free entitlement %s", st)
	}
	// 3. The same Client comes back (a fresh context, same identity): the free allowance is spent and SAID to
	//    be spent; the priced package is what remains. This is the upgrade journey.
	l = list(s.ctx2)
	o = offered(l)
	if !l.FreeAllowanceUsed || o[s.freePkg] != nil || o[s.cardPkg] == nil {
		t.Fatalf("spent free must be withheld and explained, card offered: %+v", l)
	}
	// 4. Card is chosen: purchase AWAITING_SETTLEMENT, settlement ONLINE_PAYMENT/REQUIRED, nothing granted.
	q, err = engine.CreateQuote(ctx, iamv2.QuoteRequest{TenantID: s.tenant, SiteID: s.site, AuthContextID: s.ctx2, PackageID: s.cardPkg, DeviceID: s.device, GuestNetworkID: s.network, Method: "ONLINE_PAYMENT"})
	if err != nil || q.QuoteID == "" {
		t.Fatalf("card quote: %+v %v", q, err)
	}
	paid, err := engine.ConfirmPurchase(ctx, iamv2.ConfirmRequest{TenantID: s.tenant, SiteID: s.site, QuoteID: q.QuoteID, DeviceID: s.device, GuestNetworkID: s.network})
	if err != nil || !paid.AwaitingSettlement || paid.SettlementID == "" || paid.EntitlementID != "" {
		t.Fatalf("card confirm must await settlement and grant nothing: %+v %v", paid, err)
	}
	if st := scan1[string](t, admin, `SELECT status FROM iam_v2.settlements WHERE id=$1`, paid.SettlementID); st != "REQUIRED" {
		t.Fatalf("settlement %s", st)
	}
	// 5. The provider checkout, scripted: created once; while OPEN (and whatever the browser says) nothing is
	//    granted; a CAPTURED answer at the pinned amount settles and grants, exactly once.
	k := testKey(t)
	if _, err := SaveAccount(ctx, admin, k, s.tenant, s.site, "", "stripe", "acct_f2p_"+s.site[:8], "Stripe", "USD", ModeTest,
		"ACTIVE", true, Credentials{"secret_key": "sk_test_x"}, "test-operator", "free-to-paid proof"); err != nil {
		t.Fatalf("save account: %v", err)
	}
	prov := &scriptedHosted{status: StatusResult{State: CheckoutOpen}}
	ce := &CheckoutEngine{Runtime: rolePool(t, "PHASE4_RUNTIME_DSN"), Outcome: rolePool(t, "PHASE4_OUTCOME_DSN"),
		Adapters: map[string]HostedCheckoutProvider{"stripe": prov}, Key: k, Health: NewProviderHealth()}
	st, err := ce.StartCheckout(ctx, s.tenant, s.site, paid.SettlementID, "Day pass", "http://portal/pay/return?p="+paid.PurchaseID, "http://portal/pay/return?p="+paid.PurchaseID+"&cancelled=1")
	if err != nil || !strings.HasPrefix(st.RedirectURL, "https://") {
		t.Fatalf("start checkout: %+v %v", st, err)
	}
	r, _ := ce.Reconcile(ctx, s.tenant, s.site, st.TransactionID)
	if r.EntitlementID != "" || r.TransactionStatus != "PENDING" {
		t.Fatalf("an open checkout grants nothing: %+v", r)
	}
	prov.status = StatusResult{State: CheckoutCaptured, ProviderTxnRef: "pi_f2p", AmountMinor: s.amount, Currency: "usd"}
	for i := 0; i < 3; i++ {
		if r, err = ce.Reconcile(ctx, s.tenant, s.site, st.TransactionID); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if r.EntitlementID == "" || r.SettlementStatus != "SETTLED" || r.PurchaseState != "GRANTED" {
		t.Fatalf("a verified capture settles and grants: %+v", r)
	}
	if prov.creates != 1 {
		t.Fatalf("one checkout, got %d", prov.creates)
	}
	// 6. One live entitlement per Client: the paid access superseded the spent free one; same principal, same
	//    device; the ledger holds exactly one CHARGE and no money moved anywhere real.
	var live, status, subject string
	if err := admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('PENDING','ACTIVE','SUSPENDED'))::text,
	        (SELECT status FROM iam_v2.entitlements WHERE id=$2), (SELECT guest_principal_id::text FROM iam_v2.entitlements WHERE id=$3)
	   FROM iam_v2.entitlements WHERE guest_principal_id=$1::uuid`, s.principal, free.EntitlementID, r.EntitlementID).Scan(&live, &status, &subject); err != nil {
		t.Fatal(err)
	}
	if live != "1" || status != "TERMINATED" || subject != s.principal {
		t.Fatalf("live=%s free=%s paid-subject=%s (want 1, TERMINATED, %s)", live, status, subject, s.principal)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.payment_transactions WHERE tenant_id=$1 AND site_id=$2 AND transaction_type='CHARGE'`, s.tenant, s.site); n != 1 {
		t.Fatalf("exactly one charge row, got %d", n)
	}
	// 7. And the spent free allowance stays spent after the upgrade: a third visit offers nothing free.
	if l = list(s.ctx3); !l.FreeAllowanceUsed || offered(l)[s.freePkg] != nil {
		t.Fatalf("free stays spent after the paid upgrade: %+v", l)
	}
}

type f2pScope struct {
	tenant, site, network, device, principal, freePkg, cardPkg, ctx1, ctx2, ctx3 string
	amount                                                                       int64
}

// seedFreeToPaid: one tenant/site, one client network and device, one verified Client (email), a free package
// with a once-per-client allowance, a priced package acquired by card, and three one-time contexts for the
// Client's three visits.
func seedFreeToPaid(t *testing.T, p *pgxpool.Pool) f2pScope {
	t.Helper()
	ctx := context.Background()
	u := time.Now().UnixNano()
	s := f2pScope{amount: 500}
	if err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM (SELECT gen_random_uuid() g) x RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, (SELECT gen_random_uuid() g) x RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	oct := func(sh uint) int64 { return (u >> sh) & 0xff }
	s.network = scan1[string](t, p, `INSERT INTO public.guest_networks (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr)
		VALUES (gen_random_uuid(),$1,$2,'net',$3,$4,'10.98.0.1/24','10.98.0.1','10.98.0.0/24') RETURNING id::text`,
		s.tenant, s.site, fmt.Sprintf("eth%d", u%100000000), fmt.Sprintf("f%x", u)[:15])
	s.device = scan1[string](t, p, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac)
		VALUES ($1,$2,gen_random_uuid(),$3) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("02:00:01:%02x:%02x:%02x", oct(0), oct(8), oct(16)))
	s.principal = scan1[string](t, p, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, s.tenant)
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.guest_principal_identities (tenant_id, guest_principal_id, factor_type, factor_issuer, factor_value_norm, verified_at, attrs)
		VALUES ($1,$2,'EMAIL','',$3,now(),'{"source":"otp"}')`, s.tenant, s.principal, fmt.Sprintf("client%d@example.com", u)); err != nil {
		t.Fatal(err)
	}
	plan := scan1[string](t, p, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code) VALUES ($1,$2,$3) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("F%d", u))
	planRev := scan1[string](t, p, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,down_kbps,up_kbps,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',5000,2000,2,'VALIDITY_WINDOW',1000000000) RETURNING id::text`, s.tenant, s.site, plan)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.service_plans SET current_revision_id=$1 WHERE id=$2`, planRev, plan); err != nil {
		t.Fatal(err)
	}
	pkg := func(code string, price int64, methods string, display string) (string, string) {
		id := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,active) VALUES ($1,$2,$3,true) RETURNING id::text`, s.tenant, s.site, code)
		rev := scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
			(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy,display)
			VALUES ($1,$2,$3,1,$4,'GENERAL',$5,'USD',2,$6::text[],'{"end_mode":"MANUAL_END"}'::jsonb,$7::jsonb) RETURNING id::text`,
			s.tenant, s.site, id, planRev, price, methods, display)
		if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, rev, id); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.package_grant_tiers (tenant_id,site_id,package_revision_id,tier_order,grant_value) VALUES ($1,$2,$3,10,'{"down_kbps":5000}'::jsonb)`, s.tenant, s.site, rev); err != nil {
			t.Fatal(err)
		}
		return id, rev
	}
	var freeRev string
	s.freePkg, freeRev = pkg(fmt.Sprintf("FREE%d", u), 0, "{NOT_REQUIRED}", `{"name":"Free 30 minutes"}`)
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value) VALUES ($1,$2,$3,'PRIOR_PURCHASE','{"forbids_prior":true}'::jsonb)`, s.tenant, s.site, freeRev); err != nil {
		t.Fatal(err)
	}
	s.cardPkg, _ = pkg(fmt.Sprintf("DAY%d", u), s.amount, "{ONLINE_PAYMENT}", `{"name":"Day pass"}`)
	mk := func() string {
		return guarded(t, p, "auth_context", `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_principal_id,device_id,guest_network_id,expires_at)
			VALUES ($1,$2,'OTP',$3,$4,$5, now()+interval '10 minutes') RETURNING id::text`, s.tenant, s.site, s.principal, s.device, s.network)
	}
	s.ctx1, s.ctx2, s.ctx3 = mk(), mk(), mk()
	return s
}
