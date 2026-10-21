package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A RESTORE REAPPLIES RETENTION TO THE END OF THE BACKLOG, and says so when it could not (review finding: the bounded hourly run
// returned no error, so a restore with more expired rows than one tick takes was reported as fully applied).
func TestRetentionPassesWorkTheBacklogOffAndReportFailures(t *testing.T) {
	old := dataRetentionPassFn
	defer func() { dataRetentionPassFn = old }()
	s := &server{}

	// a backlog of 45 batches in one function: the bounded run stops at its limit, the complete run goes to the end
	left := 45
	dataRetentionPassFn = func(_ *server, _ context.Context, fn string) (int, error) {
		if fn != "iam_v2.run_data_retention" || left == 0 {
			return 0, nil
		}
		left--
		return 500, nil
	}
	if complete, _ := s.retentionPasses(context.Background(), 20); complete || left != 25 {
		t.Fatalf("the bounded run should stop after 20 passes with a backlog (complete=%v, left=%d)", complete, left)
	}
	complete, err := s.retentionPasses(context.Background(), 0)
	if err != nil || !complete || left != 0 {
		t.Fatalf("the complete run did not reach the end: complete=%v err=%v left=%d", complete, err, left)
	}

	// a failing function is reported, the others still finish
	var ran int
	dataRetentionPassFn = func(_ *server, _ context.Context, fn string) (int, error) {
		if fn == "public.run_data_retention_public" {
			return 0, errors.New("statement timeout")
		}
		ran++
		if ran > 3 {
			return 0, nil
		}
		return 1, nil
	}
	complete, err = s.retentionPasses(context.Background(), 0)
	if complete || err == nil || ran < 4 {
		t.Fatalf("a failing function must be reported and must not stop the others: complete=%v err=%v ran=%d", complete, err, ran)
	}

	// the context ends a run that cannot finish
	dataRetentionPassFn = func(_ *server, ctx context.Context, _ string) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if complete, err = s.retentionPasses(ctx, 0); complete || err == nil {
		t.Fatalf("a run that ran out of time reported success: %v %v", complete, err)
	}
}

// A BACKUP WHOSE VERIFICATION MARKER CANNOT BE WRITTEN IS NOT A RECOVERY POINT: the failure is returned, not logged and forgotten.
func TestAVerificationMarkerThatCannotBeWrittenIsAnError(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	if err := writeVerifiedMarker("db-20261021T000000Z.sql.gz.age", 5); err == nil {
		t.Fatal("a marker for a backup that is not there was reported written")
	}
	name := "db-20261021T000001Z.sql.gz.age"
	if err := os.WriteFile(filepath.Join(scdBackupDir, name), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeVerifiedMarker(name, 5); err != nil {
		t.Fatalf("a good marker failed: %v", err)
	}
	if _, err := readVerifiedMarker(name); err != nil {
		t.Fatalf("the marker just written is not readable: %v", err)
	}
	// the stored backup is not rotated away in favour of one that has no marker: takeBackup discards it instead
	src, _ := os.ReadFile("backups.go")
	i := indexOf(string(src), "writeVerifiedMarker(name, tables)")
	j := indexOf(string(src), "s.rotateBackups()\n\tforgetStoredCheck()")
	if i < 0 || j < 0 || i > j {
		t.Fatal("takeBackup must check the marker before it rotates")
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A CRASHED BACKUP'S PARTIAL FILES ARE SWEPT FROM THE ACTIVE BACKUP FOLDER (review finding: only unrelated folders were visited).
func TestTheLeftoverSweepVisitsTheActiveBackupFolder(t *testing.T) {
	dir := t.TempDir()
	restore := scdBackupDirForTest(t, dir)
	defer restore()
	found := false
	for _, d := range leftoverDirs() {
		if d == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("the active backup folder is not swept: %v", leftoverDirs())
	}
	old := filepath.Join(dir, "db-20261020T000000Z.sql.gz.age.partial")
	young := filepath.Join(dir, "db-20261021T000000Z.sql.gz.age.partial")
	keep := filepath.Join(dir, "db-20261019T000000Z.sql.gz.age")
	for _, f := range []string{old, young, keep} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(keep, past, past)
	if n := sweepPartials(dir, time.Now(), 24*time.Hour); n != 1 {
		t.Fatalf("swept %d, want the one old partial", n)
	}
	for f, want := range map[string]bool{old: false, young: true, keep: true} {
		if _, err := os.Stat(f); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", filepath.Base(f), err == nil, want)
		}
	}
}
