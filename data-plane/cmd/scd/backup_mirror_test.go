package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// Another S3 server with the same bucket and prefix is another storage: its copies are not taken for the previous server's.
func TestBackupCopyIdentityTellsServersWithTheSameBucketApart(t *testing.T) {
	a := &ledger.S3Dest{Endpoint: "https://nas-a.example:9000", Region: "us-east-1", Bucket: "onegate", Prefix: "site/backups", SecretKey: "s1"}
	b := &ledger.S3Dest{Endpoint: "https://nas-b.example:9000", Region: "us-east-1", Bucket: "onegate", Prefix: "site/backups", SecretKey: "s1"}
	if a.Describe() != b.Describe() {
		t.Fatal("precondition: the two descriptions are expected to be equal")
	}
	if backupCopyIdentity(a) == backupCopyIdentity(b) {
		t.Fatal("two S3 servers with the same bucket and prefix share one copy identity")
	}
	rotated := *a
	rotated.SecretKey = "s2"
	if backupCopyIdentity(a) != backupCopyIdentity(&rotated) || strings.Contains(backupCopyIdentity(a), "s1") {
		t.Fatal("a credential is part of the copy identity")
	}
	s1 := &ledger.SFTPDest{Host: "nas", Port: 22, User: "u", Dir: "/b", Fingerprint: "SHA256:aaa"}
	s2 := &ledger.SFTPDest{Host: "nas", Port: 22, User: "u", Dir: "/b", Fingerprint: "SHA256:bbb"}
	if backupCopyIdentity(s1) == backupCopyIdentity(s2) {
		t.Fatal("an SFTP server with another host key is taken for the same storage")
	}
}

// "Copy now" answers at once (202) and the copy runs on; its outcome is in the status. Off, it is refused.
func TestBackupCopyNowStartsTheCopyAndAnswersAtOnce(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	useRetentionConf(t, "BACKUP_KEEP=1\nSAFETY_BACKUP_KEEP=2\nBACKUP_REPLICATE=1\n")
	remote := t.TempDir()
	s := mirrorServer(t, remote)
	set := seedBackup(t, kindScheduled, time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC))

	rec := httptest.NewRecorder()
	s.backupMirrorRun(rec, httptest.NewRequest(http.MethodPost, "/v1/backup/mirror/run", nil))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"started":true`) {
		t.Fatalf("copy now = %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if st := s.mirrorStatus(readBackupSettings(), listBackupMetas()); st["state"] == "ok" && st["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the copy started by Copy now did not complete")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, f := range setFiles(set) {
		if _, err := os.Stat(filepath.Join(remote, f)); err != nil {
			t.Fatalf("%s was not copied: %v", f, err)
		}
	}
	useRetentionConf(t, "BACKUP_KEEP=1\nSAFETY_BACKUP_KEEP=2\nBACKUP_REPLICATE=0\n")
	rec = httptest.NewRecorder()
	s.backupMirrorRun(rec, httptest.NewRequest(http.MethodPost, "/v1/backup/mirror/run", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("copy now with copying off = %d", rec.Code)
	}
}
