//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AD-34: a device nothing refers to is removed; a device a row refers to through a COMPOSITE foreign key
// ((tenant_id, site_id, device_id), the usual shape) is kept and does not stop the others being cleaned.
// Needs PHASE3_TEST_DSN.
func TestIntegration_PruneIdleDevicesKeepsDevicesReferencedByCompositeKeys(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var have bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'prune_idle_devices')`).Scan(&have); err != nil || !have {
		t.Skip("prune_idle_devices is not in this database")
	}
	const tenant, site = "44444444-4444-4444-4444-444444444444", "55555555-5555-5555-5555-555555555555"

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS iam_v2.zz_prune_ref`)
		_, _ = pool.Exec(ctx, `DELETE FROM iam_v2.devices WHERE tenant_id=$1 AND site_id=$2`, tenant, site)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx, `CREATE TABLE iam_v2.zz_prune_ref (
		tenant_id uuid NOT NULL, site_id uuid NOT NULL, device_id uuid NOT NULL,
		FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices (tenant_id, site_id, id))`); err != nil {
		t.Fatalf("scratch referencing table: %v", err)
	}

	// The function runs as the schema owner, as it does for every real table that refers to a device.
	_, _ = pool.Exec(ctx, `GRANT SELECT ON iam_v2.zz_prune_ref TO iam_v2_owner`)

	var kept, gone string
	for i, dst := range []*string{&kept, &gone} {
		mac := []string{"02:00:00:00:0a:01", "02:00:00:00:0a:02"}[i]
		if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen)
			VALUES ($1,$2,gen_random_uuid(),$3::macaddr, now() - interval '4000 days', now() - interval '4000 days') RETURNING id::text`,
			tenant, site, mac).Scan(dst); err != nil {
			t.Fatalf("seed device: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO iam_v2.zz_prune_ref VALUES ($1,$2,$3)`, tenant, site, kept); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT iam_v2.prune_idle_devices($1::uuid,$2::uuid,100)`, tenant, site).Scan(&n); err != nil {
		t.Fatalf("prune must not fail on a referenced device: %v", err)
	}
	if n != 1 {
		t.Fatalf("removed %d devices, want exactly the unreferenced one", n)
	}
	var keptStill, goneStill bool
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.devices WHERE id=$1::uuid)`, kept).Scan(&keptStill)
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.devices WHERE id=$1::uuid)`, gone).Scan(&goneStill)
	if !keptStill || goneStill {
		t.Fatalf("referenced device kept = %v (want true), unreferenced device still present = %v (want false)", keptStill, goneStill)
	}
}
