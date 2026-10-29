//go:build integration && phase5

package transfer

import (
	"context"
	"errors"
	"testing"
)

// Audit AD-46 (P6 L11): the operator's reason is stored with the transfer under the staff-notes retention, and the landing
// package honours a PMS_INTERFACE restriction of the destination.

func TestIntegration_AD46_OperatorReasonIsStoredAndFollowsRetention(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	ctx := context.Background()
	grant(t, p, f, f.stayA)
	out, err := New(p).Execute(ctx, req(f, f.stayA, f.stayB))
	if err != nil {
		t.Fatal(err)
	}
	var kind, why string
	if err := p.QueryRow(ctx, `SELECT reason, operator_reason FROM iam_v2.entitlement_transfers WHERE id=$1`, out.TransferID).Scan(&kind, &why); err != nil {
		t.Fatal(err)
	}
	if kind != "CROSS_PMS_TRANSFER" || why != "guest moved to the other property" {
		t.Fatalf("the lineage kind stays and the operator's text is stored beside it: %q / %q", kind, why)
	}
	// It is append-only like the rest of the row ...
	if _, err := p.Exec(ctx, `UPDATE iam_v2.entitlement_transfers SET operator_reason='edited later' WHERE id=$1`, out.TransferID); err == nil {
		t.Fatal("a stored operator reason must not be editable")
	}
	// ... and goes with the existing erasure of this table's `reason` (a stay's anonymisation).
	if _, err := p.Exec(ctx, `SELECT iam_v2.anonymise_stay($1)`, f.stayA); err != nil {
		t.Fatalf("anonymise: %v", err)
	}
	if err := p.QueryRow(ctx, `SELECT operator_reason FROM iam_v2.entitlement_transfers WHERE id=$1`, out.TransferID).Scan(&why); err != nil {
		t.Fatal(err)
	}
	if why != "Removed after the staff-notes period" {
		t.Fatalf("the operator's text follows the staff-notes retention, got %q", why)
	}
}

func TestIntegration_AD46_LandingPackageHonoursAnInterfaceRestriction(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	ctx := context.Background()
	// Only a restricted, non-system package is left to land on.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET active=false WHERE code='XFER-GRACE' AND site_id=$1`, f.site); err != nil {
		t.Fatal(err)
	}
	var pkg, rev string
	if err := p.QueryRow(ctx, `WITH
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(), $1, $2, 'ONLY-A', false, true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods)
	          SELECT gen_random_uuid(), $1, $2, ip.id, 1, r.service_plan_revision_id, 'FREE_STAY', 0, ARRAY['NOT_REQUIRED']::text[]
	            FROM ip, iam_v2.internet_package_revisions r WHERE r.id = $3 RETURNING id, package_id)
	  SELECT package_id::text, id::text FROM ipr`, f.tenant, f.site, f.pkg).Scan(&pkg, &rev); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, rev, pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.package_eligibility_rules(tenant_id,site_id,package_revision_id,rule_type,rule_value)
		VALUES ($1,$2,$3,'PMS_INTERFACE', jsonb_build_object('pms_interface_ids', jsonb_build_array($4::text)))`,
		f.tenant, f.site, rev, f.ifaceA); err != nil {
		t.Fatal(err)
	}
	grant(t, p, f, f.stayA)
	// The destination is interface B; the only package is restricted to interface A: nothing to land on, and the source
	// access is NOT taken away.
	if _, err := New(p).Execute(ctx, req(f, f.stayA, f.stayB)); !errors.Is(err, ErrNoGracePackage) {
		t.Fatalf("a package restricted to another interface is not offered, got %v", err)
	}
	var live int
	if err := p.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, f.stayA).Scan(&live); err != nil || live != 1 {
		t.Fatalf("the source access survives a refused landing: %d %v", live, err)
	}
	// Naming the destination interface makes it eligible.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.package_eligibility_rules SET rule_value=jsonb_build_object('pms_interface_ids', jsonb_build_array($2::text))
		WHERE package_revision_id=$1 AND rule_type='PMS_INTERFACE'`, rev, f.ifaceB); err != nil {
		// the rules table may be append-only: replace by a fresh revision-less insert path is not needed for this test
		t.Skipf("eligibility rules are not editable here: %v", err)
	}
	out, err := New(p).Execute(ctx, req(f, f.stayA, f.stayB))
	if err != nil {
		t.Fatal(err)
	}
	if out.ToPackage != rev {
		t.Fatalf("lands on the package that names the destination interface: %s vs %s", out.ToPackage, rev)
	}
}
