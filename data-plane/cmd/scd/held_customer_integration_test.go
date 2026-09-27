//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The site database is the authoritative record of whose data this appliance holds: an identity reset keeps
// it, so it must decide holds_customer_id even with no assignment on disk.
func TestIntegration_HeldCustomerFromSiteDatabase(t *testing.T) {
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
	var existing int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM public.tenants`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing != 0 {
		t.Skip("the test database already holds tenants; this test needs an empty tenants table")
	}

	// Factory-clean: no tenant rows, no assignment.
	if got, err := heldCustomerID(ctx, p, t.TempDir()); err != nil || got != "" {
		t.Fatalf("empty database reported %q (%v)", got, err)
	}

	var a string
	if err := p.QueryRow(ctx, `INSERT INTO public.tenants(id, slug, name) SELECT g, g::text, g::text FROM gen_random_uuid() g RETURNING id::text`).Scan(&a); err != nil {
		t.Fatalf("seed a tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1::uuid`, a) })
	if got, err := heldCustomerID(ctx, p, t.TempDir()); err != nil || got != a {
		t.Fatalf("held %q (%v), want %s", got, err, a)
	}

	var b string
	if err := p.QueryRow(ctx, `INSERT INTO public.tenants(id, slug, name) SELECT g, g::text, g::text FROM gen_random_uuid() g RETURNING id::text`).Scan(&b); err != nil {
		t.Fatalf("seed a second tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1::uuid`, b) })
	if _, err := heldCustomerID(ctx, p, t.TempDir()); !errors.Is(err, errHoldsSeveralCustomers) {
		t.Fatalf("two customers' data must refuse registration, got %v", err)
	}
}
