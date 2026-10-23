//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestIntegration_TheDefaultDatabasePasswordProbe(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	if _, err := admin.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'stayconnect') THEN CREATE ROLE stayconnect LOGIN; END IF;
		END $$`); err != nil {
		t.Fatal(err)
	}
	set := func(pw string) {
		if _, err := admin.Exec(ctx, `ALTER ROLE stayconnect LOGIN PASSWORD '`+pw+`'`); err != nil {
			t.Fatal(err)
		}
	}
	cc := admin.Config()
	t.Cleanup(func() { set("not-the-default-" + t.Name()) })

	set("stayconnect")
	if found, ok := newDefaultDBPasswordCheck(cc.Host, cc.Port).probe(ctx); !found || !ok {
		t.Fatalf("default password in use: found=%v ok=%v", found, ok)
	}
	set("a-generated-password-1234")
	if found, ok := newDefaultDBPasswordCheck(cc.Host, cc.Port).probe(ctx); found || !ok {
		t.Fatalf("default password refused: found=%v ok=%v", found, ok)
	}
}
