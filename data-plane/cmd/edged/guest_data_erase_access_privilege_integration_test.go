//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// An erasure ends the person's own access as svc_edged with what the staff End Package already holds; nothing new is granted.
func TestIntegration_GuestDataErase_EndsAccessWithTheRuntimeRolesOwnGrants(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	ctx := context.Background()
	ep, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	for _, col := range []string{"guest_account_id", "guest_principal_id"} {
		if _, err := ep.Exec(ctx, `SELECT id::text FROM iam_v2.entitlements
		     WHERE tenant_id=gen_random_uuid() AND site_id=gen_random_uuid() AND `+col+`=gen_random_uuid() AND status <> 'TERMINATED'`); err != nil {
			t.Errorf("svc_edged cannot find the access to end by %s: %v", col, err)
		}
	}
	var ok bool
	if err := ep.QueryRow(ctx, `SELECT has_function_privilege(current_user, 'iam_v2.admin_end_package(uuid,uuid,uuid,uuid,text,uuid)', 'EXECUTE')`).Scan(&ok); err != nil || !ok {
		t.Errorf("svc_edged cannot run the staff End Package writer: %v", err)
	}
	if err := ep.QueryRow(ctx, `SELECT has_function_privilege(current_user, 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)', 'EXECUTE')`).Scan(&ok); err != nil || ok {
		t.Errorf("svc_edged must not hold the raw entitlement state writer (Gate-P B-7): %v %v", ok, err)
	}
}
