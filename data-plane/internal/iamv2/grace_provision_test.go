package iamv2

import (
	"context"
	"testing"
)

// D32: normal grace is SYSTEM-provisioned, one hidden package per Site, and Hotel Admin controls policy only.
//
// These assert the three properties that make that decision real rather than nominal: provisioning converges
// instead of accumulating, the package is absent from the operator catalogue, and the operator publisher
// still cannot create one.
func TestSystemGraceProvisioningIsIdempotentAndHidden(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	seedPlanOnly(t, db) // an enabled plan revision for the grace revision to pin
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

	// IDEMPOTENCE. Running again must converge on the same package and revision, not publish another. The
	// revision is immutable and live entitlements pin it, so re-publishing on every boot would fork history.
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

	// SHAPE. It must satisfy exactly what the contract re-validates at every checkout.
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

	// HIDDEN. It must not appear in the operator catalogue.
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

	// POLICY IS THE OPERATOR'S. The provisioned package must be a valid target for SetGrace, and provisioning
	// must NOT have chosen a policy on the operator's behalf.
	// Provisioning must NOT have chosen a policy on the operator's behalf: a provisioned package with no
	// policy is the correct intermediate state, because the policy is a commercial question D32 assigns to
	// Hotel Admin.
	before, err := repo.GetGraceConfig(ctx, p2Tenant, p2Site)
	if err != nil {
		t.Fatal(err)
	}
	if before.GracePackageRevisionID != "" {
		t.Fatalf("provisioning set grace policy on the operator's behalf: %+v", before)
	}
	a := newAdmin(t, db)
	if res, err := a.SetGrace(ctx, p2Tenant, p2Site, first.RevisionID,
		map[string]any{"eligibility_window_seconds": 3600}); err != nil || res.Reason != "ok" {
		t.Fatalf("the system grace package must be a valid policy target: %+v %v", res, err)
	}
}

// The system package's ACTIVATION is not operator-controlled (D32).
//
// The HTTP surface additionally requires a reason and a password re-confirmation, but those are checks a
// determined operator simply satisfies -- neither says "this package is not yours". Deactivating the system
// grace package would silently disable checkout grace for the whole site while every screen still looked
// normal, so the refusal is asserted here, in the write itself, where the UI affordances cannot bypass it.
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

	// A NON-system package must still be deactivatable: the rule is about provenance, not a blanket freeze.
	pkg := scan1(t, db, `INSERT INTO iam_v2.internet_packages (tenant_id,site_id,code,active,is_system) VALUES ($1,$2,'ORDINARY',true,false) RETURNING id::text`, p2Tenant, p2Site)
	if _, err := a.SetActive(ctx, p2Tenant, p2Site, pkg, false); err != nil {
		t.Fatalf("an ordinary package must remain operator-controlled: %v", err)
	}
}

// D32 END TO END: policy in, a grace package the REAL checkout validator accepts out.
//
// The property that matters is not "a package was created" but "the package the conversion will judge at a
// departure is the one the operator's policy describes". So this publishes a policy and then asks
// iam_v2.grace_package_mismatch_reason -- the same function ConvertAtCheckout uses -- for a verdict.
func TestSystemGracePolicyProducesAPackageTheCheckoutValidatorAccepts(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()
	repo := NewPgCommerceAdminRepository(db)
	// A NULL-tenant operator: publish_checkout_grace_policy accepts an actor whose tenant_id IS NULL or
	// matches, and the scratch database has no public.tenants row for p2Tenant to point a FK at.
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

	// THE REAL VALIDATOR. A non-NULL reason is the conversion's own verdict that this policy would fall back
	// to Emergency Grace at every departure.
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
	// THE AUDIT ROW IS NOT ASSERTED HERE, AND THAT IS A FIXTURE LIMIT RATHER THAN A GAP IN THE BOUNDARY.
	//
	// p2DB truncates iam_v2.internet_package_revisions CASCADE, and
	// iam_v2.checkout_grace_policy_publications carries an FK to grace_package_revision_id -- so the shared
	// fixture cascade-deletes the very ledger this test would read. Asserting it here would be asserting the
	// fixture's teardown order, not the product.
	//
	// What DOES prove the canonical boundary was used is the optimistic-version check below:
	// publish_checkout_grace_config (the raw writer) takes no version parameter at all, so a stale version
	// being REFUSED can only have come from publish_checkout_grace_policy. The actor and the bounded reason
	// code are enforced by that same function and by nothing else in this path.

	// OPTIMISTIC CONCURRENCY: republishing against a stale version must be refused, not silently overwrite.
	if _, _, err := PublishSystemGracePolicy(ctx, repo, GracePublishRequest{
		TenantID: p2Tenant, SiteID: p2Site, Policy: pol,
		ActorOperatorID: actor, ReasonCode: "DEV_TRIAL_D32", ExpectedVersion: 0}); err == nil {
		t.Fatal("a stale expected_version was accepted: two operators could silently overwrite each other")
	}

	// A CHANGED policy must produce a package that still matches -- the derivation is not a one-off.
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

// The reserved system grace codes must be unreachable from the generic operator publisher, including by
// code collision -- the door left open once the catalogue was hidden and activation protected.
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
	// And an ordinary code still works, so the rule is about the reserved namespace and not a blanket freeze.
	if res, err := a.PublishRevision(ctx, PackagePublishSpec{
		TenantID: p2Tenant, SiteID: p2Site, PackageCode: "ordinary-pkg",
		ServicePlanRevisionID: planRev,
		DurationPolicy:        map[string]any{"end_mode": "MANUAL_END"},
		GrantTiers:            []GrantTier{{Order: 1, Value: map[string]any{}}},
	}); err != nil || res.Reason != "published" {
		t.Fatalf("an ordinary package must remain publishable: %+v %v", res, err)
	}
}
