//go:build integration

package main

// A policy the Checkout conversion would later judge invalid must be refused at publication, not accepted and
// silently degraded to Emergency Grace on the next departure.

import (
	"context"
	"testing"
)

// seedGracePackage creates an ordinary system-owned Checkout-Grace package and returns its revision and pinned
// plan scalars. The reserved Emergency catalog is not used: it is a fallback, not a policy an operator may adopt.
func (f *apiFixture) seedGracePackage(t *testing.T) (rev string, down, up, devLimit, duration int, quota int64, policy string) {
	t.Helper()
	ctx := context.Background()
	down, up, devLimit, duration, quota, policy = 4000, 1500, 2, 3600, int64(524288000), "REJECT_NEW_DEVICE"
	if err := f.pool.QueryRow(ctx, `WITH
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         VALUES (gen_random_uuid(),$1,$2,'site-grace-plan',true) RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,
	            max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,$3,$4,$5,$8,'VALIDITY_WINDOW',$6 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,'site-grace-pkg',true,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[],
	                 jsonb_build_object('end_mode','GRACE_AFTER_CHECKOUT','grace_duration_seconds',$7::int,
	                                    'policy_version','CHECKOUT_GRACE_V1')
	          FROM ip, spr RETURNING id, package_id)
	SELECT id::text FROM ipr`,
		f.tenant, f.site, down, up, devLimit, quota, duration, policy).Scan(&rev); err != nil {
		t.Fatalf("seed grace package: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, rev); err != nil {
		t.Fatalf("pin current revision: %v", err)
	}
	return
}

// policyOnly is what an operator publishes: typed policy, no package. The system derives the plan and package
// revisions in the publication's own transaction.
func policyOnly(expected, duration, down, up, devLimit int, quota int64, devPolicy string) map[string]any {
	p := policyFor("", expected, duration, down, up, devLimit, quota, devPolicy)
	delete(p, "grace_package_revision_id")
	return p
}

func policyFor(rev string, expected, duration, down, up, devLimit int, quota int64, devPolicy string) map[string]any {
	return map[string]any{
		"grace_package_revision_id":  rev,
		"grace_duration_seconds":     duration,
		"grace_down_kbps":            down,
		"grace_up_kbps":              up,
		"grace_data_quota_bytes":     quota,
		"grace_device_limit":         devLimit,
		"grace_device_limit_policy":  devPolicy,
		"eligibility_window_seconds": 86400,
		"config_version":             0,
		"expected_config_version":    expected,
		"password":                   "operator-step-up-pw",
		"reason_code":                "INITIAL_POLICY",
	}
}

// Publishing a policy derives its own package: the checkout validator demands exact equality between policy
// and pinned revision, and the operator publisher cannot create reserved system packages.
func TestIntegration_API_GracePublicationDerivesItsOwnPackage(t *testing.T) {
	f := newAPI(t)
	status, body := f.do(t, "PUT", "/checkout-grace", policyOnly(0, 3600, 4000, 1500, 2, 524288000, "REJECT_NEW_DEVICE"))
	if status != 200 {
		t.Fatalf("publishing a policy with no package got %d %v, want 200", status, body)
	}
	if body["config_version"].(float64) != 1 {
		t.Fatalf("first publication produced version %v, want 1", body["config_version"])
	}

	// The derived package must satisfy the checkout validator, asked with the conversion's own function; otherwise
	// the next departure falls back to Emergency Grace.
	var rev string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT grace_package_revision_id::text FROM iam_v2.site_checkout_grace_config
		  WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&rev); err != nil {
		t.Fatalf("the publication stored no package revision: %v", err)
	}
	if got := f.mismatchReason(t, rev, 3600, 4000, 1500, 524288000, 2, "REJECT_NEW_DEVICE"); got != "" {
		t.Fatalf("the DERIVED package is refused by the checkout validator: %s", got)
	}

	// Offering a package is a refusal, not a silent ignore: an ignored field makes a caller believe it chose something.
	status, body = f.do(t, "PUT", "/checkout-grace",
		policyFor(rev, 1, 3600, 4000, 1500, 2, 524288000, "REJECT_NEW_DEVICE"))
	if status != 400 || body["error"] != "package_not_accepted" {
		t.Fatalf("supplying a package got %d %v, want 400/package_not_accepted", status, body)
	}

	// The controlled operation still refuses a NULL package outright.
	var out int
	if err := f.pool.QueryRow(context.Background(), `SELECT iam_v2.publish_checkout_grace_policy(
		$1,$2,NULL,3600,4000,1500,524288000,2,'REJECT_NEW_DEVICE',86400,1,$3::uuid,'DIRECT')`,
		f.tenant, f.site, f.operator).Scan(&out); err == nil {
		t.Fatal("the controlled operation accepted a NULL package")
	}
}

