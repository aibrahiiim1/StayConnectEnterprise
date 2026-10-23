package iamv2

import (
	"context"
	"testing"
)

func TestSystemGraceProvisioningIsIdempotentAndHidden(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	seedPlanOnly(t, db)
	repo := NewPgCommerceAdminRepository(db)
	prov := NewGraceProvisioner(repo)

	first, err := prov.EnsureSiteGracePackage(ctx, repo, p2Tenant, p2Site)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if first.Skipped != "" {
		t.Fatalf("provisioning skipped with an enabled plan present: %s", first.Skipped)
	}
	if !first.Created || !first.RevisionNew || first.PackageID == "" || first.RevisionID == "" {
		t.Fatalf("first provision did not create the package and its revision: %+v", first)
	}

	// Running again must converge on the same package and revision: the revision is immutable and live
	// entitlements pin it.
	second, err := prov.EnsureSiteGracePackage(ctx, repo, p2Tenant, p2Site)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if second.Created || second.RevisionNew {
		t.Fatalf("provisioning is not idempotent: %+v", second)
	}
	if second.PackageID != first.PackageID || second.RevisionID != first.RevisionID {
		t.Fatalf("provisioning moved the package/revision: %+v -> %+v", first, second)
	}

	var isSystem, active bool
	var ptype, settlement string
	var price int64
	if err := db.QueryRow(ctx, `
	    SELECT p.is_system, p.active, r.package_type, r.price_minor, array_to_string(r.settlement_methods,',')
	      FROM iam_v2.internet_packages p
	      JOIN iam_v2.internet_package_revisions r ON r.id = p.current_revision_id
	     WHERE p.id = $1`, first.PackageID).Scan(&isSystem, &active, &ptype, &price, &settlement); err != nil {
		t.Fatal(err)
	}
	if !isSystem || !active || ptype != "CHECKOUT_GRACE" || price != 0 || settlement != "NOT_REQUIRED" {
		t.Fatalf("provisioned package does not match the contract shape: system=%v active=%v type=%s price=%d settlement=%s",
			isSystem, active, ptype, price, settlement)
	}

	pkgs, err := repo.ListPackages(ctx, p2Tenant, p2Site)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p.PackageID == first.PackageID {
			t.Fatal("the system grace package is visible in the operator catalogue: an operator could " +
				"deactivate it and silently disable checkout grace")
		}
	}

	// Provisioning must not choose a policy on the operator's behalf; a package with no policy is the correct
	// intermediate state.
	before, err := repo.GetGraceConfig(ctx, p2Tenant, p2Site)
	if err != nil {
		t.Fatal(err)
	}
	if before.GracePackageRevisionID != "" {
		t.Fatalf("provisioning set grace policy on the operator's behalf: %+v", before)
	}
	// Publishing a policy converges onto the provisioned package rather than creating a competing one.
	_, pkgRev, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site,
		Policy: SystemGracePolicy{DurationSeconds: 1800, DownKbps: 4000, UpKbps: 1500,
			DataQuotaBytes: 524288000, DeviceLimit: 2, DeviceLimitPolicy: "REJECT_NEW_DEVICE",
			EligibilityWindowSeconds: 3600},
		ActorOperatorID: graceTestActor(t, db), ReasonCode: "TEST_PROVISION_CONVERGE", ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("publishing onto the provisioned package failed: %v", err)
	}
	var pkgOfRev string
	if err := db.QueryRow(ctx,
		`SELECT package_id::text FROM iam_v2.internet_package_revisions WHERE id=$1`, pkgRev).Scan(&pkgOfRev); err != nil {
		t.Fatal(err)
	}
	if pkgOfRev != first.PackageID {
		t.Fatalf("publication created a SECOND system grace package (%s) instead of using the provisioned one (%s)",
			pkgOfRev, first.PackageID)
	}
	var n int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.internet_packages WHERE tenant_id=$1 AND site_id=$2 AND is_system`,
		p2Tenant, p2Site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one system package for the site, found %d", n)
	}
}

// Activation of the system package is refused in the write itself, where UI affordances cannot bypass it.
func TestSystemGracePackageActivationIsNotOperatorControlled(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	seedPlanOnly(t, db)
	repo := NewPgCommerceAdminRepository(db)
	prov, err := NewGraceProvisioner(repo).EnsureSiteGracePackage(ctx, repo, p2Tenant, p2Site)
	if err != nil || prov.PackageID == "" {
		t.Fatalf("provision: %+v %v", prov, err)
	}
	a := newAdmin(t, db)
	if _, err := a.SetActive(ctx, p2Tenant, p2Site, prov.PackageID, false); err == nil {
		var active bool
		_ = db.QueryRow(ctx, `SELECT active FROM iam_v2.internet_packages WHERE id=$1`, prov.PackageID).Scan(&active)
		t.Fatalf("an operator deactivated the system grace package (active=%v): checkout grace would be "+
			"silently disabled site-wide", active)
	}
	var active bool
	if err := db.QueryRow(ctx, `SELECT active FROM iam_v2.internet_packages WHERE id=$1`,
		prov.PackageID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("the system grace package was deactivated despite the refusal")
	}

	pkg := scan1(t, db, `INSERT INTO iam_v2.internet_packages (tenant_id,site_id,code,active,is_system) VALUES ($1,$2,'ORDINARY',true,false) RETURNING id::text`, p2Tenant, p2Site)
	if _, err := a.SetActive(ctx, p2Tenant, p2Site, pkg, false); err != nil {
		t.Fatalf("an ordinary package must remain operator-controlled: %v", err)
	}
}

// Publishes a policy, then asks iam_v2.grace_package_mismatch_reason (the function ConvertAtCheckout uses) for
// a verdict.
func TestSystemGracePolicyProducesAPackageTheCheckoutValidatorAccepts(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	repo := NewPgCommerceAdminRepository(db)
	// A NULL-tenant operator: the scratch database has no public.tenants row for p2Tenant to reference.
	actor := scan1(t, db, `INSERT INTO public.operators (id,email,password_hash,status,tenant_id)
		VALUES (gen_random_uuid(),'grace-'||substr(md5(random()::text),1,8)||'@dev.local','x','active',NULL)
		RETURNING id::text`)

	pol := SystemGracePolicy{DurationSeconds: 1800, DownKbps: 4000, UpKbps: 1500,
		DataQuotaBytes: 524288000, DeviceLimit: 2, DeviceLimitPolicy: "REJECT_NEW_DEVICE",
		EligibilityWindowSeconds: 3600}
	v, publishedRev, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site, Policy: pol,
		ActorOperatorID: actor, ReasonCode: "DEV_TRIAL_D32", ExpectedVersion: 0})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if v < 1 {
		t.Fatalf("publication returned version %d", v)
	}

	// A non-NULL reason means the conversion would fall back to Emergency Grace at every departure.
	var pkgRev string
	var reason *string
	_ = publishedRev
	if err := db.QueryRow(ctx, `
	    SELECT grace_package_revision_id::text,
	           iam_v2.grace_package_mismatch_reason(tenant_id, site_id, grace_package_revision_id,
	             grace_duration_seconds, grace_down_kbps, grace_up_kbps, grace_data_quota_bytes,
	             grace_device_limit, grace_device_limit_policy)
	      FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		p2Tenant, p2Site).Scan(&pkgRev, &reason); err != nil {
		t.Fatal(err)
	}
	if reason != nil {
		t.Fatalf("the published policy would be REJECTED at checkout: %s", *reason)
	}
	// The audit row is not asserted: p2DB truncates package revisions CASCADE, which wipes the publications
	// ledger. The stale-version refusal below proves publish_checkout_grace_policy was used, since the raw
	// writer takes no version.

	// Republishing against a stale version must be refused.
	if _, _, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site, Policy: pol,
		ActorOperatorID: actor, ReasonCode: "DEV_TRIAL_D32", ExpectedVersion: 0}); err == nil {
		t.Fatal("a stale expected_version was accepted: two operators could silently overwrite each other")
	}

	pol2 := pol
	pol2.DownKbps = 8000
	pol2.DurationSeconds = 900
	if _, _, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site, Policy: pol2,
		ActorOperatorID: actor, ReasonCode: "DEV_TRIAL_D32", ExpectedVersion: v}); err != nil {
		t.Fatalf("republish with a changed policy: %v", err)
	}
	if err := db.QueryRow(ctx, `
	    SELECT iam_v2.grace_package_mismatch_reason(tenant_id, site_id, grace_package_revision_id,
	             grace_duration_seconds, grace_down_kbps, grace_up_kbps, grace_data_quota_bytes,
	             grace_device_limit, grace_device_limit_policy)
	      FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		p2Tenant, p2Site).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != nil {
		t.Fatalf("the changed policy would be REJECTED at checkout: %s", *reason)
	}
}

func TestOperatorCannotPublishOntoTheSystemGraceCodes(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	a := newAdmin(t, db)
	planRev := seedPlanOnly(t, db)
	for _, code := range []string{"__system_checkout_grace", "__system_checkout_grace_plan"} {
		res, err := a.PublishRevision(ctx, PackagePublishSpec{
			TenantID: p2Tenant, SiteID: p2Site, PackageCode: code,
			ServicePlanRevisionID: planRev,
			DurationPolicy:        map[string]any{"end_mode": "MANUAL_END"},
			GrantTiers:            []GrantTier{{Order: 1, Value: map[string]any{}}},
		})
		if err == nil && res.Reason == "published" {
			t.Fatalf("an operator published onto the reserved system grace code %q", code)
		}
	}
	if res, err := a.PublishRevision(ctx, PackagePublishSpec{
		TenantID: p2Tenant, SiteID: p2Site, PackageCode: "ordinary-pkg",
		ServicePlanRevisionID: planRev,
		DurationPolicy:        map[string]any{"end_mode": "MANUAL_END"},
		GrantTiers:            []GrantTier{{Order: 1, Value: map[string]any{}}},
	}); err != nil || res.Reason != "published" {
		t.Fatalf("an ordinary package must remain publishable: %+v %v", res, err)
	}
}

// Provisioning must not pin the reserved emergency plan: ORDER BY code sorts '__sys_emergency_grace_plan__'
// first on a fresh appliance, and the matcher refuses it (PLAN_IS_EMERGENCY_CATALOG). The correct state is no
// revision yet.
func TestProvisioningSkipsRatherThanPinningAReservedEmergencyPlan(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `SELECT iam_v2.bootstrap_emergency_grace($1,$2)`, p2Tenant, p2Site); err != nil {
		t.Fatalf("bootstrap emergency: %v", err)
	}
	repo := NewPgCommerceAdminRepository(db)
	res, err := NewGraceProvisioner(repo).EnsureSiteGracePackage(ctx, repo, p2Tenant, p2Site)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.RevisionNew || res.RevisionID != "" {
		t.Fatalf("provisioning published a revision with only reserved plans available: %+v", res)
	}
	if res.Skipped == "" {
		t.Fatal("provisioning neither published nor said why it did not")
	}

	var n int
	if err := db.QueryRow(ctx, `
	    SELECT count(*)
	      FROM iam_v2.internet_package_revisions r
	      JOIN iam_v2.internet_packages p ON p.id = r.package_id
	      JOIN iam_v2.service_plan_revisions spr ON spr.id = r.service_plan_revision_id
	      JOIN iam_v2.service_plans sp ON sp.id = spr.service_plan_id
	     WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.code=$3
	       AND sp.code = '__sys_emergency_grace_plan__'`,
		p2Tenant, p2Site, systemGraceCode).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d system grace revision(s) pin the reserved Emergency plan", n)
	}

	// Publishing a policy derives its own plan and package, so the skip is a waiting room, not a dead end.
	ver, rev, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site,
		Policy: SystemGracePolicy{DurationSeconds: 3600, DownKbps: 5000, UpKbps: 2000,
			DataQuotaBytes: 524288000, DeviceLimit: 1, DeviceLimitPolicy: "REJECT_NEW_DEVICE",
			EligibilityWindowSeconds: 86400},
		ActorOperatorID: scan1(t, db, `INSERT INTO public.operators (id,email,password_hash,status,tenant_id)
			VALUES (gen_random_uuid(),'grace-'||substr(md5(random()::text),1,8)||'@dev.local','x','active',NULL)
			RETURNING id::text`),
		ReasonCode: "INITIAL_POLICY", ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("publishing a policy from the skipped state: %v", err)
	}
	if ver != 1 || rev == "" {
		t.Fatalf("publication produced version %d revision %q", ver, rev)
	}
}
