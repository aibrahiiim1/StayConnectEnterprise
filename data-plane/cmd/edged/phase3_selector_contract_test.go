//go:build integration

package main

// The selector must offer EXACTLY what publication accepts. Any package listed here that publication would
// then refuse is a trap: the operator picks it, gets an error they cannot act on, and learns to distrust the
// screen. These tests poison one attribute at a time and prove each poisoned candidate is absent from the
// list, present in the diagnostics, and refused by publication.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// seedCandidate creates a CHECKOUT_GRACE package whose duration policy and plan can be poisoned per test.
// durationPolicy is raw JSON so a test can supply a malformed one.
func (f *apiFixture) seedCandidate(t *testing.T, code, durationPolicy string, planCode string, timeAccounting string) string {
	t.Helper()
	ctx := context.Background()
	var rev string
	if err := f.pool.QueryRow(ctx, `WITH
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         VALUES (gen_random_uuid(),$1,$2,$4,true) RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,
	            max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,4000,1500,2,'REJECT_NEW_DEVICE',$5,524288000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,$3,true,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[],$6::jsonb
	          FROM ip, spr RETURNING id, package_id)
	SELECT id::text FROM ipr`, f.tenant, f.site, code, planCode, timeAccounting, durationPolicy).Scan(&rev); err != nil {
		t.Fatalf("seed candidate %s: %v", code, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

const goodDuration = `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":"CHECKOUT_GRACE_V1"}`

// mismatchReason asks the authoritative validator directly.
func (f *apiFixture) mismatchReason(t *testing.T, rev string, duration, down, up int, quota int64, devLimit int, devPolicy string) string {
	t.Helper()
	var reason *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT iam_v2.grace_package_mismatch_reason($1,$2,$3::uuid,$4,$5,$6,$7,$8,$9)`,
		f.tenant, f.site, rev, duration, down, up, quota, devLimit, devPolicy).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason == nil {
		return ""
	}
	return *reason
}

// The approved policy version is REQUIRED and must be an exact scalar string. "No declared version" is not the
// same as "the approved version", and a number or object is a malformed declaration rather than a version.
func TestIntegration_API_PolicyVersionMustBeExactlyApproved(t *testing.T) {
	f := newAPI(t)
	cases := []struct {
		name, policy, wantReason string
	}{
		{"missing", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600}`, "DURATION_POLICY_VERSION"},
		{"blank", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":""}`, "DURATION_POLICY_VERSION"},
		{"wrong", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":"CHECKOUT_GRACE_V2"}`, "DURATION_POLICY_VERSION"},
		{"emergency version", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":"EMERGENCY_GRACE_V1"}`, "DURATION_POLICY_VERSION"},
		{"numeric", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":1}`, "DURATION_POLICY_VERSION"},
		{"array", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":["CHECKOUT_GRACE_V1"]}`, "DURATION_POLICY_VERSION"},
		{"object", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":{"v":"CHECKOUT_GRACE_V1"}}`, "DURATION_POLICY_VERSION"},
		{"null", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":null}`, "DURATION_POLICY_VERSION"},
		{"approved", goodDuration, ""},
	}
	for i, tc := range cases {
		rev := f.seedCandidate(t, fmt.Sprintf("pkg-ver-%d", i), tc.policy, fmt.Sprintf("plan-ver-%d", i), "VALIDITY_WINDOW")
		if got := f.mismatchReason(t, rev, 3600, 4000, 1500, 524288000, 2, "REJECT_NEW_DEVICE"); got != tc.wantReason {
			t.Fatalf("%s: mismatch reason %q, want %q", tc.name, got, tc.wantReason)
		}
		// the selector agrees with the validator, and so does publication
		_, body := f.do(t, "GET", "/checkout-grace/packages", nil)
		listed := false
		for _, r := range body["data"].([]any) {
			if r.(map[string]any)["package_revision_id"] == rev {
				listed = true
			}
		}
		if listed != (tc.wantReason == "") {
			t.Fatalf("%s: listed=%v but mismatch=%q — the selector disagrees with the validator", tc.name, listed, tc.wantReason)
		}
		status, resp := f.do(t, "PUT", "/checkout-grace",
			policyFor(rev, 0, 3600, 4000, 1500, 2, 524288000, "REJECT_NEW_DEVICE"))
		if tc.wantReason == "" {
			if status != 200 {
				t.Fatalf("%s: the approved version was refused: %d %v", tc.name, status, resp)
			}
		} else if status != 400 || resp["error"] != "package_invalid" {
			t.Fatalf("%s: publication got %d %v, want 400/package_invalid", tc.name, status, resp)
		}
	}
}

// The whole reserved Emergency catalog is off limits — including through the side door of an ordinary-looking
// package that pins the reserved Emergency SERVICE PLAN.
func TestIntegration_API_ReservedEmergencyPlanCannotBeRepurposed(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.bootstrap_emergency_grace($1,$2)`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	// an ordinary-looking package pinned to the canonical Emergency plan revision
	var rev string
	if err := f.pool.QueryRow(ctx, `WITH
	  spr AS (SELECT spr.id FROM iam_v2.service_plans sp
	          JOIN iam_v2.service_plan_revisions spr ON spr.service_plan_id = sp.id
	          WHERE sp.tenant_id=$1 AND sp.site_id=$2 AND sp.code='__sys_emergency_grace_plan__' LIMIT 1),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,'ordinary-looking-pkg',true,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[],
	                 $3::jsonb FROM ip, spr RETURNING id, package_id)
	SELECT id::text FROM ipr`, f.tenant, f.site, goodDuration).Scan(&rev); err != nil {
		t.Fatalf("seed repurposed package: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, rev); err != nil {
		t.Fatal(err)
	}
	if got := f.mismatchReason(t, rev, 3600, 5000, 2000, 268435456, 1, "REJECT_NEW_DEVICE"); got != "PLAN_IS_EMERGENCY_CATALOG" {
		t.Fatalf("mismatch reason %q, want PLAN_IS_EMERGENCY_CATALOG", got)
	}
	_, body := f.do(t, "GET", "/checkout-grace/packages", nil)
	for _, r := range body["data"].([]any) {
		if r.(map[string]any)["package_revision_id"] == rev {
			t.Fatal("a package pinning the reserved Emergency plan was offered for selection")
		}
	}
}

// Every other way a candidate can be unpublishable must also keep it OUT of the list — and a malformed
// duration policy must exclude that one candidate rather than breaking the endpoint.
func TestIntegration_API_SelectorExcludesEveryUnpublishableCandidate(t *testing.T) {
	f := newAPI(t)
	good := f.seedCandidate(t, "pkg-good", goodDuration, "plan-good", "VALIDITY_WINDOW")

	poisoned := []struct{ code, duration, plan, accounting string }{
		{"pkg-endmode", `{"end_mode":"VALIDITY_WINDOW","grace_duration_seconds":3600,"policy_version":"CHECKOUT_GRACE_V1"}`, "plan-endmode", "VALIDITY_WINDOW"},
		{"pkg-no-endmode", `{"grace_duration_seconds":3600,"policy_version":"CHECKOUT_GRACE_V1"}`, "plan-no-endmode", "VALIDITY_WINDOW"},
		{"pkg-badjson-dur", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":"soon","policy_version":"CHECKOUT_GRACE_V1"}`, "plan-badjson", "VALIDITY_WINDOW"},
		{"pkg-huge-dur", `{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":99999999999,"policy_version":"CHECKOUT_GRACE_V1"}`, "plan-huge", "VALIDITY_WINDOW"},
		{"pkg-accounting", goodDuration, "plan-accounting", "AGGREGATE_ONLINE_TIME"},
	}
	for _, p := range poisoned {
		f.seedCandidate(t, p.code, p.duration, p.plan, p.accounting)
	}

	status, body := f.do(t, "GET", "/checkout-grace/packages", nil)
	if status != 200 {
		t.Fatalf("a poisoned candidate broke the whole selector: %d %v", status, body)
	}
	codes := map[string]bool{}
	for _, r := range body["data"].([]any) {
		codes[r.(map[string]any)["package_code"].(string)] = true
	}
	if !codes["pkg-good"] {
		t.Fatal("the publishable package was not offered")
	}
	for _, p := range poisoned {
		if codes[p.code] {
			t.Fatalf("%s was offered but publication would refuse it", p.code)
		}
	}
	// and the one that IS offered publishes cleanly with its own values
	if status, resp := f.do(t, "PUT", "/checkout-grace",
		policyFor(good, 0, 3600, 4000, 1500, 2, 524288000, "REJECT_NEW_DEVICE")); status != 200 {
		t.Fatalf("the offered package was refused by publication: %d %v", status, resp)
	}
}

// The selector carries the whole immutable description an operator needs to choose — and nothing a guest
// should never see.
func TestIntegration_API_SelectorCarriesCompleteImmutableMetadata(t *testing.T) {
	f := newAPI(t)
	f.seedCandidate(t, "pkg-meta", goodDuration, "plan-meta", "VALIDITY_WINDOW")
	_, body := f.do(t, "GET", "/checkout-grace/packages", nil)
	rows := body["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one candidate, got %d", len(rows))
	}
	o := rows[0].(map[string]any)
	for _, k := range []string{
		"package_revision_id", "package_code", "revision_no",
		"service_plan_revision_id", "service_plan_code", "service_plan_revision_no",
		"down_kbps", "up_kbps", "data_quota_bytes", "device_limit", "device_limit_policy",
		"time_accounting_mode", "grace_duration_seconds", "end_mode", "policy_version",
		"settlement_mode", "is_current", "is_active",
	} {
		if _, ok := o[k]; !ok {
			t.Fatalf("the selector omits %s, which an operator needs to understand the choice", k)
		}
	}
	if o["policy_version"] != "CHECKOUT_GRACE_V1" || o["end_mode"] != "GRACE_AFTER_CHECKOUT" {
		t.Fatalf("declared policy not surfaced: %v", o)
	}
	_ = time.Now()
}
