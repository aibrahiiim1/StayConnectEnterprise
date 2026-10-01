//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// WHAT A CLIENT-NETWORK REPLACEMENT MAY AND MAY NOT DO TO AN INTERNET PACKAGE.
//
// The replacement carries the packages that name the retired network forward onto its successor. That is a
// republish of an IMMUTABLE revision, which means every field the forwarded spec fails to carry is a silent,
// uncorrectable edit to the hotel's pricing, offer window, renewal terms or quota — made by an operator who
// asked for a cabling change. These tests hold the two properties that follow from that:
//
//   1. the forwarded revision differs from its predecessor in the network id and in NOTHING else;
//   2. a package that CANNOT be carried forward stops the change becoming permanent, rather than leaving the
//      wire confirmed and the catalogue behind it.

// seedNetworkPackage publishes a package with deliberately NON-DEFAULT values in every field a republish could
// drop, limited to one client network. It writes the revision directly, because the authoring API has no way to
// set plan_overrides, renewable or max_purchases_per_stay — which is exactly how those three came to be reset on
// every republish without anybody noticing.
func (f *apiFixture) seedNetworkPackage(t *testing.T, code, networkID string, extra map[string]string) (pkgID, revID string) {
	t.Helper()
	ctx := context.Background()
	set := map[string]string{
		"package_type":           "'ONE_DAY'",
		"price_minor":            "2500",
		"currency":               "'USD'",
		"currency_exponent":      "2",
		"settlement_methods":     "ARRAY['PREPAID','ONLINE_PAYMENT']::text[]",
		"duration_policy":        `'{"end_mode":"VALIDITY_WINDOW","duration_seconds":86400}'::jsonb`,
		"display":                `'{"name":"Lobby day pass","description":"one day"}'::jsonb`,
		"visible_from":           "'2026-01-01T00:00:00Z'::timestamptz",
		"visible_until":          "'2027-01-01T00:00:00Z'::timestamptz",
		"data_allocation_policy": `'{"mode":"PER_STAY_NIGHT","gb_per_night":2}'::jsonb`,
		"plan_overrides":         `'{"down_kbps":9000}'::jsonb`,
		"renewable":              "true",
		"max_purchases_per_stay": "3",
	}
	for k, v := range extra {
		set[k] = v
	}
	cols, vals := []string{}, []string{}
	for k, v := range set {
		cols = append(cols, k)
		vals = append(vals, v)
	}
	q := fmt.Sprintf(`WITH
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         VALUES (gen_random_uuid(),$1,$2,$3||'-plan',true) RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
	            down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,8000,4000,3,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',1073741824 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,$3,false,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,%s)
	          SELECT $1,$2,ip.id,1,spr.id,%s FROM ip, spr RETURNING id, package_id)
	SELECT id::text, package_id::text FROM ipr`, strings.Join(cols, ","), strings.Join(vals, ","))
	if err := f.pool.QueryRow(ctx, q, f.tenant, f.site, code).Scan(&revID, &pkgID); err != nil {
		t.Fatalf("seed package %s: %v", code, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, revID, pkgID); err != nil {
		t.Fatal(err)
	}
	// One grant tier (publication refuses a package without any) and the network limit itself.
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.package_grant_tiers (tenant_id,site_id,package_revision_id,tier_order,grant_value)
		VALUES ($1,$2,$3,1,'{"down_kbps":5000}'::jsonb)`, f.tenant, f.site, revID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value)
		VALUES ($1,$2,$3,'SITE_NETWORK',jsonb_build_object('guest_network_ids', jsonb_build_array($4::text)))`,
		f.tenant, f.site, revID, networkID); err != nil {
		t.Fatal(err)
	}
	return pkgID, revID
}

type revisionFacts struct {
	RevisionNo                               int
	PackageType, Currency, Duration, Display string
	Alloc, Overrides, Methods                string
	PriceMinor                               int64
	Exponent                                 *int
	VisibleFrom, VisibleUntil                *string
	Renewable                                bool
	MaxPurchases                             *int
	NetworkIDs                               []string
	Tiers                                    int
}

