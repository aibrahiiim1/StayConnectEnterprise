//go:build integration

package main

// Real HTTP + PostgreSQL contract tests for the Checkout-Grace PUBLICATION rules: a policy that would later be
// judged invalid by the Checkout conversion must be refused while the operator is looking at it, not accepted
// and silently degraded to Emergency Grace on the next departure.

import (
	"context"
	"testing"
)

// seedGracePackage provisions the canonical grace catalog and returns the current CHECKOUT_GRACE package
// revision together with the plan scalars it pins.
// seedGracePackage creates an ORDINARY (non-emergency) system-owned Checkout-Grace package for this site and
// returns the revision plus the plan scalars it pins. The reserved Emergency catalog is deliberately NOT used:
// it is the fallback of last resort, not a policy an operator may adopt.
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

// A policy with NO package revision cannot be published: it would report success while guaranteeing that every
// subsequent checkout falls back to Emergency Grace and raises an alert.
func TestIntegration_API_GracePublicationRequiresAPackage(t *testing.T) {
	f := newAPI(t)
	status, body := f.do(t, "PUT", "/checkout-grace", gracePolicy(0, nil))
	if status != 400 || body["error"] != "package_required" {
		t.Fatalf("null package got %d %v, want 400/package_required", status, body)
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site); n != 0 {
		t.Fatal("a refused publication created a config row")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.checkout_grace_policy_publications WHERE tenant_id=$1`,
		f.tenant); n != 0 {
		t.Fatal("a refused publication left an audit row")
	}
	// the database refuses it directly too: the HTTP layer is not the only guard
	var out int
	if err := f.pool.QueryRow(context.Background(), `SELECT iam_v2.publish_checkout_grace_policy(
		$1,$2,NULL,3600,4000,1500,524288000,2,'REJECT_NEW_DEVICE',86400,0,$3::uuid,'DIRECT')`,
		f.tenant, f.site, f.operator).Scan(&out); err == nil {
		t.Fatal("the controlled operation accepted a NULL package")
	}
}

// The selector offers only revisions publication would accept, described by their own immutable attributes.
func TestIntegration_API_GracePackageSelector(t *testing.T) {
	f := newAPI(t)
	rev, down, up, devLimit, duration, quota, devPolicy := f.seedGracePackage(t)
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
	if status, body := f.do(t, "PUT", "/checkout-grace",
		policyFor(rev, 0, duration, down, up, devLimit, quota, devPolicy)); status != 200 || body["config_version"].(float64) != 1 {
		t.Fatalf("publishing the offered package failed: %d %v", status, body)
	}
}

// Poison tests: every published scalar must match the pinned revision EXACTLY.
func TestIntegration_API_GracePublicationRejectsEachMismatch(t *testing.T) {
	f := newAPI(t)
	rev, down, up, devLimit, duration, quota, devPolicy := f.seedGracePackage(t)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"duration", func(p map[string]any) { p["grace_duration_seconds"] = duration + 1 }},
		{"down", func(p map[string]any) { p["grace_down_kbps"] = down + 1 }},
		{"up", func(p map[string]any) { p["grace_up_kbps"] = up + 1 }},
		{"quota", func(p map[string]any) { p["grace_data_quota_bytes"] = quota + 1 }},
		{"device-limit", func(p map[string]any) { p["grace_device_limit"] = devLimit + 1 }},
	} {
		p := policyFor(rev, 0, duration, down, up, devLimit, quota, devPolicy)
		tc.mutate(p)
		status, body := f.do(t, "PUT", "/checkout-grace", p)
		if status != 400 || body["error"] != "package_invalid" {
			t.Fatalf("%s mismatch got %d %v, want 400/package_invalid", tc.name, status, body)
		}
	}
	if n := count(t, f.pool, `SELECT count(*) FROM iam_v2.checkout_grace_policy_publications WHERE tenant_id=$1`, f.tenant); n != 0 {
		t.Fatal("a refused mismatch left a publication audit row")
	}
}

// The controlled operations are the FINAL authority: a NULL precondition never means "skip the check".
func TestIntegration_API_ControlledOperationsRefuseNullPreconditions(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	rev, down, up, devLimit, duration, quota, devPolicy := f.seedGracePackage(t)

	if status, _ := f.do(t, "PUT", "/checkout-grace", policyFor(rev, 0, duration, down, up, devLimit, quota, devPolicy)); status != 200 {
		t.Fatal("initial publication failed")
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

// The reserved Emergency catalog is never selectable and never publishable as the ordinary policy: adopting it
// would make "configured" and "emergency" indistinguishable and silence the alert that says the real policy is
// broken.
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
	status, resp := f.do(t, "PUT", "/checkout-grace",
		policyFor(emergencyRev, 0, 3600, 5000, 2000, 1, 524288000, "REJECT_NEW_DEVICE"))
	if status != 400 || resp["error"] != "package_invalid" {
		t.Fatalf("publishing the emergency catalog got %d %v, want 400/package_invalid", status, resp)
	}
}
