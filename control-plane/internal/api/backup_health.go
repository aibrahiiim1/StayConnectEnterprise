package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Where the Central host's backup facts live. Variables so a test can point them at a temporary directory.
var (
	backupRetentionStatusPath = "/opt/stayconnect/backup-retention-status.json" // stayconnect-backup-cleanup
	backupPinsPath            = "/etc/stayconnect/backup-retention.pins"
	dbBackupStatusPath        = "/opt/stayconnect/central-backup-status.json" // central-backup.sh, every run
	dbBackupDir               = "/opt/stayconnect/backups/db"                 // central-backup.sh's dumps
)

// dbBackupMaxAge is when the newest database dump counts as overdue: the daily timer plus a few hours.
const dbBackupMaxAge = 26 * time.Hour

// dbBackupRun is what central-backup.sh records on every run, success or failure.
type dbBackupRun struct {
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
	LastError     string     `json:"last_error"`
	LastFile      string     `json:"last_file"`
}

// DBBackupHealth answers the one question the page exists for: is there a recent, good database backup?
type DBBackupHealth struct {
	NewestFile    *string    `json:"newest_file"`
	NewestAt      *time.Time `json:"newest_at"`
	NewestBytes   int64      `json:"newest_bytes"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
	LastError     *string    `json:"last_error"`
	Healthy       bool       `json:"healthy"`
	Problem       *string    `json:"problem"`
}

// dbBackupHealth reads the dump directory itself -- the newest finished dump is a fact on disk, not something
// a status file says -- and the last-run record, and decides. A failing daily dump used to be invisible: the
// cleanup status kept reporting a fresh last_run, and the page never showed when the newest dump was taken.
func dbBackupHealth(now time.Time) DBBackupHealth {
	var h DBBackupHealth
	if raw, err := os.ReadFile(dbBackupStatusPath); err == nil {
		var run dbBackupRun
		if json.Unmarshal(raw, &run) == nil {
			h.LastAttemptAt, h.LastSuccessAt, h.LastFailureAt = run.LastAttemptAt, run.LastSuccessAt, run.LastFailureAt
			h.LastError = strPtr(run.LastError)
		}
	}
	type dump struct {
		path string
		at   time.Time
		size int64
	}
	var dumps []dump
	entries, _ := os.ReadDir(dbBackupDir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "central-") || !strings.HasSuffix(name, ".dump") {
			continue // a *.partial is a dump still being written (or abandoned), never a backup
		}
		if fi, err := e.Info(); err == nil && fi.Size() > 0 {
			dumps = append(dumps, dump{filepath.Join(dbBackupDir, name), fi.ModTime(), fi.Size()})
		}
	}
	sort.Slice(dumps, func(i, j int) bool { return dumps[i].at.After(dumps[j].at) })
	if len(dumps) > 0 {
		at := dumps[0].at.UTC()
		h.NewestFile, h.NewestAt, h.NewestBytes = strPtr(dumps[0].path), &at, dumps[0].size
	}
	problem := ""
	switch {
	case h.NewestAt == nil:
		problem = "There is no database backup on this host."
	case h.LastFailureAt != nil && (h.LastSuccessAt == nil || h.LastFailureAt.After(*h.LastSuccessAt)):
		problem = "The last database backup FAILED"
		if h.LastError != nil {
			problem += ": " + *h.LastError
		}
		problem += ". The newest good backup is older than that run."
	case now.Sub(*h.NewestAt) > dbBackupMaxAge:
		problem = "The newest database backup is more than a day old; the daily backup is not running."
	}
	h.Healthy = problem == ""
	h.Problem = strPtr(problem)
	return h
}

// BackupHealthHandler exposes the Central host's backup health: the database backups (newest dump, the last
// run and whether it failed) and the backup/rollback retention status written by stayconnect-backup-cleanup
// (last cleanup, retained/pinned/protected/delete-candidate artifacts, disk usage, cleanup failures, whether a
// valid rollback path exists). Read-only, platform view.
func (b *Base) BackupHealthHandler(w http.ResponseWriter, r *http.Request) {
	db := dbBackupHealth(time.Now())
	raw, err := os.ReadFile(backupRetentionStatusPath)
	if err != nil {
		WriteJSON(w, http.StatusOK, map[string]any{
			"available":       false,
			"message":         "backup cleanup has not run yet on this host",
			"database_backup": db,
		})
		return
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "status parse failed")
		return
	}
	// Operator-pinned artifacts (paths that cleanup must always keep).
	pins := []string{}
	if pf, err := os.ReadFile(backupPinsPath); err == nil {
		for _, ln := range strings.Split(string(pf), "\n") {
			ln = strings.TrimSpace(ln)
			if ln != "" && !strings.HasPrefix(ln, "#") {
				pins = append(pins, ln)
			}
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"available":       true,
		"status":          status,
		"pins":            pins,
		"database_backup": db,
	})
}
