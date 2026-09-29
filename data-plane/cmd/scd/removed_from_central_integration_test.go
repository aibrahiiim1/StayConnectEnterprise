//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TENANT DATA IN THE SITE DATABASE COUNTS AS HAVING HELD A CUSTOMER, even with no assignment file on disk --
// a wiped or lost assignment directory must not turn an appliance carrying a customer's data back into one
// that may register under a new key.
func TestIntegration_OrphanWithTenantDataButNoAssignmentIsBlocked(t *testing.T) {
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
	var tenant string
	if err := p.QueryRow(ctx, `INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id::text`).
		Scan(&tenant); err != nil {
		t.Fatalf("seed a tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1::uuid`, tenant) })

	f := newOrphanFixture(t)
	f.srv.db = p
	held, err := f.srv.heldCustomer(ctx, f.paths.AssignmentDir)
	if err != nil || !held {
		t.Fatalf("tenant data in the site database was not counted as a held customer (held=%v err=%v)", held, err)
	}
	if got := f.srv.handleConfirmedOrphan(ctx, f.paths); got != "removed" {
		t.Fatalf("outcome %q, want removed", got)
	}
	if !exists(filepath.Join(f.paths.IdentityDir, "identity.key")) {
		t.Fatal("the identity key was cleared")
	}
}
