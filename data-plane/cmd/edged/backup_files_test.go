package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func useBackupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldRoot, oldDB := backupRoot, dbBackupDir
	backupRoot, dbBackupDir = dir, filepath.Join(dir, "db")
	t.Cleanup(func() { backupRoot, dbBackupDir = oldRoot, oldDB })
	if err := os.MkdirAll(dbBackupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// P9 L7: a symlink in the backup tree is not served, whatever it points at.
func TestResolveArtifactRefusesSymlinks(t *testing.T) {
	dir := useBackupDir(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("not a backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dbBackupDir, "db-20261028T000000Z.sql.gz.age")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok := resolveArtifact("db-20261028T000000Z.sql.gz.age"); ok {
		t.Fatal("a symlinked artifact must not resolve")
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy-1.tgz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveArtifact("deploy-1.tgz"); !ok {
		t.Fatal("a regular file still resolves")
	}
}

// P9 L8: listing reuses an archive's digest while the file is unchanged, and still notices a same-size rewrite
// whose mtime was set back.
func TestVerificationDigestIsReusedUntilTheFileChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the digest cache relies on Linux ctime")
	}
	useBackupDir(t)
	name := "db-20261028T010000Z.sql.gz.age"
	full := filepath.Join(dbBackupDir, name)
	body := []byte("archive-bytes-0001")
	if err := os.WriteFile(full, body, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(full)
	sum := sha256.Sum256(body)
	marker, _ := json.Marshal(verifiedRecord{VerifiedAt: time.Now().UTC(), SizeBytes: fi.Size(),
		ModTime: fi.ModTime().UTC(), Tables: 3, SHA256: hex.EncodeToString(sum[:])})
	if err := os.WriteFile(full+".verified", marker, 0o600); err != nil {
		t.Fatal(err)
	}

	before := digestFiles.Load()
	for i := 0; i < 3; i++ {
		if _, ok := readVerification(dbBackupDir, name); !ok {
			t.Fatalf("read %d: an intact archive must read as verified", i)
		}
	}
	if n := digestFiles.Load() - before; n != 1 {
		t.Fatalf("three listings digested the archive %d times, want 1", n)
	}

	// Same size, different bytes, mtime put back: only ctime betrays it.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(full, []byte("archive-bytes-9999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, ok := readVerification(dbBackupDir, name); ok {
		t.Fatal("a rewritten archive must not read as verified from the cache")
	}
}
