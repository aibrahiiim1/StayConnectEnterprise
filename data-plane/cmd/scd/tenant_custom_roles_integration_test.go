//go:build integration

package main

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A previous customer's custom roles count as its data on a cross-tenant move and are purged after its staff accounts, as svc_scd.
func TestIntegration_ACustomRoleOfAnotherTenantIsForeignData(t *testing.T) {
	ownerDSN, scdDSN := os.Getenv("PHASE3_TEST_DSN"), os.Getenv("SCD_ROLE_TEST_DSN")
	if ownerDSN == "" || scdDSN == "" {
		t.Skip("PHASE3_TEST_DSN and SCD_ROLE_TEST_DSN are needed")
	}
	i, j := slices.Index(tenantOwnedTables, "operators"), slices.Index(tenantOwnedTables, "custom_roles")
	if j < 0 || j < i {
		t.Fatalf("custom_roles must be purged, after operators: %v", tenantOwnedTables)
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var prev, cur, role string
	if err := owner.QueryRow(ctx, `INSERT INTO tenants (name, slug) VALUES ('previous customer', 'prev-'||substr(md5(random()::text),1,10)) RETURNING id::text`).Scan(&prev); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO tenants (name, slug) VALUES ('current customer', 'cur-'||substr(md5(random()::text),1,10)) RETURNING id::text`).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO custom_roles (tenant_id, name, permissions) VALUES ($1, 'Night desk', '{"usage":"read"}') RETURNING id::text`,
		prev).Scan(&role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DELETE FROM custom_roles WHERE id = $1`, role)
		_, _ = owner.Exec(ctx, `DELETE FROM tenants WHERE id = ANY($1::uuid[])`, []string{prev, cur})
	})
	sp, err := pgxpool.New(ctx, scdDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	s := &server{db: sp, tenID: cur}
	foreign, err := s.hasForeignTenantData(ctx)
	if err != nil {
		t.Fatalf("svc_scd cannot check for a previous customer's data: %v", err)
	}
	if !foreign {
		t.Fatal("a previous customer's custom role was not seen as foreign data")
	}
	if _, err := sp.Exec(ctx, `DELETE FROM custom_roles WHERE tenant_id IS NOT NULL AND tenant_id <> $1 AND id = $2`, cur, role); err != nil {
		t.Fatalf("svc_scd cannot purge a previous customer's custom role: %v", err)
	}
}