func (f *apiFixture) revisionFacts(t *testing.T, pkgID string) revisionFacts {
	t.Helper()
	ctx := context.Background()
	var v revisionFacts
	var revID string
	if err := f.pool.QueryRow(ctx, `
		SELECT cur.id::text, cur.revision_no, cur.package_type, cur.price_minor, COALESCE(cur.currency,''),
		       cur.currency_exponent::int, cur.settlement_methods::text, cur.duration_policy::text,
		       cur.display::text, COALESCE(cur.data_allocation_policy::text,''), COALESCE(cur.plan_overrides::text,''),
		       to_char(cur.visible_from AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(cur.visible_until AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       cur.renewable, cur.max_purchases_per_stay
		  FROM iam_v2.internet_packages p
		  JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
		 WHERE p.id=$1`, pkgID).Scan(&revID, &v.RevisionNo, &v.PackageType, &v.PriceMinor, &v.Currency,
		&v.Exponent, &v.Methods, &v.Duration, &v.Display, &v.Alloc, &v.Overrides,
		&v.VisibleFrom, &v.VisibleUntil, &v.Renewable, &v.MaxPurchases); err != nil {
		t.Fatalf("read current revision of %s: %v", pkgID, err)
	}
	rows, err := f.pool.Query(ctx, `SELECT jsonb_array_elements_text(rule_value->'guest_network_ids')
		  FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1 AND rule_type='SITE_NETWORK'`, revID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			v.NetworkIDs = append(v.NetworkIDs, id)
		}
	}
	rows.Close()
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.package_grant_tiers WHERE package_revision_id=$1`, revID).Scan(&v.Tiers); err != nil {
		t.Fatal(err)
	}
	return v
}

// materialise runs the apply's DATABASE half and then stands up the netd revision the apply would have
// created, in the state the apply left it in. A settle only ever looks at a request whose apply was KEPT, so a
// fixture that skipped the revision would be testing a state the product cannot be in.
func (f *apiFixture) materialise(t *testing.T, replID, revisionState string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.materialisePendingReplacements(ctx, tx, "operator@test"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("materialise: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// At most one revision may be mid-flight at a time (ncr_single_inflight). Earlier tests in this package
	// leave theirs behind, so the fixture retires them rather than racing the index.
	if _, err := f.pool.Exec(ctx, `UPDATE network_config_revisions SET state='superseded'
		 WHERE state IN ('applying','pending_confirmation')`); err != nil {
		t.Fatal(err)
	}
	var revID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO network_config_revisions (state, summary)
		VALUES ($1,'test apply') RETURNING id::text`, revisionState).Scan(&revID); err != nil {
		t.Fatalf("seed netd revision: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid WHERE id=$1`, replID, revID); err != nil {
		t.Fatal(err)
	}
	return revID
}

// materialiseAndSettle applies and then settles, as a confirm does.
func (f *apiFixture) materialiseAndSettle(t *testing.T, replID string) []map[string]any {
	t.Helper()
	rev := f.materialise(t, replID, "pending_confirmation")
	return f.app.settleConfirmedReplacements(context.Background(), nil, rev)
}

func TestIntegration_NetworkReplace_ForwardedRevisionChangesTheNetworkAndNothingElse(t *testing.T) {
	f := newAPI(t)
	f.app.commerce = f.commerceAdmin(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.150.0.0/24", "10.150.0.1")
	pkgID, _ := f.seedNetworkPackage(t, "day-pass-"+parent, netID, nil)

	before := f.revisionFacts(t, pkgID)
	if before.NetworkIDs[0] != netID || !before.Renewable || before.MaxPurchases == nil || *before.MaxPurchases != 3 {
		t.Fatalf("fixture did not take: %+v", before)
	}

	st, body := f.do(t, "POST", "/network/guest-networks/"+netID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 60, "reason": "port moved to a trunk"})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	// Staging alone must not touch the catalogue at all.
	if mid := f.revisionFacts(t, pkgID); mid.RevisionNo != before.RevisionNo {
		t.Fatalf("staging published a revision: %d -> %d", before.RevisionNo, mid.RevisionNo)
	}

	settled := f.materialiseAndSettle(t, body["replacement_id"].(string))
	if len(settled) != 1 || settled[0]["state"] != "CONFIRMED" {
		t.Fatalf("settle = %v", settled)
	}
	var successor string
	if err := f.pool.QueryRow(context.Background(), `SELECT successor_network_id::text
		  FROM iam_v2.guest_network_replacements WHERE id=$1`, body["replacement_id"]).Scan(&successor); err != nil {
		t.Fatal(err)
	}

	after := f.revisionFacts(t, pkgID)
	if after.RevisionNo != before.RevisionNo+1 {
		t.Fatalf("expected exactly one forward revision, got %d -> %d", before.RevisionNo, after.RevisionNo)
	}
	if len(after.NetworkIDs) != 1 || !strings.EqualFold(after.NetworkIDs[0], successor) {
		t.Fatalf("forwarded rule names %v, want the successor %s", after.NetworkIDs, successor)
	}
	// EVERY OTHER FIELD, compared one by one. A loop over a struct would pass while silently skipping a field
	// nobody remembered to add, which is the failure mode this test exists for.
	for _, c := range []struct {
		field string
		a, b  any
	}{
		{"package_type", after.PackageType, before.PackageType},
		{"price_minor", after.PriceMinor, before.PriceMinor},
		{"currency", after.Currency, before.Currency},
		{"settlement_methods", after.Methods, before.Methods},
		{"duration_policy", after.Duration, before.Duration},
		{"display", after.Display, before.Display},
		{"data_allocation_policy", after.Alloc, before.Alloc},
		{"plan_overrides", after.Overrides, before.Overrides},
		{"renewable", after.Renewable, before.Renewable},
		{"grant_tiers", after.Tiers, before.Tiers},
	} {
		if fmt.Sprint(c.a) != fmt.Sprint(c.b) {
			t.Errorf("%s changed across the forward republish: %v -> %v", c.field, c.b, c.a)
		}
	}
	if after.Exponent == nil || before.Exponent == nil || *after.Exponent != *before.Exponent {
		t.Errorf("currency_exponent changed: %v -> %v", before.Exponent, after.Exponent)
	}
	if after.MaxPurchases == nil || *after.MaxPurchases != 3 {
		t.Errorf("max_purchases_per_stay was reset: %v", after.MaxPurchases)
	}
	if after.VisibleFrom == nil || before.VisibleFrom == nil || *after.VisibleFrom != *before.VisibleFrom ||
		after.VisibleUntil == nil || before.VisibleUntil == nil || *after.VisibleUntil != *before.VisibleUntil {
		t.Errorf("the sale window was dropped: %v..%v -> %v..%v",
			before.VisibleFrom, before.VisibleUntil, after.VisibleFrom, after.VisibleUntil)
	}
}

