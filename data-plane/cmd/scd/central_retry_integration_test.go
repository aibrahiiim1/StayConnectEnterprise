//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// THE LEDGER HALF OF THE OFFLINE RECONCILIATION, against a real edge_offline_packages: a package Central has
// not confirmed is offered again on every pass, and one it confirmed is stamped once and never offered again.
func TestIntegration_OfflineReconcileLedger(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var present bool
	if err := p.QueryRow(ctx, `SELECT to_regclass('public.edge_offline_packages') IS NOT NULL`).Scan(&present); err != nil || !present {
		t.Skip("edge_offline_packages is not in this schema")
	}
	var a, b string
	if err := p.QueryRow(ctx, `
		WITH ins AS (INSERT INTO edge_offline_packages (package_id, nonce)
		             VALUES (gen_random_uuid(), gen_random_uuid()::text), (gen_random_uuid(), gen_random_uuid()::text)
		             RETURNING package_id::text)
		SELECT min(package_id), max(package_id) FROM ins`).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(ctx, `DELETE FROM edge_offline_packages WHERE package_id IN ($1::uuid,$2::uuid)`, a, b)
	})

	s := &server{db: p}
	rec := s.offlineReconcilerFor()
	confirmed := map[string]bool{a: true} // Central confirms a; b keeps failing
	sent := map[string]int{}
	rec.send = func(_ context.Context, id string) (bool, error) {
		if id != a && id != b {
			return false, nil // someone else's row in a shared test database: leave it alone
		}
		sent[id]++
		return confirmed[id], nil
	}
	for i := 0; i < 2; i++ {
		rec.runOnce(ctx)
	}
	if sent[a] != 1 {
		t.Fatalf("a confirmed package was offered %d times, want once", sent[a])
	}
	if sent[b] != 2 {
		t.Fatalf("an unconfirmed package was offered %d times over two passes, want 2", sent[b])
	}
	var aDone, bDone bool
	if err := p.QueryRow(ctx, `SELECT
		(SELECT reconciled_at IS NOT NULL FROM edge_offline_packages WHERE package_id=$1::uuid),
		(SELECT reconciled_at IS NOT NULL FROM edge_offline_packages WHERE package_id=$2::uuid)`, a, b).
		Scan(&aDone, &bDone); err != nil {
		t.Fatal(err)
	}
	if !aDone || bDone {
		t.Fatalf("reconciled_at: confirmed=%v unconfirmed=%v; want true/false", aDone, bDone)
	}
}
