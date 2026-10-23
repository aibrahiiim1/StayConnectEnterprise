//go:build integration

package main

// A package deactivated in Hotel Admin (`internet_packages.active`) must not be offered; visible_until only
// dates a package, it cannot disable one indefinitely.

import (
	"context"
	"testing"
)

func setPackageActive(t *testing.T, f *authFixture, pkgRevID string, active bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE iam_v2.internet_packages ip SET active=$2
		  FROM iam_v2.internet_package_revisions ipr
		 WHERE ipr.id=$1::uuid AND ip.id=ipr.package_id`, pkgRevID, active); err != nil {
		t.Fatalf("set package active=%v: %v", active, err)
	}
}

func TestIntegration_Phase3Offers_InactivePackageIsNotOffered(t *testing.T) {
	f := newAuthFixture(t)

	// Baseline: offered while active, so the refusal below is attributable to the flag.
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

// One active and one inactive package, both otherwise offerable, must yield exactly one offer: the active one.
// The fixture's priced package stays, to show `active` is not the only filter.
func TestIntegration_Phase3Offers_OnlyTheActivePackageSurvives(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	// Three statements rather than one CTE: a data-modifying CTE cannot see rows its siblings insert.
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
		return
	}
	t.Fatalf("the surviving offer is the RETIRED package %s", retiredRev)
}
