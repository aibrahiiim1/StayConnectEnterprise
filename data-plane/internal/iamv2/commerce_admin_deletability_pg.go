package iamv2

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// WHAT STILL POINTS AT A PACKAGE OR A PLAN, counted with the privileges svc_edged actually holds.
//
// Every table named in these two queries is one svc_edged holds SELECT on in the Gate-P allowlist
// (deploy/gatep/svc-edged-phase2-commerce-grants.sql and svc-edged-phase345-admin-grants.sql):
// internet_packages, internet_package_revisions, service_plans, service_plan_revisions, entitlements,
// purchases, offer_quotes, site_checkout_grace_config and guest_access_accounts.
//
// FOUR REFERENCES ARE DELIBERATELY NOT COUNTED, because this service cannot read them and a reference it cannot
// read must not be reported as zero: iam_v2.vouchers, iam_v2.voucher_batches, iam_v2.auth_context_offers and
// iam_v2.package_settlement_mappings. svc_edged holds nothing on the first two (scd owns the voucher domain) and
// nothing on the last two. They are still covered by the closing DELETE_REQUIRES_SCHEMA_CHANGE reason, which is
// what makes the answer "no" regardless. commerce_admin_deletability_test.go pins this list.
//
// Mentioning one of those tables here -- even in a branch that never runs -- makes PostgreSQL check the
// privilege for the whole statement and fail it. So the SQL below is built from functions a test can read.

// packageReferencesSQL counts references to every revision of one operator package. $1 tenant, $2 site,
// $3 package id. Returns found=false (no row) for an unknown or system package.
func packageReferencesSQL() string {
	return `
	WITH pkg AS (
	  SELECT p.id, p.tenant_id, p.site_id
	    FROM iam_v2.internet_packages p
	   WHERE p.tenant_id = $1 AND p.site_id = $2 AND p.id = $3::uuid AND p.is_system = false
	), revs AS (
	  SELECT r.id FROM iam_v2.internet_package_revisions r JOIN pkg ON r.package_id = pkg.id
	   WHERE r.tenant_id = $1 AND r.site_id = $2
	)
	SELECT (SELECT count(*) FROM revs),
	       (SELECT count(*) FROM iam_v2.entitlements e
	         WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.package_revision_id IN (SELECT id FROM revs)
	           AND e.status IN ('PENDING','ACTIVE','SUSPENDED')),
	       (SELECT count(*) FROM iam_v2.entitlements e
	         WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.package_revision_id IN (SELECT id FROM revs)),
	       (SELECT count(*) FROM iam_v2.purchases pu
	         WHERE pu.tenant_id = $1 AND pu.site_id = $2 AND pu.package_revision_id IN (SELECT id FROM revs)),
	       (SELECT count(*) FROM iam_v2.offer_quotes q
	         WHERE q.tenant_id = $1 AND q.site_id = $2 AND q.package_revision_id IN (SELECT id FROM revs)),
	       (SELECT count(*) FROM iam_v2.site_checkout_grace_config g
	         WHERE g.tenant_id = $1 AND g.site_id = $2 AND g.grace_package_revision_id IN (SELECT id FROM revs)),
	       (SELECT count(*) FROM iam_v2.guest_access_accounts ga
	         WHERE ga.tenant_id = $1 AND ga.site_id = $2 AND ga.assigned_package_id = $3::uuid)
	  FROM pkg`
}

// planReferencesSQL counts references to every revision of one operator plan. $1 tenant, $2 site, $3 plan id,
// $4 the reserved system codes (hidden, so they answer not-found).
func planReferencesSQL() string {
	return `
	WITH plan AS (
	  SELECT sp.id FROM iam_v2.service_plans sp
	   WHERE sp.tenant_id = $1 AND sp.site_id = $2 AND sp.id = $3::uuid AND sp.code <> ALL($4::text[])
	), revs AS (
	  SELECT r.id FROM iam_v2.service_plan_revisions r JOIN plan ON r.service_plan_id = plan.id
	   WHERE r.tenant_id = $1 AND r.site_id = $2
	)
	SELECT (SELECT count(*) FROM revs),
	       (SELECT count(*) FROM iam_v2.internet_packages ip
	          JOIN iam_v2.internet_package_revisions ipr ON ipr.id = ip.current_revision_id
	         WHERE ip.tenant_id = $1 AND ip.site_id = $2 AND ip.is_system = false AND ip.active IS TRUE
	           AND ipr.service_plan_revision_id IN (SELECT id FROM revs)),
	       (SELECT count(*) FROM iam_v2.internet_package_revisions ipr
	         WHERE ipr.tenant_id = $1 AND ipr.site_id = $2 AND ipr.service_plan_revision_id IN (SELECT id FROM revs)),
	       (SELECT count(*) FROM iam_v2.entitlements e
	         WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.service_plan_revision_id IN (SELECT id FROM revs)
	           AND e.status IN ('PENDING','ACTIVE','SUSPENDED')),
	       (SELECT count(*) FROM iam_v2.entitlements e
	         WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.service_plan_revision_id IN (SELECT id FROM revs))
	  FROM plan`
}

