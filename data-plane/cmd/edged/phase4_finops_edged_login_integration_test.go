//go:build integration && phase4

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

// RECOVERY UNDER EDGED'S OWN LOGIN. The API contract tests above run against a superuser pool, which is how
// Charge health and Recovery answered "query failed" on a real appliance for months without a test noticing:
// no test ever read or acted as svc_edged. This one does, with the Gate-P grants a factory-clean appliance
// gets. EDGED_ROLE_TEST_DSN is a svc_edged login on the same database as PHASE3_TEST_DSN.
func TestIntegrationFinOpsAPI_RecoveryWorksUnderEdgedsOwnLogin(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	f := newAPI(t, "payments_operator")
	ctx := context.Background()
	ep, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	eng, err := payment.NewProductionEngine(ep, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The read side of Charge health and Recovery.
	if _, err := eng.Health(ctx, f.tenant, f.site); err != nil {
		t.Fatalf("health as svc_edged: %v", err)
	}
	if _, err := eng.RecoveryStatusFor(ctx, f.tenant, f.site); err != nil {
		t.Fatalf("recovery state as svc_edged: %v", err)
	}

	// A held item, declared by the superuser fixture as a restore would.
	var epoch int64
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p4_declare_financial_recovery($1,$2,$3,$4)`,
		f.tenant, f.site, f.operator, "privilege proof: holding money movement").Scan(&epoch); err != nil {
		t.Fatalf("declare recovery: %v", err)
	}
	var holdID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.financial_recovery_holds
		(tenant_id,site_id,epoch,work_kind,work_id,held_status)
		VALUES ($1,$2,$3,'SETTLEMENT',gen_random_uuid(),'REQUIRED') RETURNING id::text`,
		f.tenant, f.site, epoch).Scan(&holdID); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	holds, err := eng.OpenHolds(ctx, f.tenant, f.site, 10)
	if err != nil || len(holds) != 1 {
		t.Fatalf("open holds as svc_edged: %v (%d)", err, len(holds))
	}
	// The actions: resolve, then release -- as svc_edged, with the author recorded.
	if err := eng.ResolveHold(ctx, holdID, "CONFIRMED_NOT_COMPLETED", f.operator, "privilege proof: nothing was charged"); err != nil {
		t.Fatalf("resolve as svc_edged: %v", err)
	}
	var author string
	if err := f.pool.QueryRow(ctx, `SELECT resolved_by::text FROM iam_v2.financial_recovery_holds WHERE id=$1`, holdID).Scan(&author); err != nil || author != f.operator {
		t.Fatalf("resolution author %q (%v), want %s", author, err, f.operator)
	}
	if _, err := eng.ReleaseRecovery(ctx, f.tenant, f.site, f.operator, "privilege proof: every hold reconciled"); err != nil {
		t.Fatalf("release as svc_edged: %v", err)
	}

	// Zero-attempt retry: an unknown posting must be refused by the FUNCTION, not by a missing grant.
	var out string
	err = ep.QueryRow(ctx, `SELECT iam_v2.p4_authorize_zero_attempt_retry(gen_random_uuid(), $1::uuid, 'privilege proof reason', '{}'::jsonb)::text`,
		f.operator).Scan(&out)
	if err == nil || strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "REVIEW_POSTING_UNKNOWN") {
		t.Fatalf("zero-attempt retry as svc_edged: %v", err)
	}

	// The financial mirror maximum age (D48): read it, and change it through the audited definer.
	var age int
	if err := ep.QueryRow(ctx, `SELECT iam_v2.p4_financial_mirror_max_age_seconds($1::uuid,$2::uuid,gen_random_uuid())`,
		f.tenant, f.site).Scan(&age); err != nil || age != 14400 {
		t.Fatalf("mirror age as svc_edged: %d %v", age, err)
	}
	var iface string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind,display_label)
		VALUES ($1,$2,'protel-fias','Protel') RETURNING id::text`, f.tenant, f.site).Scan(&iface); err != nil {
		t.Fatal(err)
	}
	if err := ep.QueryRow(ctx, `SELECT iam_v2.p4_set_financial_mirror_max_age($1::uuid,$2::uuid,$3::uuid,7200,'privilege proof',$4::uuid)`,
		f.tenant, f.site, iface, f.operator).Scan(&age); err != nil {
		t.Fatalf("set mirror age as svc_edged: %v", err)
	}

	// Reads other screens make: Checkout grace history and the client-network active count.
	for _, q := range []string{
		`SELECT count(*) FROM iam_v2.pms_interface_financial_settings WHERE tenant_id=$1`,
		`SELECT count(*) FROM iam_v2.pms_interface_financial_setting_changes WHERE tenant_id=$1`,
		`SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE tenant_id=$1`,
		`SELECT count(*) FROM iam_v2.device_network_appearances WHERE tenant_id=$1`,
	} {
		var n int
		if err := ep.QueryRow(ctx, q, f.tenant).Scan(&n); err != nil {
			t.Fatalf("%s as svc_edged: %v", q, err)
		}
	}
}
