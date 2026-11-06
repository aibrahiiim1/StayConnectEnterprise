//go:build integration

package payment

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// A room move continues a paid package under its own zero-price purchase (0213). A full refund of the original charge must still end
// the access it funded, which now lives in the continuation.
func TestCardRefundPolicy_AFullRefundEndsTheAccessARoomMoveContinued(t *testing.T) {
	ctx := context.Background()
	s, e, prov, charge, ent := capturedChain(t)
	admin := pool(t)

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, f := range []string{"commerce_intent"} {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'TERMINATED',now(),'CONVERTED')`, ent); err != nil {
		t.Fatalf("convert the original: %v", err)
	}
	var cont string
	if err := tx.QueryRow(ctx, `WITH o AS (SELECT * FROM iam_v2.entitlements WHERE id=$1::uuid),
		pu AS (INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, trigger, amount_minor, state)
		       SELECT tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, 'ROOM_MOVE_RESTORE', 0, 'GRANTED' FROM o RETURNING id)
		INSERT INTO iam_v2.entitlements (tenant_id, site_id, stay_id, guest_account_id, voucher_id, guest_principal_id, anonymous_subject_id,
		       pms_interface_id, purchase_id, policy_snapshot, snapshot_version, service_plan_revision_id, package_revision_id,
		       time_accounting_mode, end_mode, window_ends_at, status, data_quota_bytes)
		SELECT o.tenant_id, o.site_id, o.stay_id, o.guest_account_id, o.voucher_id, o.guest_principal_id, o.anonymous_subject_id,
		       o.pms_interface_id, pu.id, o.policy_snapshot || jsonb_build_object('restores', o.id::text), o.snapshot_version,
		       o.service_plan_revision_id, o.package_revision_id, o.time_accounting_mode, o.end_mode, o.window_ends_at, 'PENDING', o.data_quota_bytes
		  FROM o, pu RETURNING id::text`, ent).Scan(&cont); err != nil {
		t.Fatalf("continue the package: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'ACTIVE',now(),'ROOM_MOVE_RESTORED')`, cont); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	prov.status.Events = []ProviderEvent{{EventID: "refund:" + charge + ":" + strconv.FormatInt(s.amount, 10),
		Kind: "REFUND", AmountMinor: s.amount, ProviderRef: "pi_x", Cumulative: true}}
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)

	if st, why := entStatus(t, cont); st != "TERMINATED" || why != "REVOKED" {
		t.Fatalf("the continued access survived a full refund: %s/%s", st, why)
	}
	if n := count1(t, admin, `SELECT count(*) FROM iam_v2.refund_access_terminations WHERE entitlement_id=$1`, cont); n != 1 {
		t.Fatalf("refund evidence rows for the continuation: %d, want 1", n)
	}
}
