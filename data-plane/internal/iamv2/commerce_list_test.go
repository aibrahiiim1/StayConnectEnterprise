package iamv2

import (
	"context"
	"testing"
	"time"
)

// dark: portal OFF -> Disabled, nil repo untouched.
func TestListEligiblePackagesDark(t *testing.T) {
	e, err := NewCommerceEngine(DefaultCommerceConfig(), nil, NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.ListEligiblePackages(context.Background(), PackageListRequest{
		TenantID: "t", SiteID: "s", AuthContextID: "a", DeviceID: "d", GuestNetworkID: "g",
	})
	if err != nil || !res.Disabled {
		t.Fatalf("dark list must be Disabled: %+v %v", res, err)
	}
}

// C2 listing: only eligible, free, in-window packages appear; ineligible ones are silently excluded;
// listing creates no quote/purchase/entitlement and returns guest-safe display + opaque package ids.
func TestListEligiblePackagesFiltersAndIsReadOnly(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil) // eligible free package "PKG1"
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()

	// add a priced package (must be excluded) sharing the plan/account.
	pricedPkg := scan1(t, db, `INSERT INTO iam_v2.internet_packages (tenant_id,site_id,code,active) VALUES ($1,$2,'PRICED',true) RETURNING id::text`, p2Tenant, p2Site)
	pricedRev := scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy,display)
		VALUES ($1,$2,$3,1,$4,'GENERAL',500,'USD',2,'{NOT_REQUIRED}','{"end_mode":"MANUAL_END"}'::jsonb,'{"name":"Priced"}'::jsonb) RETURNING id::text`,
		p2Tenant, p2Site, pricedPkg, s.planRevID)
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, pricedRev, pricedPkg); err != nil {
		t.Fatalf("priced pointer: %v", err)
	}
	// add a voucher-only eligible free package (account subject must NOT see it).
	vPkg := scan1(t, db, `INSERT INTO iam_v2.internet_packages (tenant_id,site_id,code,active) VALUES ($1,$2,'VOUCHERONLY',true) RETURNING id::text`, p2Tenant, p2Site)
	vRev := scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy,display)
		VALUES ($1,$2,$3,1,$4,'GENERAL',0,'USD',2,'{NOT_REQUIRED}','{"end_mode":"MANUAL_END"}'::jsonb,'{"name":"VoucherOnly"}'::jsonb) RETURNING id::text`,
		p2Tenant, p2Site, vPkg, s.planRevID)
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, vRev, vPkg); err != nil {
		t.Fatalf("v pointer: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO iam_v2.package_grant_tiers (tenant_id,site_id,package_revision_id,tier_order,grant_value) VALUES ($1,$2,$3,10,'{"down_kbps":5000}'::jsonb)`, p2Tenant, p2Site, vRev); err != nil {
		t.Fatalf("v tier: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value) VALUES ($1,$2,$3,'SUBJECT_KIND','{"kinds":["VOUCHER"]}'::jsonb)`, p2Tenant, p2Site, vRev); err != nil {
		t.Fatalf("v rule: %v", err)
	}

	res, err := e.ListEligiblePackages(ctx, PackageListRequest{
		TenantID: p2Tenant, SiteID: p2Site, AuthContextID: s.authCtxID, DeviceID: s.deviceID, GuestNetworkID: p2GN,
	})
	if err != nil || res.Reason != "ok" {
		t.Fatalf("list: %+v %v", res, err)
	}
	if len(res.Packages) != 1 {
		t.Fatalf("account subject must see exactly the one eligible free package, got %d: %+v", len(res.Packages), res.Packages)
	}
	if res.Packages[0].PackageID != s.packageID {
		t.Fatalf("wrong package listed: %s", res.Packages[0].PackageID)
	}
	// guest-safe display only: has free/name, no price internals beyond the zeroed free marker.
	d := res.Packages[0].Display
	if d["free"] != true || d["name"] == nil {
		t.Fatalf("display not guest-safe/complete: %+v", d)
	}
	// read-only: nothing was created.
	for _, tbl := range []string{"offer_quotes", "purchases", "settlements", "entitlements"} {
		if n := count(t, db, `SELECT count(*) FROM iam_v2.`+tbl); n != 0 {
			t.Fatalf("listing created %d rows in %s (must be read-only)", n, tbl)
		}
	}
}

// listing denies on a mismatched device (wrong pin) without disclosing packages.
func TestListEligiblePackagesAuthPin(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	res, _ := e.ListEligiblePackages(context.Background(), PackageListRequest{
		TenantID: p2Tenant, SiteID: p2Site, AuthContextID: s.authCtxID,
		DeviceID: "99999999-9999-9999-9999-999999999999", GuestNetworkID: p2GN,
	})
	if res.Reason != "auth_context_mismatch" || len(res.Packages) != 0 {
		t.Fatalf("wrong-device list must deny with no packages: %+v", res)
	}
}