func countReason(code string, n int64, msg string) DeletabilityReason {
	c := n
	return DeletabilityReason{Code: code, Message: msg, Count: &c}
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// packageReasons turns the seven counts into operator sentences. Pure, so the wording is unit-tested.
func packageReasons(revs, live, ents, purchases, quotes, grace, accounts int64) []DeletabilityReason {
	return []DeletabilityReason{
		countReason("ACTIVE_ENTITLEMENTS", live, fmt.Sprintf("%d %s using this package right now.",
			live, plural(live, "guest is", "guests are"))),
		countReason("ENTITLEMENTS", ents, fmt.Sprintf("%d internet %s given to guests %s this package.",
			ents, plural(ents, "grant", "grants"), plural(ents, "records", "record"))),
		countReason("PURCHASES", purchases, fmt.Sprintf("%d %s on record for this package.",
			purchases, plural(purchases, "purchase is", "purchases are"))),
		countReason("OFFER_QUOTES", quotes, fmt.Sprintf("%d %s shown to guests on the portal %s this package.",
			quotes, plural(quotes, "offer", "offers"), plural(quotes, "names", "name"))),
		countReason("CHECKOUT_GRACE", grace, "The after-check-out grace setting points at this package."),
		countReason("GUEST_ACCOUNTS", accounts, fmt.Sprintf("%d guest %s assigned this package.",
			accounts, plural(accounts, "account is", "accounts are"))),
		countReason("REVISION_HISTORY", revs, fmt.Sprintf("%d saved %s of this package %s kept permanently for the record.",
			revs, plural(revs, "version", "versions"), plural(revs, "is", "are"))),
	}
}

// planReasons turns the five counts into operator sentences.
func planReasons(revs, activePkgs, pkgVersions, live, ents int64) []DeletabilityReason {
	return []DeletabilityReason{
		countReason("ACTIVE_PACKAGES", activePkgs, fmt.Sprintf("%d active %s this plan to guests.",
			activePkgs, plural(activePkgs, "package gives", "packages give"))),
		countReason("ACTIVE_ENTITLEMENTS", live, fmt.Sprintf("%d %s online on this plan right now.",
			live, plural(live, "guest is", "guests are"))),
		countReason("ENTITLEMENTS", ents, fmt.Sprintf("%d internet %s given to guests %s this plan.",
			ents, plural(ents, "grant", "grants"), plural(ents, "records", "record"))),
		countReason("PACKAGE_VERSIONS", pkgVersions, fmt.Sprintf("%d saved package %s %s on this plan.",
			pkgVersions, plural(pkgVersions, "version", "versions"), plural(pkgVersions, "is built", "are built"))),
		countReason("REVISION_HISTORY", revs, fmt.Sprintf("%d saved %s of this plan %s kept permanently for the record.",
			revs, plural(revs, "version", "versions"), plural(revs, "is", "are"))),
	}
}

func (r *PgCommerceAdminRepository) PackageReferences(ctx context.Context, tenantID, siteID, packageID string) ([]DeletabilityReason, bool, error) {
	var revs, live, ents, purchases, quotes, grace, accounts int64
	err := r.db.QueryRow(ctx, packageReferencesSQL(), tenantID, siteID, packageID).
		Scan(&revs, &live, &ents, &purchases, &quotes, &grace, &accounts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return packageReasons(revs, live, ents, purchases, quotes, grace, accounts), true, nil
}

func (r *PgCommerceAdminRepository) PlanReferences(ctx context.Context, tenantID, siteID, planID string) ([]DeletabilityReason, bool, error) {
	var revs, activePkgs, pkgVersions, live, ents int64
	err := r.db.QueryRow(ctx, planReferencesSQL(), tenantID, siteID, planID, reservedCommerceCodes).
		Scan(&revs, &activePkgs, &pkgVersions, &live, &ents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return planReasons(revs, activePkgs, pkgVersions, live, ents), true, nil
}
