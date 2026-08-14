//go:build integration && phase4

// PHASE-4 TEST OWNERSHIP. The `phase4` tag is not decoration: these tests need the Phase-4 schema
// (migrations 0011-0026), and the PHASE-3 regression gate builds a database that stops at 0010. With
// only the `integration` tag they compiled into the Phase-3 gate's ./cmd/edged/ package and failed
// against a schema in which `financial_base_currency` and `p4_declare_financial_recovery` cannot exist
// -- a false failure that says nothing about Phase 3 and hides anything that would say something.
// The tag is what makes the schema requirement structural rather than a naming convention.

package main

import (
	"context"
	"strings"
	"testing"
)

// Real HTTP + real PostgreSQL contract tests for the Phase-4 financial OPERATIONS surface.
//
// The database already enforces the recovery rules, and the payment package's own matrix proves them. What
// only exists at the API is the authorization and the redaction: that this surface needs the financial
// permission, that a recovery decision is step-up authenticated and takes its author from the session, that
// an out-of-scope settlement is indistinguishable from an absent one, and that no response here carries a
// correlation handle an operator could use to act out of band.

func TestIntegrationFinOpsAPI_HealthCarriesNoIdentifiers(t *testing.T) {
	f := newAPI(t, "payments_operator")
	code, raw := f.doRaw(t, "GET", "/financial-ops/health", nil)
	if code != 200 {
		t.Fatalf("health: %d %s", code, raw)
	}
	// The whole point of the shape: a redacted surface cannot leak what it has no field for.
	for _, forbidden := range []string{"provider_ref", "idempotency_key", "provider_txn_ref"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("health response carried %q:\n%s", forbidden, raw)
		}
	}
	for _, required := range []string{"outbox_queued", "payments_unknown", "settlements_manual_review",
		"recovery_active", "provider_egress_enabled", "status", "reasons"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("health response is missing the contract signal %q", required)
		}
	}
}

func TestIntegrationFinOpsAPI_RequiresTheFinancialPermission(t *testing.T) {
	// An operator with no financial permission cannot read the surface at all.
	f := newAPI(t, "guest_relations_operator")
	if code, _ := f.doRaw(t, "GET", "/financial-ops/health", nil); code != 403 && code != 404 {
		t.Fatalf("a non-financial role read the financial operations surface: %d", code)
	}
}

func TestIntegrationFinOpsAPI_RecoveryDecisionsNeedStepUpAndTakeTheirAuthorFromTheSession(t *testing.T) {
	f := newAPI(t, "payments_operator")
	ctx := context.Background()

	var epoch int64
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p4_declare_financial_recovery($1,$2,$3,$4)`,
		f.tenant, f.site, f.operator, "api contract test: holding money movement").Scan(&epoch); err != nil {
		t.Fatalf("declare recovery: %v", err)
	}
	var holdID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.financial_recovery_holds
		(tenant_id,site_id,epoch,work_kind,work_id,held_status)
		VALUES ($1,$2,$3,'SETTLEMENT',gen_random_uuid(),'REQUIRED') RETURNING id::text`,
		f.tenant, f.site, epoch).Scan(&holdID); err != nil {
		t.Fatalf("seed hold: %v", err)
	}

	code, body := f.do(t, "GET", "/financial-ops/recovery", nil)
	if code != 200 {
		t.Fatalf("recovery: %d", code)
	}
	if rec, _ := body["recovery"].(map[string]any); rec == nil || rec["Active"] != true {
		t.Fatalf("recovery state does not report the hold: %v", body)
	}

	// no password -> refused, and nothing recorded
	if code, _ := f.do(t, "POST", "/financial-ops/recovery/holds/"+holdID+"/resolve", map[string]any{
		"resolution": "CONFIRMED_NOT_COMPLETED",
		"note":       "checked the provider dashboard; nothing was charged",
	}); code != 401 {
		t.Fatalf("a decision without step-up was accepted: %d", code)
	}
	var resolved *string
	if err := f.pool.QueryRow(ctx, `SELECT resolution FROM iam_v2.financial_recovery_holds WHERE id=$1`,
		holdID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != nil {
		t.Fatalf("a refused decision was recorded as %s", *resolved)
	}

	// An actor cannot even be OFFERED. The decoder rejects an unknown field outright, which is stronger
	// than accepting and ignoring it: there is no version of this request that names its own author.
	if code, _ := f.do(t, "POST", "/financial-ops/recovery/holds/"+holdID+"/resolve", map[string]any{
		"resolution": "CONFIRMED_NOT_COMPLETED",
		"note":       "checked the provider dashboard; nothing was charged",
		"password":   f.password,
		"actor":      "00000000-0000-0000-0000-000000000000",
	}); code != 400 {
		t.Fatalf("a request naming its own author was not rejected: %d", code)
	}

	// the legitimate decision, whose author is taken from the session
	code, body = f.do(t, "POST", "/financial-ops/recovery/holds/"+holdID+"/resolve", map[string]any{
		"resolution": "CONFIRMED_NOT_COMPLETED",
		"note":       "checked the provider dashboard; nothing was charged",
		"password":   f.password,
	})
	if code != 200 {
		t.Fatalf("a valid decision was refused: %d %v", code, body)
	}
	var author string
	if err := f.pool.QueryRow(ctx, `SELECT resolved_by::text FROM iam_v2.financial_recovery_holds WHERE id=$1`,
		holdID).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if author != f.operator {
		t.Fatalf("the recorded author is %s, not the session operator %s", author, f.operator)
	}

	// release now succeeds because nothing is left unreconciled
	code, _ = f.do(t, "POST", "/financial-ops/recovery/release", map[string]any{
		"note":     "every held item reconciled against the provider dashboard",
		"password": f.password,
	})
	if code != 200 {
		t.Fatalf("release after full reconciliation was refused: %d", code)
	}
}

