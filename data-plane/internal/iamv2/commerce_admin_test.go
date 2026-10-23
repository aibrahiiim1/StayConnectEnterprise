package iamv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

func seedPlanOnly(t *testing.T, db *pgxpool.Pool) (planRevID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO public.guest_networks (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr) VALUES ($1,$2,$3,'net','br-lan','br-guest','10.77.0.1/24','10.77.0.1','10.77.0.0/24') ON CONFLICT (id) DO NOTHING`, p2GN, p2Tenant, p2Site); err != nil {
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
	if got := scan1(t, db, `SELECT current_revision_id::text FROM iam_v2.internet_packages WHERE id=$1`, r1.PackageID); got != r1.CurrentRevisionID {
		t.Fatalf("current pointer %s != %s", got, r1.CurrentRevisionID)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1`, r1.CurrentRevisionID); n != 1 {
		t.Fatalf("want 1 eligibility rule, got %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.package_grant_tiers WHERE package_revision_id=$1`, r1.CurrentRevisionID); n != 1 {
		t.Fatalf("want 1 grant tier, got %d", n)
	}
	var price int64
	db.QueryRow(ctx, `SELECT price_minor FROM iam_v2.internet_package_revisions WHERE id=$1`, r1.CurrentRevisionID).Scan(&price)
	if price != 0 {
		t.Fatalf("published revision must be free, price=%d", price)
	}
	if _, err := db.Exec(ctx, `UPDATE iam_v2.internet_package_revisions SET price_minor=1 WHERE id=$1`, r1.CurrentRevisionID); err == nil {
		t.Fatal("published revision must be immutable")
	}

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
		{"duplicate-step-order", func(s *PackagePublishSpec) {
			s.GrantTiers = []GrantTier{{Order: 10, Value: map[string]any{}}, {Order: 10, Value: map[string]any{}}}
		}, "invalid_grant_tier"},
		{"contradictory-prior-purchase", func(s *PackagePublishSpec) {
			s.EligibilityRules = []EligibilityRule{
				{Type: "PRIOR_PURCHASE", Value: map[string]any{"forbids_prior": true}},
				{Type: "PRIOR_PURCHASE", Value: map[string]any{"requires_prior": true}},
			}
		}, "invalid_eligibility_rule"},
		{"pms-interface-not-uuid", func(s *PackagePublishSpec) {
			s.EligibilityRules = []EligibilityRule{{Type: "PMS_INTERFACE", Value: map[string]any{"pms_interface_ids": []any{"protel"}}}}
		}, "invalid_eligibility_rule"},
		{"three-decimal-card", func(s *PackagePublishSpec) {
			s.PriceMinor, s.Currency, s.CurrencyExponent = 1005, "KWD", 3
			s.AcquisitionMethods = []string{AcquireCard}
		}, "invalid_currency"},
		{"past-fixed-end", func(s *PackagePublishSpec) {
			s.DurationPolicy = map[string]any{"end_mode": "FIXED_AT", "ends_at": "2020-01-01T00:00:00Z"}
		}, "invalid_duration_policy"},
	}
	for _, c := range cases {
		spec := base
		c.mut(&spec)
		res, err := a.PublishRevision(ctx, spec)
		if err != nil || (res.Reason != c.reason && !strings.HasPrefix(res.Reason, c.reason+": ")) {
			t.Fatalf("%s: want reason %q, got %+v err=%v", c.name, c.reason, res, err)
		}
	}
	spec := base
	spec.DurationPolicy = map[string]any{"end_mode": "FIXED_AT", "ends_at": "2020-01-01T00:00:00Z"}
	if res, _ := a.PublishRevision(ctx, spec); !strings.Contains(res.Reason, "must be in the future") {
		t.Fatalf("duration refusal lost its reason: %q", res.Reason)
	}
	spec.PriorDurationPolicy = map[string]any{"end_mode": "FIXED_AT", "ends_at": "2020-01-01T00:00:00Z"}
	spec.DryRun = true
	if res, err := a.PublishRevision(ctx, spec); err != nil || res.Reason != "validated" {
		t.Fatalf("an unchanged past end blocked a republish: %+v err=%v", res, err)
	}
	spec = base
	spec.PriceMinor, spec.Currency, spec.CurrencyExponent = 1010, "KWD", 3
	spec.AcquisitionMethods = []string{AcquireCard}
	spec.DryRun = true
	if res, err := a.PublishRevision(ctx, spec); err != nil || res.Reason != "validated" {
		t.Fatalf("KWD 1.010 by card refused: %+v err=%v", res, err)
	}
	if n := count(t, db, `SELECT count(*) FROM iam_v2.internet_packages WHERE code='BAD'`); n != 0 {
		t.Fatalf("rejected publishes left %d packages", n)
	}
}