// A refused publication leaves nothing behind: derivation is in the publication's transaction, so no config row,
// audit row, plan or package revision is stranded.
func TestIntegration_API_RefusedGracePolicyLeavesNoDerivedCatalog(t *testing.T) {
	f := newAPI(t)
	p := policyOnly(0, 3600, 4000, 1500, 2, 524288000, "REJECT_NEW_DEVICE")
	p["grace_duration_seconds"] = 999999999 // past the contract's 7-day ceiling
	if status, body := f.do(t, "PUT", "/checkout-grace", p); status != 400 {
		t.Fatalf("an out-of-range duration got %d %v, want 400", status, body)
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site); n != 0 {
		t.Fatal("a refused publication created a config row")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.checkout_grace_policy_publications WHERE tenant_id=$1`,
		f.tenant); n != 0 {
		t.Fatal("a refused publication left an audit row")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.service_plans WHERE tenant_id=$1 AND code='__system_checkout_grace_plan'`,
		f.tenant); n != 0 {
		t.Fatal("a refused publication left a derived service plan behind")
	}
}

// The selector offers only revisions publication would accept, described by their own immutable attributes.
func TestIntegration_API_GracePackageSelector(t *testing.T) {
	f := newAPI(t)
	rev, down, _, _, duration, _, _ := f.seedGracePackage(t)
	status, body := f.do(t, "GET", "/checkout-grace/packages", nil)
	if status != 200 {
		t.Fatalf("packages: %d %v", status, body)
	}
	rows := body["data"].([]any)
	if len(rows) == 0 {
		t.Fatal("no selectable grace packages were offered")
	}
	found := false
	for _, r := range rows {
		o := r.(map[string]any)
		if o["package_revision_id"] == rev {
			found = true
			if o["down_kbps"].(float64) != float64(down) || o["grace_duration_seconds"].(float64) != float64(duration) {
				t.Fatalf("the selector shows different numbers than the pinned revision: %v", o)
			}
			if o["settlement_mode"] != "NOT_REQUIRED" || o["is_current"] != true {
				t.Fatalf("a non-current or settlement-bearing package was offered: %v", o)
			}
		}
	}
	if !found {
		t.Fatal("the site's grace package revision was not offered")
	}
}

