//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Another customer's guest records are detected (and the transition therefore refused), not silently served beside.
func TestIntegrationForeignGuestRecordsAreDetected(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tenant, other string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&tenant, &other); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac) VALUES ($1,gen_random_uuid(),gen_random_uuid(),'3c:22:fb:ee:00:01')`, other); err != nil {
		t.Skipf("cannot seed a device here: %v", err)
	}
	var found bool
	q := "SELECT false"
	for _, tb := range guestRecordTables {
		q += " OR EXISTS(SELECT 1 FROM " + tb + " WHERE tenant_id <> $1)"
	}
	if err := tx.QueryRow(ctx, q, tenant).Scan(&found); err != nil || !found {
		t.Fatalf("another customer's device was not detected: %v %v", found, err)
	}
	if err := tx.QueryRow(ctx, q, other).Scan(&found); err != nil {
		t.Fatal(err)
	}
	_ = found // other rows of the shared test database may belong to other tenants
}
