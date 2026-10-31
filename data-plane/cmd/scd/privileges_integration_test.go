//go:build integration

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A sequence owned by a column follows its table's owner and is never re-owned on its own: the replay of "ALTER SEQUENCE ...
// OWNER" for one whose owner differed from the restoring role failed every restore once migration 0203 added such a table
// (restore drill, 2026-11-01). A free-standing sequence is still carried.
func TestRestorePrivilegeCaptureLeavesOwnedSequencesToTheirTable(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`CREATE TABLE public.zz_owned_seq_probe (id bigserial PRIMARY KEY, n integer GENERATED ALWAYS AS IDENTITY)`,
		`CREATE SEQUENCE public.zz_free_seq_probe`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rows, err := tx.Query(ctx, privilegeCaptureSQL)
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	for rows.Next() {
		var ord int
		var s string
		if err := rows.Scan(&ord, &s); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s, "zz_") {
			stmts = append(stmts, s)
		}
	}
	rows.Close()
	all := strings.Join(stmts, "\n")
	if strings.Contains(all, "ALTER SEQUENCE public.zz_owned_seq_probe_id_seq OWNER") ||
		strings.Contains(all, "ALTER SEQUENCE public.zz_owned_seq_probe_n_seq OWNER") {
		t.Fatalf("a column-owned sequence is re-owned on its own:\n%s", all)
	}
	if !strings.Contains(all, "ALTER SEQUENCE public.zz_free_seq_probe OWNER") || !strings.Contains(all, "ALTER TABLE public.zz_owned_seq_probe OWNER") {
		t.Fatalf("the table or the free-standing sequence is not carried:\n%s", all)
	}
	// The replay works when the table's owner is not the one that created it (what the drill hit).
	if _, err := tx.Exec(ctx, `ALTER TABLE public.zz_owned_seq_probe OWNER TO stayconnect`); err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if strings.HasPrefix(s, "ALTER ") {
			if _, err := tx.Exec(ctx, s); err != nil {
				t.Fatalf("replay %q: %v", s, err)
			}
		}
	}
}