// Derivation is exact for every policy: if it ever rounded, truncated or defaulted a field, the conversion
// would fall back to Emergency Grace while the screen showed a published policy.
func TestIntegration_API_EveryPublishedPolicyDerivesAnExactPackage(t *testing.T) {
	f := newAPI(t)
	expected := 0
	for _, tc := range []struct {
		name                         string
		duration, down, up, devLimit int
		quota                        int64
	}{
		{"the fixture's own shape", 3600, 4000, 1500, 2, 524288000},
		{"a shorter grace", 900, 10000, 4000, 1, 1073741824},
		{"an awkward duration", 4500, 1, 1, 1000, 1},
		{"the contract ceilings", 604800, 10000000, 10000000, 1000, 1099511627776},
	} {
		status, body := f.do(t, "PUT", "/checkout-grace",
			policyOnly(expected, tc.duration, tc.down, tc.up, tc.devLimit, tc.quota, "REJECT_NEW_DEVICE"))
		if status != 200 {
			t.Fatalf("%s: publication got %d %v", tc.name, status, body)
		}
		expected = int(body["config_version"].(float64))

		var rev string
		var gotDur, gotDown, gotUp, gotDev int
		var gotQuota int64
		if err := f.pool.QueryRow(context.Background(), `
			SELECT grace_package_revision_id::text, grace_duration_seconds, grace_down_kbps, grace_up_kbps,
			       grace_data_quota_bytes, grace_device_limit
			  FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
			f.tenant, f.site).Scan(&rev, &gotDur, &gotDown, &gotUp, &gotQuota, &gotDev); err != nil {
			t.Fatalf("%s: read back: %v", tc.name, err)
		}
		// No silent normalisation on the way through.
		if gotDur != tc.duration || gotDown != tc.down || gotUp != tc.up || gotQuota != tc.quota || gotDev != tc.devLimit {
			t.Fatalf("%s: stored %d/%d/%d/%d/%d, want %d/%d/%d/%d/%d",
				tc.name, gotDur, gotDown, gotUp, gotQuota, gotDev,
				tc.duration, tc.down, tc.up, tc.quota, tc.devLimit)
		}
		if reason := f.mismatchReason(t, rev, tc.duration, tc.down, tc.up, tc.quota, tc.devLimit, "REJECT_NEW_DEVICE"); reason != "" {
			t.Fatalf("%s: the derived package is refused by the checkout validator: %s", tc.name, reason)
		}
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.checkout_grace_policy_publications WHERE tenant_id=$1`, f.tenant); n != 4 {
		t.Fatalf("four publications produced %d ledger rows", n)
	}
}

// The controlled operations are the FINAL authority: a NULL precondition never means "skip the check".
func TestIntegration_API_ControlledOperationsRefuseNullPreconditions(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	rev, down, up, devLimit, duration, quota, devPolicy := f.seedGracePackage(t)

	// rev is needed so the direct calls are refused on their NULL precondition, not on the package they name.
	if status, body := f.do(t, "PUT", "/checkout-grace",
		policyOnly(0, duration, down, up, devLimit, quota, devPolicy)); status != 200 {
		t.Fatalf("initial publication failed: %d %v", status, body)
	}
	var v int
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.publish_checkout_grace_policy(
		$1,$2,$3::uuid,$4,$5,$6,$7,$8,$9,86400,NULL,$10::uuid,'DIRECT')`,
		f.tenant, f.site, rev, duration, down, up, quota, devLimit, devPolicy, f.operator).Scan(&v); err == nil {
		t.Fatal("a NULL expected_config_version bypassed concurrency control")
	}
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.publish_checkout_grace_policy(
		$1,$2,$3::uuid,$4,$5,$6,$7,$8,$9,86400,1,$10::uuid,NULL)`,
		f.tenant, f.site, rev, duration, down, up, quota, devLimit, devPolicy, f.operator).Scan(&v); err == nil {
		t.Fatal("a NULL reason code was accepted")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.checkout_grace_policy_publications WHERE tenant_id=$1`, f.tenant); n != 1 {
		t.Fatal("a refused direct call changed the publication history")
	}
	if got := count(t, f.pool, `SELECT config_version FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site); got != 1 {
		t.Fatalf("config_version = %d after refused direct calls, want 1", got)
	}

	audit := f.seedAlert(t)
	var seq int64
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.record_alert_action($1,$2,$3::uuid,'ACKNOWLEDGED',$4::uuid,'X',NULL)`,
		f.tenant, f.site, audit, f.operator).Scan(&seq); err == nil {
		t.Fatal("a NULL expected_state acted against any current state")
	}
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.record_alert_action($1,$2,$3::uuid,'ACKNOWLEDGED',$4::uuid,NULL,'OPEN')`,
		f.tenant, f.site, audit, f.operator).Scan(&seq); err == nil {
		t.Fatal("a NULL reason code was accepted")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.checkout_grace_alert_actions WHERE audit_id=$1`, audit); n != 1 {
		t.Fatalf("the alert lifecycle changed under refused calls (%d rows, want the single OPEN)", n)
	}
}

// The reserved Emergency catalog is never publishable as the ordinary policy: it would make "configured" and
// "emergency" indistinguishable and silence the alert that the real policy is broken.
func TestIntegration_API_EmergencyCatalogIsNotAnOrdinaryPolicy(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.bootstrap_emergency_grace($1,$2)`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	var emergencyRev string
	if err := f.pool.QueryRow(ctx, `SELECT ip.current_revision_id::text FROM iam_v2.internet_packages ip
		WHERE ip.tenant_id=$1 AND ip.site_id=$2 AND ip.code='__sys_emergency_grace_pkg__'`,
		f.tenant, f.site).Scan(&emergencyRev); err != nil {
		t.Fatal(err)
	}
	_, body := f.do(t, "GET", "/checkout-grace/packages", nil)
	for _, r := range body["data"].([]any) {
		if r.(map[string]any)["package_revision_id"] == emergencyRev {
			t.Fatal("the reserved Emergency catalog was offered as an ordinary policy choice")
		}
	}
	// The matcher refuses it whatever the scalars say.
	if reason := f.mismatchReason(t, emergencyRev, 3600, 5000, 2000, 524288000, 1, "REJECT_NEW_DEVICE"); reason != "PACKAGE_IS_EMERGENCY_CATALOG" {
		t.Fatalf("the emergency package's mismatch reason is %q, want PACKAGE_IS_EMERGENCY_CATALOG", reason)
	}
	// No package is accepted from a caller, so naming it is refused up front.
	if status, resp := f.do(t, "PUT", "/checkout-grace",
		policyFor(emergencyRev, 0, 3600, 5000, 2000, 1, 524288000, "REJECT_NEW_DEVICE")); status != 400 {
		t.Fatalf("naming a package got %d %v, want 400", status, resp)
	}
	// Publishing the emergency catalog's own terms must still land on the system package, not the reserved one.
	if status, resp := f.do(t, "PUT", "/checkout-grace",
		policyOnly(0, 3600, 5000, 2000, 1, 524288000, "REJECT_NEW_DEVICE")); status != 200 {
		t.Fatalf("publishing emergency-shaped terms was refused: %d %v", status, resp)
	}
	var adopted string
	if err := f.pool.QueryRow(ctx, `SELECT grace_package_revision_id::text
		  FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site).Scan(&adopted); err != nil {
		t.Fatal(err)
	}
	if adopted == emergencyRev {
		t.Fatal("the site policy adopted the reserved Emergency catalog revision")
	}
}
