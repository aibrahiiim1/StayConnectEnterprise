//go:build integration

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A restore keeps the live key-holder register (backup_key_carry.go): the "backup" state is rebuilt inside one
// transaction -- a removed holder active again, a holder the live register never had -- and the carry puts the
// live state back. Rolled back at the end, so the shared test database is untouched.
func TestIntegrationBackupKeyRegisterSurvivesARestore(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	key := func(c string) string { return "age1" + strings.Repeat(c, 58) }
	var tenant, site, kept, departed string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&tenant, &site); err != nil {
		t.Fatal(err)
	}
	holder := func(label, k, status string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO public.backup_key_holders
			  (tenant_id, site_id, label, public_key, status, loss_acknowledged, created_by, confirmed_at, removed_at, removed_by, remove_reason)
			VALUES ($1,$2,$3,$4,$5,true,'test',now(),
			        CASE WHEN $5='REMOVED' THEN now() END, CASE WHEN $5='REMOVED' THEN 'test' END, CASE WHEN $5='REMOVED' THEN 'left' END)
			RETURNING id::text`, tenant, site, label, k, status).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
		return id
	}
	kept = holder("General Manager", key("q"), "ACTIVE")
	departed = holder("Former IT manager", key("p"), "REMOVED")

	// The live register, as the restore reads it.
	var snap string
	if err := tx.QueryRow(ctx, keyRegisterCaptureSQL).Scan(&snap); err != nil || snap == "" {
		t.Fatalf("capture: %q %v", snap, err)
	}

	// What an older backup would hold: the departed holder still active, and a holder unknown to the live register.
	if _, err := tx.Exec(ctx, `UPDATE public.backup_key_holders SET status='ACTIVE', removed_at=NULL, removed_by=NULL, remove_reason=NULL WHERE id=$1`, departed); err != nil {
		t.Fatal(err)
	}
	stranger := holder("Unknown to the live register", key("r"), "ACTIVE")

	if _, err := tx.Exec(ctx, keyRegisterCarryFn); err != nil {
		t.Fatalf("create carry function: %v", err)
	}
	var out string
	if err := tx.QueryRow(ctx, `SELECT pg_temp.carry_key_register($1::jsonb)`, snap).Scan(&out); err != nil {
		t.Fatalf("carry: %v", err)
	}
	if !strings.HasPrefix(out, "carried:") {
		t.Fatalf("carry = %q", out)
	}
	status := func(id string) string {
		var s string
		if err := tx.QueryRow(ctx, `SELECT status FROM public.backup_key_holders WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for name, c := range map[string]struct{ id, want string }{
		"kept holder":               {kept, "ACTIVE"},
		"departed holder":           {departed, "REMOVED"},
		"holder the live never had": {stranger, "REMOVED"},
	} {
		if got := status(c.id); got != c.want {
			t.Errorf("%s = %s, want %s", name, got, c.want)
		}
	}
	var recorded int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.backup_key_holder_events WHERE holder_id=$1 AND event='removed' AND actor='restore'`,
		stranger).Scan(&recorded); err != nil || recorded != 1 {
		t.Errorf("the restore's removal was not recorded: %d %v", recorded, err)
	}
}
