package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

func mirrorServer(t *testing.T, remote string) *server {
	t.Helper()
	s := &server{}
	s.ledgerRT.store = ledger.NewStore(ledger.Config{LocalPath: filepath.Join(t.TempDir(), "ledger.jsonl"), Dest: &ledger.DirDest{Root: remote}})
	return s
}

func TestBackupCopyIsVerifiedByReadBackAndFollowsRotation(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	useRetentionConf(t, "BACKUP_KEEP=1\nSAFETY_BACKUP_KEEP=2\nBACKUP_REPLICATE=1\n")
	remote := t.TempDir()
	s := mirrorServer(t, remote)
	base := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	first := seedBackup(t, kindScheduled, base)

	s.mirrorBackups(context.Background())
	for _, f := range setFiles(first) {
		want, _ := os.ReadFile(filepath.Join(scdBackupDir, f))
		got, err := os.ReadFile(filepath.Join(remote, f))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s was not copied exactly (err %v)", f, err)
		}
	}
	st := s.mirrorStatus(readBackupSettings(), listBackupMetas())
	if st["state"] != "ok" {
		t.Fatalf("status after a verified copy = %v", st)
	}

	// A newer set rotates the first out locally; its copy is removed from the storage only after the new one is copied.
	second := seedBackup(t, kindManual, base.Add(24*time.Hour))
	s.rotateBackups()
	s.mirrorBackups(context.Background())
	if _, err := os.Stat(filepath.Join(remote, second.Name)); err != nil {
		t.Fatal("the new set was not copied")
	}
	if _, err := os.Stat(filepath.Join(remote, first.Name)); err == nil {
		t.Error("the copy of a rotated set was kept at the storage")
	}
	// Something this appliance did not write is never removed.
	foreign := filepath.Join(remote, "db-20250101T000000Z.sql.gz.age")
	_ = os.WriteFile(foreign, []byte("not ours"), 0o600)
	s.mirrorBackups(context.Background())
	if _, err := os.Stat(foreign); err != nil {
		t.Error("a file the appliance did not copy was removed from the storage")
	}
}

func TestBackupCopyFailureNeverTouchesTheLocalBackupAndIsNotReportedCopied(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	useRetentionConf(t, "BACKUP_REPLICATE=1\n")
	s := mirrorServer(t, filepath.Join(t.TempDir(), "unplugged-disk")) // does not exist: every write fails
	m := seedBackup(t, kindManual, time.Now().UTC())
	before, _ := os.ReadFile(filepath.Join(scdBackupDir, m.Name))
	s.mirrorBackups(context.Background())
	after, err := os.ReadFile(filepath.Join(scdBackupDir, m.Name))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("a failed copy changed or removed the local backup")
	}
	st := s.mirrorStatus(readBackupSettings(), listBackupMetas())
	if st["state"] == "ok" {
		t.Fatalf("a failed copy was reported as copied: %v", st)
	}
	for _, row := range st["sets"].([]map[string]any) {
		if row["state"] == "copied" {
			t.Fatalf("set %v shown copied although nothing was written", row["name"])
		}
	}
}

func TestBackupCopyIsOffByDefaultAndANewStorageGetsEverySetAgain(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	useRetentionConf(t, "")
	remote := t.TempDir()
	s := mirrorServer(t, remote)
	m := seedBackup(t, kindManual, time.Now().UTC())
	s.mirrorBackups(context.Background())
	if _, err := os.Stat(filepath.Join(remote, m.Name)); err == nil {
		t.Fatal("a backup was copied although copying is off (local only is the default)")
	}
	useRetentionConf(t, "BACKUP_REPLICATE=1\n")
	s.mirrorBackups(context.Background())
	replacement := t.TempDir()
	s.ledgerRT.store.SetDest(&ledger.DirDest{Root: replacement})
	s.mirrorBackups(context.Background())
	if _, err := os.Stat(filepath.Join(replacement, m.Name)); err != nil {
		t.Fatal("a replaced storage did not receive the retained sets")
	}
	if _, err := os.Stat(filepath.Join(remote, m.Name)); err != nil {
		t.Error("replacing the storage deleted the copy at the previous one")
	}
}
