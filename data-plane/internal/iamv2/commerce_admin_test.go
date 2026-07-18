package iamv2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dark: admin surface OFF -> disabled, nil repo never used.
func TestCommerceAdminDark(t *testing.T) {
	a, err := NewCommerceAdmin(DefaultCommerceConfig(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := a.PublishRevision(context.Background(), PackagePublishSpec{
		TenantID: "t", SiteID: "s", PackageCode: "PKG", ServicePlanRevisionID: "p",
		GrantTiers: []GrantTier{{Order: 1, Value: map[string]any{}}},
	}); err != nil || !res.Disabled {
		t.Fatalf("dark publish must be Disabled with no repo: %+v %v", res, err)
	}
	if _, disabled, err := a.ListPackages(context.Background(), "t", "s"); err != nil || !disabled {
		t.Fatalf("dark ListPackages must be disabled: %v", err)
	}
}

func newAdmin(t *testing.T, db *pgxpool.Pool) *CommerceAdmin {
	t.Helper()
	a, err := NewCommerceAdmin(CommerceConfig{MasterEnabled: true, AdminEnabled: true}, NewPgCommerceAdminRepository(db), NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// seedPlanOnly creates a guest_network + service_plan + one current plan revision (no package).
func seedPlanOnly(t *testing.T, db *pgxpool.Pool) (planRevID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO public.guest_networks (id,tenant_id,site_id,name) VALUES ($1,$2,$3,'net') ON CONFLICT (id) DO NOTHING`, p2GN, p2Tenant, p2Site); err != nil {
		t.Fatalf("seed gn: %v", err)
	}
	planID := scan1(t, db, `INSERT INTO iam_v2.service_plans (tenant_id,site_id,code) VALUES ($1,$2,$3) RETURNING id::text`, p2Tenant, p2Site, "ADMPLAN")
	planRevID = scan1(t, db, `INSERT INTO iam_v2.service_plan_revisions (tenant_id,site_id,service_plan_id,revision_no,name,down_kbps,up_kbps,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'p',5000,2000,2,'VALIDITY_WINDOW',1000000000) RETURNING id::text`, p2Tenant, p2Site, planID)
	if _, err := db.Exec(ctx, `UPDATE iam_v2.service_plans SET current_revision_id=$1 WHERE id=$2`, planRevID, planID); err != nil {
		t.Fatalf("plan pointer: %v", err)
	}
	return
}

func TestCommerceAdminPublishImmutableAndPointer(t *testing.T) {
	db := p2DB(t)
	planRev := seedPlanOnly(t, db)
	a := newAdmin(t, db)
	ctx := context.Background()

	spec := PackagePublishSpec{
		TenantID: p2Tenant, SiteID: p2Site, PackageCode: "FREEWIFI", ServicePlanRevisionID: planRev,
		Display:        map[string]any{"name": "Free WiFi"},
		DurationPolicy: map[string]any{"end_mode": "MANUAL_END"},
		EligibilityRules: []EligibilityRule{
			{Type: RuleAuthMethod, Value: map[string]any{"methods": []any{"account", "voucher"}}},
		},
		GrantTiers: []GrantTier{{Order: 10, Value: map[string]any{"down_kbps": json.Number("5000")}}},
	}
	r1, err := a.PublishRevision(ctx, spec)
	if err != nil || r1.Reason != "published" || r1.PackageID == "" || r1.CurrentRevisionID == "" {
		t.Fatalf("publish 1: %+v %v", r1, err)
	}
	// current pointer + rule/tier rows present
	if got := scan1(t, db, `SELECT current_revision_id::text FROM iam_v2.internet_packages WHERE id=$1`, r1.PackageID); got != r1.CurrentRevisionID {
		t.Fatalf("current pointer %s != %s", got, r1.CurrentRevisionID)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1`, r1.CurrentRevisionID); n != 1 {
		t.Fatalf("want 1 eligibility rule, got %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.package_grant_tiers WHERE package_revision_id=$1`, r1.CurrentRevisionID); n != 1 {
		t.Fatalf("want 1 grant tier, got %d", n)
	}
	// published revision is free + immutable
	var price int64
	db.QueryRow(ctx, `SELECT price_minor FROM iam_v2.internet_package_revisions WHERE id=$1`, r1.CurrentRevisionID).Scan(&price)
	if price != 0 {
		t.Fatalf("published revision must be free, price=%d", price)
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_package_revisions SET price_minor=1 WHERE id=$1`, r1.CurrentRevisionID); err == nil {
		t.Fatal("published revision must be immutable")
	}

	// publish a SECOND revision -> pointer moves; old revision unchanged; revision_count=2
	r2, err := a.PublishRevision(ctx, spec)
	if err != nil || r2.Reason != "published" || r2.CurrentRevisionID == r1.CurrentRevisionID {
		t.Fatalf("publish 2: %+v %v", r2, err)
	}
	if got := scan1(t, db, `SELECT current_revision_id::text FROM iam_v2.internet_packages WHERE id=$1`, r1.PackageID); got != r2.CurrentRevisionID {
		t.Fatal("pointer did not move to revision 2")
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.internet_package_revisions WHERE package_id=$1`, r1.PackageID); n != 2 {
		t.Fatalf("want 2 revisions, got %d", n)
	}

	// SetActive toggle is a package-row change; revision history unchanged.
	before := count(t, db, `SELECT count(*) FROM iam_v2.internet_package_revisions WHERE package_id=$1`, r1.PackageID)
	if _, err := a.SetActive(ctx, p2Tenant, p2Site, r1.PackageID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if act := count(t, db, `SELECT count(*) FROM iam_v2.internet_packages WHERE id=$1 AND active=false`, r1.PackageID); act != 1 {
		t.Fatal("package must be inactive")
	}
	if after := count(t, db, `SELECT count(*) FROM iam_v2.internet_package_revisions WHERE package_id=$1`, r1.PackageID); after != before {
		t.Fatal("activation toggle rewrote revision history")
	}
}

func TestCommerceAdminPublishRejectsInvalid(t *testing.T) {
	db := p2DB(t)
	planRev := seedPlanOnly(t, db)
	a := newAdmin(t, db)
	ctx := context.Background()
	base := PackagePublishSpec{
		TenantID: p2Tenant, SiteID: p2Site, PackageCode: "BAD", ServicePlanRevisionID: planRev,
		DurationPolicy: map[string]any{"end_mode": "MANUAL_END"},
		GrantTiers:     []GrantTier{{Order: 1, Value: map[string]any{}}},
	}
	cases := []struct {
		name   string
		mut    func(*PackagePublishSpec)
		reason string
	}{
		{"pms-rule", func(s *PackagePublishSpec) {
			s.EligibilityRules = []EligibilityRule{{Type: "ROOM_TYPE", Value: map[string]any{}}}
		}, "invalid_eligibility_rule"},
		{"bad-tier", func(s *PackagePublishSpec) {
			s.GrantTiers = []GrantTier{{Order: 1, Value: map[string]any{"down_kbps": json.Number("-1")}}}
		}, "invalid_grant_tier"},
		{"no-tiers", func(s *PackagePublishSpec) { s.GrantTiers = nil }, "no_grant_tiers"},
		{"pms-duration", func(s *PackagePublishSpec) {
			s.DurationPolicy = map[string]any{"end_mode": "AT_CHECKOUT"}
		}, "invalid_duration_policy"},
		{"foreign-plan", func(s *PackagePublishSpec) {
			s.ServicePlanRevisionID = "99999999-9999-9999-9999-999999999999"
		}, "plan_revision_not_found"},
	}
	for _, c := range cases {
		spec := base
		c.mut(&spec)
		res, err := a.PublishRevision(ctx, spec)
		if err != nil || res.Reason != c.reason {
			t.Fatalf("%s: want reason %q, got %+v err=%v", c.name, c.reason, res, err)
		}
	}
	// no partial package/revision persisted by any rejected publish
	if n := count(t, db, `SELECT count(*) FROM iam_v2.internet_packages WHERE code='BAD'`); n != 0 {
		t.Fatalf("rejected publishes left %d packages", n)
	}
}
