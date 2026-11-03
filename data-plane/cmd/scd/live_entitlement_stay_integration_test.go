package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A returning room guest whose stay already holds live access must join it, not be offered packages to buy again.
func TestLiveEntitlementFindsTheStaysAccessForARoomSignIn(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("fixture needs a superuser DSN: %v", err)
	}

	var tenant, site, stay, otherStay, roomCtx, voucherCtx, otherRoomCtx, ent string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text, gen_random_uuid()::text,
		gen_random_uuid()::text`).Scan(&tenant, &site, &stay, &otherStay); err != nil {
		t.Fatal(err)
	}
	roomContext := func(stayID string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts (id, tenant_id, site_id, method, stay_id, pms_interface_id,
			authentication_interface_revision_id, device_id, guest_network_id, expires_at)
			VALUES (gen_random_uuid(), $1, $2, 'PMS', $3, gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
			gen_random_uuid(), now() + interval '5 minutes') RETURNING id::text`, tenant, site, stayID).Scan(&id); err != nil {
			t.Fatalf("seed room auth context: %v", err)
		}
		return id
	}
	roomCtx = roomContext(stay)
	otherRoomCtx = roomContext(otherStay)
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts (id, tenant_id, site_id, method, voucher_id, device_id,
		guest_network_id, expires_at) VALUES (gen_random_uuid(), $1, $2, 'VOUCHER', gen_random_uuid(), gen_random_uuid(),
		gen_random_uuid(), now() + interval '5 minutes') RETURNING id::text`, tenant, site).Scan(&voucherCtx); err != nil {
		t.Fatalf("seed voucher auth context: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements (tenant_id, site_id, stay_id, purchase_id, policy_snapshot,
		service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status, activated_at)
		VALUES ($1, $2, $3, gen_random_uuid(), '{}'::jsonb, gen_random_uuid(), gen_random_uuid(), 'VALIDITY_WINDOW',
		'MANUAL_END', 'ACTIVE', now()) RETURNING id::text`, tenant, site, stay).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}

	got, err := liveEntitlementID(ctx, tx, roomCtx, tenant, site)
	if err != nil || got != ent {
		t.Fatalf("room sign-in on the stay: got %q, %v; want the stay's live entitlement %q", got, err, ent)
	}
	if got, err := liveEntitlementID(ctx, tx, otherRoomCtx, tenant, site); !errors.Is(err, pgx.ErrNoRows) || got != "" {
		t.Fatalf("room sign-in on another stay found %q (%v); a stay's access is only its own", got, err)
	}
	if got, err := liveEntitlementID(ctx, tx, voucherCtx, tenant, site); !errors.Is(err, pgx.ErrNoRows) || got != "" {
		t.Fatalf("a voucher context matched the stay's access %q (%v); only a room sign-in is the stay's subject", got, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET status = 'TERMINATED', terminal_reason = 'ADMIN', terminated_at = now() WHERE id = $1`, ent); err != nil {
		t.Fatal(err)
	}
	if got, err := liveEntitlementID(ctx, tx, roomCtx, tenant, site); !errors.Is(err, pgx.ErrNoRows) || got != "" {
		t.Fatalf("ended access was still offered for joining: %q (%v)", got, err)
	}
}
