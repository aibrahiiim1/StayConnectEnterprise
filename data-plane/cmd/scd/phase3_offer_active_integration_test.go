//go:build integration

package main

// A PACKAGE TURNED OFF IN HOTEL ADMIN MUST NOT BE OFFERED TO GUESTS.
//
// It was. The offer predicate filtered on current revision, system flag, package type, visibility window,
// price and settlement, and never once looked at `internet_packages.active` — the operator's own switch. A
// package deactivated in Hotel Admin carried on being offered.
//
// This was found in production configuration rather than in a test: a deactivated 2 Gbps "Free Internet
// Package" was still guest-selectable beside its 2 Mbps replacement. Two consequences, and the second is
// worse than the first. The guest saw a choice screen where the operator intended automatic access; and one
// of the two choices was the plan the operator believed they had retired.
//
// The admin UI showing "inactive" while the portal offers the thing is the failure mode these tests exist to
// prevent. visible_until is not the fix — it dates a package, it does not disable one, and an operator who
// has to date-fence a package to turn it off has no way to say "off, indefinitely".

import (
	"context"
	"testing"
)

// setPackageActive drives the operator's switch through the column Hotel Admin writes.
func setPackageActive(t *testing.T, f *authFixture, pkgRevID string, active bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE iam_v2.internet_packages ip SET active=$2
		  FROM iam_v2.internet_package_revisions ipr
		 WHERE ipr.id=$1::uuid AND ip.id=ipr.package_id`, pkgRevID, active); err != nil {
		t.Fatalf("set package active=%v: %v", active, err)
	}
}

// THE REGRESSION. Deactivate the only offer and the guest is offered nothing — not the deactivated package.
func TestIntegration_Phase3Offers_InactivePackageIsNotOffered(t *testing.T) {
	f := newAuthFixture(t)

	// Baseline: while active, this package IS offered. Asserting it first means the refusal below is
	// attributable to the flag rather than to some unrelated part of the fixture.
	_, before := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000051-0000-4000-8000-000000000000"))
	if before.Outcome != outcomeVerified || len(before.Offers) != 1 {
		t.Fatalf("setup: expected exactly one offer while active, got %+v", before)
	}

	setPackageActive(t, f, f.pkgRev, false)

	_, after := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000052-0000-4000-8000-000000000000"))
	for _, o := range after.Offers {
		if o.PackageRevisionID == f.pkgRev {
			t.Fatalf("a package deactivated in Hotel Admin was still offered to a guest: %+v. The admin UI "+
				"says it is off; the portal must agree", after.Offers)
		}
	}
}

// REACTIVATION RESTORES THE OFFER. `active` is a switch, not a one-way retirement, and an operator who turns
// a package off during a problem must be able to turn it back on without republishing a revision.
func TestIntegration_Phase3Offers_ReactivatedPackageIsOfferedAgain(t *testing.T) {
	f := newAuthFixture(t)

	setPackageActive(t, f, f.pkgRev, false)
	setPackageActive(t, f, f.pkgRev, true)

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000053-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || len(res.Offers) != 1 || res.Offers[0].PackageRevisionID != f.pkgRev {
		t.Fatalf("a reactivated package was not offered again: %+v", res)
	}
}

// THE EXACT PRODUCTION SHAPE: one active package and one inactive one, both otherwise offerable. Before the
// fix this produced two offers and therefore a choice screen. It must now produce exactly one, automatically
// granted, and it must be the active one.
//
// The priced package in the fixture is deliberately left alone — it is already excluded on price, and having
// it present proves the `active` term did not accidentally become the only thing doing the filtering.
func TestIntegration_Phase3Offers_OnlyTheActivePackageSurvives(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	// Clone the fixture's free package into a second, deactivated package — the retired predecessor.
	//
	// Three statements rather than one CTE: a data-modifying CTE cannot see rows its sibling CTEs insert,
	// because every arm reads the same snapshot taken before the statement began. The single-statement
	// version updated nothing and returned no rows.
	var retiredPkg string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.internet_packages (id, tenant_id, site_id, code, active, is_system)
		SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.code || '-retired', false, false
		  FROM iam_v2.internet_packages ip
		  JOIN iam_v2.internet_package_revisions ipr ON ipr.package_id = ip.id
		 WHERE ipr.id = $1::uuid
		RETURNING id::text`, f.pkgRev).Scan(&retiredPkg); err != nil {
		t.Fatalf("seed the retired package: %v", err)
	}
	var retiredRev string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.internet_package_revisions
		  (id, tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type,
		   price_minor, currency, currency_exponent, settlement_methods, duration_policy, renewable)
		SELECT gen_random_uuid(), ipr.tenant_id, ipr.site_id, $2::uuid, 1, ipr.service_plan_revision_id,
		       ipr.package_type, 0, ipr.currency, ipr.currency_exponent, ipr.settlement_methods,
		       ipr.duration_policy, ipr.renewable
		  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id = $1::uuid
		RETURNING id::text`, f.pkgRev, retiredPkg).Scan(&retiredRev); err != nil {
		t.Fatalf("seed the retired revision: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.internet_packages SET current_revision_id=$2::uuid WHERE id=$1::uuid`,
		retiredPkg, retiredRev); err != nil {
		t.Fatalf("point the retired package at its revision: %v", err)
	}

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000054-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("resolve failed: %+v", res)
	}
	if len(res.Offers) != 1 {
		t.Fatalf("got %d offers, want exactly 1. More than one offer turns automatic access into a choice "+
			"screen, and the extra option here is a package the operator retired: %+v", len(res.Offers),
			res.Offers)
	}
	if res.Offers[0].PackageRevisionID != retiredRev {
		return // the surviving offer is the active one, which is the point
	}
	t.Fatalf("the surviving offer is the RETIRED package %s", retiredRev)
}