func TestCommerceAdminServicePlans(t *testing.T) {
	db := p2DB(t)
	if _, err := db.Exec(context.Background(), `INSERT INTO public.guest_networks (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr) VALUES ($1,$2,$3,'net','br-lan','br-guest','10.77.0.1/24','10.77.0.1','10.77.0.0/24') ON CONFLICT (id) DO NOTHING`, p2GN, p2Tenant, p2Site); err != nil {
		t.Fatalf("gn: %v", err)
	}
	a := newAdmin(t, db)
	ctx := context.Background()

	dk := 5000
	spec := PlanPublishSpec{TenantID: p2Tenant, SiteID: p2Site, PlanCode: "GOLD", Name: "Gold", DownKbps: &dk, MaxConcurrentDevices: 3}
	r1, err := a.PublishPlanRevision(ctx, spec)
	if err != nil || r1.Reason != "published" || r1.PackageID == "" {
		t.Fatalf("publish plan: %+v %v", r1, err)
	}
	plans, disabled, err := a.ListPlans(ctx, p2Tenant, p2Site)
	if err != nil || disabled || len(plans) != 1 || plans[0].Code != "GOLD" || plans[0].RevisionCount != 1 {
		t.Fatalf("list plans: %+v disabled=%v err=%v", plans, disabled, err)
	}
	r2, err := a.PublishPlanRevision(ctx, spec)
	if err != nil || r2.CurrentRevisionID == r1.CurrentRevisionID {
		t.Fatalf("publish plan rev2: %+v %v", r2, err)
	}
	revs, _, err := a.PlanRevisions(ctx, p2Tenant, p2Site, r1.PackageID)
	if err != nil || len(revs) != 2 || !revs[0].IsCurrent || revs[0].RevisionNo != 2 {
		t.Fatalf("plan revisions: %+v %v", revs, err)
	}
	bad := spec
	bad.TimeAccountingMode = "AGGREGATE_ONLINE_TIME"
	// The admin surface returns specific validation reasons on purpose (the operator is trusted, unlike a
	// guest); collapsing them to a generic label made a one-word enum typo cost a debugging cycle.
	res, _ := a.PublishPlanRevision(ctx, bad)
	if res.Reason != "unsupported time_accounting_mode" {
		t.Fatalf("AGGREGATE plan must be rejected with the specific reason, got %+v", res)
	}
	if res.Reason == "invalid_plan_spec" {
		t.Fatalf("admin rejection reason regressed to the generic label")
	}
}

func TestCommerceAdminInspectionPIIFree(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, nil)
	e := newEngine(t, db, 5*time.Minute)
	a := newAdmin(t, db)
	ctx := context.Background()
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	if pr, err := e.ConfirmFreePurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN}); err != nil || pr.Reason != "granted" {
		t.Fatalf("confirm: %+v %v", pr, err)
	}
	quotes, disabled, err := a.Quotes(ctx, p2Tenant, p2Site, 100)
	if err != nil || disabled || len(quotes) != 1 || quotes[0].PriceMinor != 0 {
		t.Fatalf("quotes inspect: %+v disabled=%v err=%v", quotes, disabled, err)
	}
	purchases, _, err := a.Purchases(ctx, p2Tenant, p2Site, 100)
	if err != nil || len(purchases) != 1 || purchases[0].State != "GRANTED" {
		t.Fatalf("purchases inspect: %+v %v", purchases, err)
	}
}

// The retired grace writer must refuse, not write: an operator can no longer name a package, and the
// conversion demands exact equality with the published policy.
func TestRetiredRawGraceWriterRefuses(t *testing.T) {
	db := p2DB(t)
	repo := NewPgCommerceAdminRepository(db)
	ctx := context.Background()
	var got error
	if err := repo.WithTx(ctx, func(tx CommerceAdminTx) error {
		got = tx.UpsertGraceConfig(ctx, p2Tenant, p2Site, "00000000-0000-0000-0000-000000000001",
			map[string]any{"grace_minutes": 30})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var de *Error
	if !errors.As(got, &de) || de.Code != ErrInvalidInput {
		t.Fatalf("the raw grace writer did not refuse: %v", got)
	}
	var n int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		p2Tenant, p2Site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the refused write still produced %d config row(s)", n)
	}
}

// The read must not present stale pre-D32 free-form config as current policy: nothing can write or clear it
// now, so surfacing it would show values the operator cannot change.
func TestGraceReadDoesNotSurfaceStaleFreeFormConfig(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	repo := NewPgCommerceAdminRepository(db)
	pol := SystemGracePolicy{DurationSeconds: 1800, DownKbps: 512, UpKbps: 256,
		DataQuotaBytes: 1048576, DeviceLimit: 2, DeviceLimitPolicy: "REJECT_NEW_DEVICE",
		EligibilityWindowSeconds: 7200}
	if _, _, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site, Policy: pol,
		ActorOperatorID: graceTestActor(t, db), ReasonCode: "TEST_STALE_CONFIG", ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE iam_v2.site_checkout_grace_config SET config = '{"grace_minutes":30,"operator_note":"stale"}'::jsonb
		  WHERE tenant_id=$1 AND site_id=$2`, p2Tenant, p2Site); err != nil {
		t.Fatalf("seed residue: %v", err)
	}
	got, err := repo.GetGraceConfig(ctx, p2Tenant, p2Site)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"grace_minutes", "operator_note"} {
		if _, present := got.Config[k]; present {
			t.Errorf("stale free-form key %q was returned as current configuration", k)
		}
	}
	for k, want := range map[string]any{
		"grace_duration_seconds": 1800, "grace_down_kbps": 512, "grace_up_kbps": 256,
		"grace_device_limit": 2, "grace_device_limit_policy": "REJECT_NEW_DEVICE",
		"eligibility_window_seconds": 7200,
	} {
		if got.Config[k] != want {
			t.Errorf("typed field %q = %v, want %v", k, got.Config[k], want)
		}
	}
	if got.ConfigVersion < 1 {
		t.Errorf("config_version=%d: the read does not tell the operator what to send as expected_version",
			got.ConfigVersion)
	}
}

// tenant_id is NULL because the boundary accepts a NULL or matching tenant and the scratch database has no
// public.tenants row to reference.
func graceTestActor(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	return scan1(t, db, `INSERT INTO public.operators (id,email,password_hash,status,tenant_id)
		VALUES (gen_random_uuid(),'grace-'||substr(md5(random()::text),1,8)||'@dev.local','x','active',NULL)
		RETURNING id::text`)
}
