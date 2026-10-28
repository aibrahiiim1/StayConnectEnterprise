package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// D5-M2: a pass re-reads one stored file in full (not every backup), every file is still re-read in rotation, and a
// change in size, time or (eventually) content is reported.
func TestStoredBackupCheckRotates(t *testing.T) {
	defer scdBackupDirForTest(t, t.TempDir())()
	rotatingCheck.hashed = nil
	now := time.Now()
	var metas []backupMeta
	for i := 0; i < 3; i++ {
		metas = append(metas, seedBackup(t, kindScheduled, now.Add(-time.Duration(i)*time.Hour)))
	}
	reads := 0
	old := rotatingDigest
	rotatingDigest = func(p string) (string, error) { reads++; return fileDigest(p) }
	defer func() { rotatingDigest = old }()

	if probs := verifyStoredBackupsRotating(now); len(probs) != 0 || reads != 6 {
		t.Fatalf("first pass: problems %v, full reads %d (want 6: nothing verified yet)", probs, reads)
	}
	reads = 0
	if probs := verifyStoredBackupsRotating(now.Add(15 * time.Minute)); len(probs) != 0 || reads != 1 {
		t.Fatalf("second pass: problems %v, full reads %d (want 1)", probs, reads)
	}

	// Same size, same modification time, different contents: found within one rotation of the six files.
	target := filepath.Join(scdBackupDir, metas[2].Name)
	fi, _ := os.Stat(target)
	b, _ := os.ReadFile(target)
	b[0] ^= 0xff
	if err := os.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(target, fi.ModTime(), fi.ModTime())
	found := false
	for pass := 2; pass <= 8 && !found; pass++ {
		for _, p := range verifyStoredBackupsRotating(now.Add(time.Duration(pass) * 15 * time.Minute)) {
			if p.Name == metas[2].Name {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("silently changed contents were never detected by the rotation")
	}

	// A missing file is reported on the very next pass.
	_ = os.Remove(filepath.Join(scdBackupDir, metas[0].EtcName))
	missing := false
	for _, p := range verifyStoredBackupsRotating(now.Add(3 * time.Hour)) {
		if p.Name == metas[0].EtcName {
			missing = true
		}
	}
	if !missing {
		t.Fatal("a missing file was not reported")
	}
}
