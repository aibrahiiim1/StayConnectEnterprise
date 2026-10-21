package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The Backups list shows what the one engine recorded about each backup, lists a backup without a note as an earlier backup, and
// does not list bookkeeping files as backups of their own.
func TestBackupListCarriesTheNoteAndHidesBookkeeping(t *testing.T) {
	dir := t.TempDir()
	oldRoot, oldDB := backupRoot, dbBackupDir
	backupRoot, dbBackupDir = dir, filepath.Join(dir, "db")
	t.Cleanup(func() { backupRoot, dbBackupDir = oldRoot, oldDB })
	if err := os.MkdirAll(dbBackupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dbBackupDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("db-20261021T020000Z.sql.gz.age", "x")
	write("db-20261021T020000Z.meta.json", `{"kind":"pre-change","reason":"migration 0149","by":"ops","restorable":true,"ledger_seq":7}`)
	write("db-20261021T020000Z.sql.gz.age.verified", "{}")
	write("db-20260901T020000Z.sql.gz", "old")
	write("db-20261022T020000Z.sql.gz.age.partial", "half")

	rec := httptest.NewRecorder()
	(&server{}).listBackupArtifacts(rec, httptest.NewRequest(http.MethodGet, "/artifacts", nil))
	var rows []backupArtifact
	var env struct {
		Data []backupArtifact `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err == nil && env.Data != nil {
		rows = env.Data
	} else if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list = %s", rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("listed %d entries, want 2 (no markers, notes or partial files): %+v", len(rows), rows)
	}
	for _, r := range rows {
		switch r.Name {
		case "db-20261021T020000Z.sql.gz.age":
			if r.BackupKind != "pre-change" || r.Reason != "migration 0149" || r.By != "ops" || !r.Restorable || r.LedgerSeq != 7 {
				t.Errorf("note not carried: %+v", r)
			}
		case "db-20260901T020000Z.sql.gz":
			if r.BackupKind != "earlier" {
				t.Errorf("a backup with no note is %q, want earlier", r.BackupKind)
			}
		default:
			t.Errorf("unexpected entry %s", r.Name)
		}
	}
}