// A PACKAGE THAT CANNOT BE CARRIED FORWARD STOPS THE CHANGE SETTLING. The forced failure is a duration policy
// that was valid when it was published and is not valid to publish now — a FIXED_AT end that has since passed.
// Publication refuses it, by design, and that refusal must not be swallowed.
func TestIntegration_NetworkReplace_WillNotSettleWhileAPackageCannotBeCarriedForward(t *testing.T) {
	f := newAPI(t)
	f.app.commerce = f.commerceAdmin(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.151.0.0/24", "10.151.0.1")
	pkgID, _ := f.seedNetworkPackage(t, "expired-"+parent, netID, map[string]string{
		"duration_policy": `'{"end_mode":"FIXED_AT","ends_at":"2020-01-01T00:00:00Z"}'::jsonb`,
	})
	before := f.revisionFacts(t, pkgID)

	st, body := f.do(t, "POST", "/network/guest-networks/"+netID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 61, "reason": "port moved to a trunk"})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	replID := body["replacement_id"].(string)

	// The pre-flight the confirm route runs refuses it, naming the package — and writes nothing.
	rev := f.materialise(t, replID, "pending_confirmation")
	blocking := f.app.replacementsBlockingConfirm(context.Background())
	if len(blocking) != 1 {
		t.Fatalf("pre-flight found %d blockers, want 1: %v", len(blocking), blocking)
	}
	if got := describeBlockingPackages(blocking); !strings.Contains(got, "expired-"+parent) {
		t.Fatalf("the refusal does not name the package: %q", got)
	}
	if mid := f.revisionFacts(t, pkgID); mid.RevisionNo != before.RevisionNo {
		t.Fatal("the dry run published a revision; it must write nothing")
	}

	// ...and settling anyway leaves the request APPLIED rather than CONFIRMED.
	settled := f.app.settleConfirmedReplacements(context.Background(), nil, rev)
	if len(settled) != 1 || settled[0]["state"] != "APPLIED" {
		t.Fatalf("settle = %v, want the request left APPLIED", settled)
	}
	if state, _ := f.replacementState(t, replID); state != "APPLIED" {
		t.Fatalf("replacement state = %s, want APPLIED: a change whose packages were not carried forward must not read as finished", state)
	}

	// THE GUESTS ARE NOT STRANDED MEANWHILE. The retired network keeps its logical identity, so the pinned rule
	// still resolves to a device on the successor.
	var successor string
	if err := f.pool.QueryRow(context.Background(), `SELECT successor_network_id::text
		  FROM iam_v2.guest_network_replacements WHERE id=$1`, replID).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	var ancestors int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.guest_network_lineage
		 WHERE tenant_id=$1 AND site_id=$2 AND network_id=$3 AND ancestor_id=$4`,
		f.tenant, f.site, successor, netID).Scan(&ancestors); err != nil {
		t.Fatal(err)
	}
	if ancestors != 1 {
		t.Fatalf("lineage from the successor back to the retired network = %d rows, want 1", ancestors)
	}

	// Correcting the package lets the withheld settle finish on its own, with no second confirm. The
	// correction is a NEW revision, because a revision is immutable — which is also why a replacement cannot
	// repair one of these itself and has to refuse instead.
	if _, err := f.pool.Exec(context.Background(), `WITH cur AS (
		  SELECT * FROM iam_v2.internet_package_revisions
		   WHERE id = (SELECT current_revision_id FROM iam_v2.internet_packages WHERE id=$1)),
		fixed AS (
		  INSERT INTO iam_v2.internet_package_revisions
		    (tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,
		     currency,currency_exponent,settlement_methods,duration_policy,visible_from,visible_until,display,
		     data_allocation_policy,plan_overrides,renewable,max_purchases_per_stay)
		  SELECT tenant_id,site_id,package_id,revision_no+1,service_plan_revision_id,package_type,price_minor,
		     currency,currency_exponent,settlement_methods,
		     '{"end_mode":"VALIDITY_WINDOW","duration_seconds":86400}'::jsonb,
		     visible_from,visible_until,display,data_allocation_policy,plan_overrides,renewable,max_purchases_per_stay
		    FROM cur RETURNING id, package_id),
		r AS (INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value)
		      SELECT e.tenant_id,e.site_id,fixed.id,e.rule_type,e.rule_value FROM iam_v2.package_eligibility_rules e, cur, fixed
		       WHERE e.package_revision_id = cur.id RETURNING 1),
		g AS (INSERT INTO iam_v2.package_grant_tiers (tenant_id,site_id,package_revision_id,tier_order,grant_value)
		      SELECT x.tenant_id,x.site_id,fixed.id,x.tier_order,x.grant_value FROM iam_v2.package_grant_tiers x, cur, fixed
		       WHERE x.package_revision_id = cur.id RETURNING 1)
		UPDATE iam_v2.internet_packages p SET current_revision_id = fixed.id
		  FROM fixed WHERE p.id = fixed.package_id
		    AND (SELECT count(*) FROM r) >= 0 AND (SELECT count(*) FROM g) >= 0`, pkgID); err != nil {
		t.Fatal(err)
	}
	if settled = f.app.settleConfirmedReplacements(context.Background(), nil, rev); len(settled) != 1 || settled[0]["state"] != "CONFIRMED" {
		t.Fatalf("settle after correction = %v, want CONFIRMED", settled)
	}
	if after := f.revisionFacts(t, pkgID); !strings.EqualFold(after.NetworkIDs[0], successor) {
		t.Fatalf("after the correction the rule still names %v, want %s", after.NetworkIDs, successor)
	}
}

// A ROLLBACK LEAVES NO LINEAGE AND NO FORWARDED REVISION: the original network is back, and the catalogue is
// exactly as it was.
func TestIntegration_NetworkReplace_RollbackLeavesEligibilityExactlyAsItWas(t *testing.T) {
	f := newAPI(t)
	f.app.commerce = f.commerceAdmin(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.152.0.0/24", "10.152.0.1")
	pkgID, _ := f.seedNetworkPackage(t, "rb-"+parent, netID, nil)
	before := f.revisionFacts(t, pkgID)

	st, body := f.do(t, "POST", "/network/guest-networks/"+netID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 62, "reason": "port moved to a trunk"})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	replID := body["replacement_id"].(string)
	ctx := context.Background()
	f.materialise(t, replID, "pending_confirmation")
	var successor string
	if err := f.pool.QueryRow(ctx, `SELECT successor_network_id::text FROM iam_v2.guest_network_replacements WHERE id=$1`, replID).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	var live int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_lineage WHERE network_id=$1`, successor).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("while APPLIED the successor must continue the original; lineage rows = %d", live)
	}

	if err := f.app.revertMaterialised(ctx, []string{replID}, "the apply was rolled back"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if enabled, netType, vlan, _ := f.networkState(t, netID); !enabled || netType != "untagged" || vlan != nil {
		t.Fatalf("after rollback the original is enabled=%v type=%s vlan=%v", enabled, netType, vlan)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_lineage WHERE network_id=$1`, successor).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("a rolled-back replacement left %d lineage rows; a change that did not happen continues nothing", live)
	}
	after := f.revisionFacts(t, pkgID)
	if after.RevisionNo != before.RevisionNo || !strings.EqualFold(after.NetworkIDs[0], netID) {
		t.Fatalf("rollback changed the catalogue: rev %d -> %d, network %v", before.RevisionNo, after.RevisionNo, after.NetworkIDs)
	}
}

// commerceAdmin builds the real authoring engine against the fixture's pool, with the master flag on.
func (f *apiFixture) commerceAdmin(t *testing.T) *iamv2.CommerceAdmin {
	t.Helper()
	repo := iamv2.NewPgCommerceAdminRepository(f.pool)
	f.app.commerceRepo = repo
	admin, err := iamv2.NewCommerceAdmin(
		iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true, AdminEnabled: true}, repo, iamv2.NopObserver{})
	if err != nil {
		t.Fatalf("commerce admin: %v", err)
	}
	return admin
}
