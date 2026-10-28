package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Audit Phase 1 L15: the console gets file names and a short error, never host paths.
func TestBackupHealthHidesHostPaths(t *testing.T) {
	pointBackupPathsAt(t)
	now := time.Now()
	writeDump(t, "central-20261015T020000Z-scheduled.dump", now.Add(-time.Hour))
	fail := now.UTC()
	raw, _ := json.Marshal(dbBackupRun{LastFailureAt: &fail, LastAttemptAt: &fail,
		LastError: "pg_dump: could not write /opt/stayconnect/backups/db/central-x.dump.partial: No space left " + strings.Repeat("x", 400)})
	_ = os.WriteFile(dbBackupStatusPath, raw, 0o600)
	h := dbBackupHealth(now)
	if h.NewestFile == nil || *h.NewestFile != "central-20261015T020000Z-scheduled.dump" {
		t.Fatalf("newest_file must be the bare file name: %v", h.NewestFile)
	}
	if h.LastError == nil || strings.Contains(*h.LastError, "/opt/") || !strings.Contains(*h.LastError, "central-x.dump.partial") ||
		len([]rune(*h.LastError)) > 201 {
		t.Fatalf("last_error must name the file without its path and stay short: %v", *h.LastError)
	}
	if h.Problem == nil || strings.Contains(*h.Problem, "/opt/") {
		t.Fatalf("problem leaks a path: %v", h.Problem)
	}
}
