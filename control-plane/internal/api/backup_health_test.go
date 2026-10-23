package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pointBackupPathsAt(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := []string{backupRetentionStatusPath, backupPinsPath, dbBackupStatusPath, dbBackupDir}
	backupRetentionStatusPath = filepath.Join(dir, "retention.json")
	backupPinsPath = filepath.Join(dir, "pins")
	dbBackupStatusPath = filepath.Join(dir, "central-backup-status.json")
	dbBackupDir = filepath.Join(dir, "db")
	_ = os.MkdirAll(dbBackupDir, 0o700)
	t.Cleanup(func() {
		backupRetentionStatusPath, backupPinsPath, dbBackupStatusPath, dbBackupDir = old[0], old[1], old[2], old[3]
	})
	return dir
}

func writeDump(t *testing.T, name string, at time.Time) {
	t.Helper()
	p := filepath.Join(dbBackupDir, name)
	if err := os.WriteFile(p, []byte("PGDMP"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, at, at)
}

func TestBackupHealthShowsNewestDumpAndFailures(t *testing.T) {
	pointBackupPathsAt(t)
	now := time.Now()

	if h := dbBackupHealth(now); h.Healthy || h.Problem == nil || h.NewestAt != nil {
		t.Fatalf("no dump at all is unhealthy: %+v", h)
	}

	writeDump(t, "central-20261015T020000Z-scheduled.dump", now.Add(-5*time.Hour))
	writeDump(t, "central-20261016T020000Z-scheduled.dump.partial", now.Add(-time.Hour)) // never a backup
	h := dbBackupHealth(now)
	if !h.Healthy || h.NewestFile == nil || filepath.Base(*h.NewestFile) != "central-20261015T020000Z-scheduled.dump" {
		t.Fatalf("a 5-hour-old finished dump is healthy and is the newest (the partial is ignored): %+v", h)
	}

	ok, fail := now.Add(-5*time.Hour).UTC(), now.Add(-time.Hour).UTC()
	raw, _ := json.Marshal(dbBackupRun{LastAttemptAt: &fail, LastSuccessAt: &ok, LastFailureAt: &fail, LastError: "pg_dump failed"})
	_ = os.WriteFile(dbBackupStatusPath, raw, 0o600)
	if h = dbBackupHealth(now); h.Healthy || h.Problem == nil {
		t.Fatalf("a failed last run must be reported: %+v", h)
	}

	_ = os.Remove(dbBackupStatusPath)
	_ = os.Remove(filepath.Join(dbBackupDir, "central-20261015T020000Z-scheduled.dump"))
	writeDump(t, "central-20261012T020000Z-scheduled.dump", now.Add(-4*24*time.Hour))
	if h = dbBackupHealth(now); h.Healthy {
		t.Fatalf("a four-day-old newest dump is overdue: %+v", h)
	}

	rec := httptest.NewRecorder()
	(&Base{}).BackupHealthHandler(rec, httptest.NewRequest(http.MethodGet, "/cloud/v1/backup-health", nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["available"] != false || out["database_backup"] == nil {
		t.Fatalf("handler: %s", rec.Body.String())
	}
}