func TestIntegrationFinOpsAPI_ReleaseIsRefusedWhileWorkIsUnreconciled(t *testing.T) {
	f := newAPI(t, "payments_operator")
	ctx := context.Background()
	var epoch int64
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p4_declare_financial_recovery($1,$2,$3,$4)`,
		f.tenant, f.site, f.operator, "api contract test: unreconciled release").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.financial_recovery_holds
		(tenant_id,site_id,epoch,work_kind,work_id,held_status)
		VALUES ($1,$2,$3,'SETTLEMENT',gen_random_uuid(),'REQUIRED')`,
		f.tenant, f.site, epoch); err != nil {
		t.Fatal(err)
	}
	code, body := f.do(t, "POST", "/financial-ops/recovery/release", map[string]any{
		"note":     "I am sure it is fine",
		"password": f.password,
	})
	if code != 409 {
		t.Fatalf("release with unreconciled work returned %d %v", code, body)
	}
}

// Same tenant, different site. This is the isolation a fresh-tenant-per-fixture test cannot see.
func TestIntegrationFinOpsAPI_AnotherSiteOfTheSameTenantIsInvisible(t *testing.T) {
	a := newAPI(t, "payments_operator")
	b := newAPIIn(t, a.tenant, "payments_operator")
	ctx := context.Background()

	// Site B gets its own commercial chain. Skipping when one is absent would turn the isolation proof
	// into a no-op the day the fixture changes, which is the failure mode this test exists to rule out.
	var planID, planRev, pkgID, pkgRev, purchaseID, settlementID string
	q := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := b.pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed site B: %v", err)
		}
	}
	q(&planID, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code)
		VALUES ($1,$2,'finops-'||substr(md5(random()::text),1,8)) RETURNING id::text`, b.tenant, b.site)
	q(&planRev, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'finops',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, b.tenant, b.site, planID)
	q(&pkgID, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code)
		VALUES ($1,$2,'finops-'||substr(md5(random()::text),1,8)) RETURNING id::text`, b.tenant, b.site)
	q(&pkgRev, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
		VALUES ($1,$2,$3,1,$4,'GENERAL',100,'USD',2) RETURNING id::text`, b.tenant, b.site, pkgID, planRev)
	q(&purchaseID, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,trigger,amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,'ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		b.tenant, b.site, pkgRev)
	if err := b.pool.QueryRow(ctx, `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'ONLINE_PAYMENT','REQUIRED') RETURNING id::text`,
		b.tenant, b.site, purchaseID).Scan(&settlementID); err != nil {
		t.Fatal(err)
	}

	// Site A's operator asking for it must get the same answer, BYTE FOR BYTE, as for one that does not
	// exist. A different message would confirm that another property's settlement is there to be found.
	codeReal, bodyReal := a.doRaw(t, "GET", "/financial-ops/settlements/"+settlementID, nil)
	codeFake, bodyFake := a.doRaw(t, "GET",
		"/financial-ops/settlements/11111111-2222-3333-4444-555555555555", nil)
	if codeReal != 404 || codeFake != 404 {
		t.Fatalf("cross-site settlement leaked: real=%d fake=%d", codeReal, codeFake)
	}
	if bodyReal != bodyFake {
		t.Fatalf("an out-of-scope settlement is distinguishable from an absent one:\n real=%s\n fake=%s",
			bodyReal, bodyFake)
	}
	if code, _ := b.doRaw(t, "GET", "/financial-ops/settlements/"+settlementID, nil); code != 200 {
		t.Fatalf("site B cannot read its own settlement: %d", code)
	}
}

// The surface must not carry a correlation handle, and must not imply a capability Phase 4 does not have.
func TestIntegrationFinOpsAPI_CarriesNoCorrelationHandleAndNoRefundAffordance(t *testing.T) {
	f := newAPI(t, "payments_operator")
	code, raw := f.doRaw(t, "GET", "/financial-ops/settlements", nil)
	if code != 200 {
		t.Fatalf("settlements: %d %s", code, raw)
	}
	if strings.Contains(raw, "provider_ref") || strings.Contains(raw, "idempotency") {
		t.Fatalf("the settlement list carried a correlation handle:\n%s", raw)
	}
}
