//go:build integration

package payment

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// A package refunded before its room move completes is never continued: the restoration reads the funding through the chain.
func TestCardRefundPolicy_ARefundedPackageReadsAsReversedThroughItsContinuations(t *testing.T) {
	ctx := context.Background()
	s, e, prov, charge, ent := capturedChain(t)
	admin := pool(t)
	reversed := func(id string) bool {
		t.Helper()
		var b bool
		if err := admin.QueryRow(ctx, `SELECT iam_v2.entitlement_funding_reversed($1::uuid)`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if reversed(ent) {
		t.Fatal("a paid, unrefunded package reads as reversed")
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'TERMINATED',now(),'CONVERTED')`, ent); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'ACTIVE',now(),'ROOM_MOVE_RESTORED')`, cont); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if reversed(cont) {
		t.Fatal("a continuation of an unrefunded package reads as reversed")
	}
	prov.status.Events = []ProviderEvent{{EventID: "refund:" + charge + ":" + strconv.FormatInt(s.amount, 10),
		Kind: "REFUND", AmountMinor: s.amount, ProviderRef: "pi_x", Cumulative: true}}
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	if !reversed(ent) || !reversed(cont) {
		t.Fatal("after a full refund the package and its continuation must both read as reversed")
	}
}

// A dispute or chargeback resolved with END_ACCESS ends the continuation a room move made of the package it paid for.
func TestCardRefundPolicy_AnEndAccessReviewEndsTheAccessARoomMoveContinued(t *testing.T) {
	ctx := context.Background()
	s, e, prov, charge, ent := capturedChain(t)
	admin := pool(t)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'TERMINATED',now(),'CONVERTED')`, ent); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'ACTIVE',now(),'ROOM_MOVE_RESTORED')`, cont); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	prov.status.Events = []ProviderEvent{{EventID: "dispute:" + charge, Kind: "DISPUTE", AmountMinor: s.amount, ProviderRef: "dp_" + charge}}
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	var review, out string
	if err := admin.QueryRow(ctx, `SELECT id::text FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$1 AND kind='DISPUTE'`, charge).Scan(&review); err != nil {
		t.Fatalf("dispute case: %v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,$3,'END_ACCESS','payments:alice','dispute lost')`,
		s.tenant, s.site, review).Scan(&out); err != nil || out != "END_ACCESS" {
		t.Fatalf("resolve: %q %v", out, err)
	}
	if st, why := entStatus(t, cont); st != "TERMINATED" || why != "REVOKED" {
		t.Fatalf("the continued access survived END_ACCESS: %s/%s", st, why)
	}
}
// A dispute decided END_ACCESS while the package sat converted at a checkout keeps a later room move from bringing it back.
func TestCardRefundPolicy_AnEndAccessDecisionBeforeTheMoveIsHonoured(t *testing.T) {
	ctx := context.Background()
	s, e, prov, charge, ent := capturedChain(t)
	admin := pool(t)
	if err := guardedConvert(ctx, admin, ent); err != nil {
		t.Fatal(err)
	}
	prov.status.Events = []ProviderEvent{{EventID: "dispute:" + charge, Kind: "DISPUTE", AmountMinor: s.amount, ProviderRef: "dp_" + charge}}
	e.SweepReversals(ctx, s.tenant, s.site, 24*time.Hour, 10)
	var reversed bool
	if err := admin.QueryRow(ctx, `SELECT iam_v2.entitlement_funding_reversed($1::uuid)`, ent).Scan(&reversed); err != nil || reversed {
		t.Fatalf("an open dispute alone reads as reversed: %v %v", reversed, err)
	}
	if _, err := admin.Exec(ctx, `SELECT iam_v2.p4_resolve_reversal_review($1,$2,
		(SELECT id FROM iam_v2.card_reversal_reviews WHERE charge_transaction_id=$3 AND kind='DISPUTE'),'END_ACCESS','payments:alice','dispute lost')`,
		s.tenant, s.site, charge); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT iam_v2.entitlement_funding_reversed($1::uuid)`, ent).Scan(&reversed); err != nil || !reversed {
		t.Fatalf("an END_ACCESS decision does not stop a continuation: %v %v", reversed, err)
	}
}

func guardedConvert(ctx context.Context, p *pgxpool.Pool, ent string) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'TERMINATED',now(),'CONVERTED')`, ent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}