//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The hourly run and the restore step against a real database: new journal entries reach the file, entries the
// database does not know are applied (or reported, and tried again), and a second run does nothing.
func TestIntegrationErasureJournalFileAndReplay(t *testing.T) {
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
	var tenant, site string
	if err := conn.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&tenant, &site); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), erasureJournalName)

	// An erasure the database knows: it reaches the file.
	var known string
	if err := conn.QueryRow(ctx, `INSERT INTO iam_v2.erasure_journal(tenant_id,site_id,erasure_ref,kind,subject_id)
		VALUES ($1,$2,'ER-'||upper(substr(md5(random()::text),1,12)),'account',gen_random_uuid()) RETURNING id::text`, tenant, site).Scan(&known); err != nil {
		t.Skipf("the erasure journal is not in this database: %v", err)
	}
	added, err := syncErasureJournal(ctx, conn, tenant, site, path)
	if err != nil || added != 1 {
		t.Fatalf("sync = %d %v", added, err)
	}
	if again, err := syncErasureJournal(ctx, conn, tenant, site, path); err != nil || again != 0 {
		t.Fatalf("a second sync added %d (%v)", again, err)
	}

	// The restored database lacks that entry and one more. Neither names a record that exists (the real-record replay is
	// proved in edged's guest_data_restore_replay tests), so both are skipped as gone and then known. An entry that cannot be applied is reported and left unrecorded.
	entries, _ := readJournalFile(path)
	mk := func(id, at, ref, kind string) json.RawMessage {
		b, _ := json.Marshal(journalEntry{ID: id, ErasedAt: at, TenantID: tenant, SiteID: site, Ref: ref, Kind: kind, SubjectID: "00000000-0000-4000-8000-0000000000cc"})
		return b
	}
	var idA, idB string
	if err := conn.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&idA, &idB); err != nil {
		t.Fatal(err)
	}
	gone := mk(idA, "2026-10-03T00:00:00Z", "ER-0000000000AA", "account")
	bad := mk(idB, "2026-10-04T00:00:00Z", "ER-0000000000BB", "bogus")
	merged, _ := mergeJournal(entries, []json.RawMessage{gone, bad})
	_ = merged
	if _, err := conn.Exec(ctx, `DELETE FROM iam_v2.erasure_journal WHERE id=$1::uuid`, known); err != nil {
		// append-only: simulate the restored database by replaying under replica role
		tx, terr := conn.Begin(ctx)
		if terr != nil {
			t.Fatal(terr)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
		if _, derr := tx.Exec(ctx, `DELETE FROM iam_v2.erasure_journal WHERE id=$1::uuid`, known); derr != nil {
			t.Fatal(derr)
		}
		_ = tx.Commit(ctx)
	}
	if err := writeJournalFile(path, merged); err != nil {
		t.Fatal(err)
	}
	res, absent, err := reconcileErasureJournal(ctx, conn, path)
	if err != nil || absent {
		t.Fatalf("reconcile = %+v absent=%v %v", res, absent, err)
	}
	if res.Entries != 3 || res.Reapplied != 0 || res.RecordsGone != 2 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Ref, "BB") {
		t.Fatalf("reconcile result: %+v", res)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM iam_v2.erasure_journal WHERE id IN ($1::uuid,$2::uuid,$3::uuid)`, known, idA, idB).Scan(&n); err != nil || n != 2 {
		t.Fatalf("known entries recorded: %d %v (the one that failed must not be)", n, err)
	}
	// Run again: the failed entry is tried again, nothing else happens.
	res2, _, err := reconcileErasureJournal(ctx, conn, path)
	if err != nil || res2.Reapplied != 0 || len(res2.Failed) != 1 {
		t.Fatalf("second reconcile: %+v %v", res2, err)
	}
}
