package main

// Backups: surfaces the artefacts under /opt/stayconnect/backups and the retention sweep, and lets an
// operator take, download, verify and restore database backups.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
)

// backupRoot is a var only so tests can point it at a temporary directory.
var backupRoot = "/opt/stayconnect/backups"

// retentionStatusPath is written by stayconnect-backup-cleanup on every run.
const retentionStatusPath = "/opt/stayconnect/backup-retention-status.json"

// dbBackupDir keeps database dumps separate from deploy/rollback artefacts (the sweep has a keep_db policy for it).
var dbBackupDir = filepath.Join(backupRoot, "db")

// safeArtifact constrains a caller-supplied name to a single path element of known shape (not merely "..").
var safeArtifact = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *server) backupsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", s.backupHealth)
	r.Get("/settings", s.backupSettingsGet)
	r.Put("/settings", s.backupSettingsSet)
	r.Get("/artifacts", s.listBackupArtifacts)
	r.Post("/run", s.runDatabaseBackup)
	// Download is a separate resource (backup_download.go).
	r.Post("/artifacts/{name}/verify", s.verifyBackupArtifact)
	// Destructive: step-up and typed confirmation in the handler; only a verified backup is accepted, enforced again by scd.
	r.Post("/artifacts/{name}/restore", s.restoreDatabaseBackup)
	r.Get("/maintenance", s.backupMaintenance)
	r.Get("/last-restore", s.lastRestoreResult)
	r.Get("/ledger", s.ledgerStatusGet)
	r.Post("/ledger/destination/probe", s.ledgerDestinationProbe)
	r.Post("/ledger/destination/test", s.ledgerDestinationTest)
	r.Put("/ledger/destination", s.ledgerDestinationSave)
	r.Post("/ledger/recover", s.ledgerRecover)
	r.Get("/status", s.backupStatusGet)
	r.Post("/earlier/remove", s.earlierBackupsRemove)
	return r
}

// scd owns the root-owned settings files, so this proxies; RBAC, step-up and audit stay here.
func (s *server) backupSettingsGet(w http.ResponseWriter, r *http.Request) {
	code, body, err := s.scd.call(r.Context(), http.MethodGet, "/v1/backup/settings", nil)
	if err != nil || code != http.StatusOK {
		jsonErr(w, http.StatusInternalServerError, "internal", scdDetail(body, err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

type backupSettingsPut struct {
	Retention map[string]int `json:"retention"`
	Schedule  string         `json:"schedule"`
	Password  string         `json:"password"`
}

func (s *server) backupSettingsSet(w http.ResponseWriter, r *http.Request) {
	var in backupSettingsPut
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	// Step-up: retention decides how far back the appliance can recover.
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/settings",
		map[string]any{"retention": in.Retention, "schedule": in.Schedule})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", scdDetail(body, err))
		return
	}
	if code != http.StatusOK {
		// Pass the appliance's actionable reason through rather than flattening it.
		jsonErr(w, http.StatusBadRequest, "validation", scdDetail(body, nil))
		return
	}
	s.audit(r, "backup.settings_changed", "backup", "retention",
		map[string]any{"retention": in.Retention, "schedule": in.Schedule})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

type backupHealth struct {
	// Retention is the sweep's own status document, passed through rather than re-derived.
	Retention map[string]any `json:"retention,omitempty"`
	// RetentionReadable distinguishes "no failures reported" from "could not read".
	RetentionReadable bool   `json:"retention_readable"`
	RetentionError    string `json:"retention_error,omitempty"`

	TimerActive   bool   `json:"timer_active"`
	TimerNextRun  string `json:"timer_next_run,omitempty"`
	TimerLastRun  string `json:"timer_last_run,omitempty"`
	TimerReadable bool   `json:"timer_readable"`

	DatabaseBackups int    `json:"database_backups"`
	NewestDatabase  string `json:"newest_database_backup,omitempty"`
}

func (s *server) backupHealth(w http.ResponseWriter, r *http.Request) {
	out := backupHealth{}

	if b, err := os.ReadFile(retentionStatusPath); err != nil {
		out.RetentionError = "the retention sweep has not written a status document yet"
	} else if err := json.Unmarshal(b, &out.Retention); err != nil {
		out.RetentionError = "the retention status document could not be parsed"
	} else {
		out.RetentionReadable = true
	}

	// The timer, not a stale status document, says whether the sweep will run again.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(ctx, "systemctl", "show", "stayconnect-backup-cleanup.timer",
		"--property=ActiveState,NextElapseUSecRealtime,LastTriggerUSec").Output(); err == nil {
		out.TimerReadable = true
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || v == "" {
				continue
			}
			switch k {
			case "ActiveState":
				out.TimerActive = v == "active"
			case "NextElapseUSecRealtime":
				out.TimerNextRun = v
			case "LastTriggerUSec":
				out.TimerLastRun = v
			}
		}
	}

	if entries, err := os.ReadDir(dbBackupDir); err == nil {
		var newest time.Time
		for _, e := range entries {
			if e.IsDir() || !reDBBackupFile.MatchString(e.Name()) {
				continue
			}
			out.DatabaseBackups++
			if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		}
		if !newest.IsZero() {
			out.NewestDatabase = newest.UTC().Format(time.RFC3339)
		}
	}

	writeJSON(w, http.StatusOK, out)
}

type backupArtifact struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	ModTime   string `json:"modified_at"`
	// Downloadable is true only for an ENCRYPTED database dump (audit AD-9): every other artefact is listed but cannot be
	// downloaded, and a plain .sql.gz is refused whatever its age.
	Downloadable bool `json:"downloadable"`
	// Encrypted says whether a database dump is encrypted to the key holders. False on a database dump means a plain
	// earlier backup: it is flagged in the console, never offered for download, and can be replaced by an encrypted one.
	Encrypted bool `json:"encrypted"`
	// DownloadRefusal explains, for the console, why Downloadable is false.
	DownloadRefusal string `json:"download_refusal,omitempty"`

	// Verification state is read from the file on disk, not remembered by the browser; it goes stale when the
	// archive changes. scd enforces the same rule before restoring.
	VerifiedAt     string `json:"verified_at,omitempty"`
	VerifiedTables int    `json:"verified_tables,omitempty"`

	// The note beside a backup. A backup with no note is an earlier one: listed, restorable if verified, never removed automatically.
	BackupKind string `json:"backup_kind,omitempty"` // scheduled | manual | pre-change | pre-restore | earlier
	Reason     string `json:"reason,omitempty"`
	By         string `json:"by,omitempty"`
	Restorable bool   `json:"restorable,omitempty"`
	LedgerSeq  int64  `json:"ledger_seq,omitempty"`
}

type backupNote struct {
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	By         string `json:"by"`
	Restorable bool   `json:"restorable"`
	LedgerSeq  int64  `json:"ledger_seq"`
}

func readBackupNote(dir, name string) (backupNote, bool) {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".age"), ".sql.gz")
	b, err := os.ReadFile(filepath.Join(dir, base+".meta.json"))
	if err != nil {
		return backupNote{}, false
	}
	var n backupNote
	if json.Unmarshal(b, &n) != nil || n.Kind == "" {
		return backupNote{}, false
	}
	return n, true
}

// verifiedRecord mirrors the marker scd writes beside a verified archive; edged only reports it.
type verifiedRecord struct {
	VerifiedAt time.Time `json:"verified_at"`
	SizeBytes  int64     `json:"size_bytes"`
	ModTime    time.Time `json:"mod_time"`
	Tables     int       `json:"tables"`
	SHA256     string    `json:"sha256"`
}

// readVerification returns the marker only when the archive on disk is byte for byte the one verified. It
// digests the file: size/mtime can be set back, so they alone are not an integrity check. The digest is reused
// only while the file's identity (device, inode, size, mtime AND ctime, which no write can set back) is unchanged,
// so refreshing the list no longer re-reads every dump (audit P9 L8). A marker with no digest is not verified.
func readVerification(dir, name string) (*verifiedRecord, bool) {
	b, err := os.ReadFile(filepath.Join(dir, name+".verified"))
	if err != nil {
		return nil, false
	}
	var rec verifiedRecord
	if json.Unmarshal(b, &rec) != nil || rec.SHA256 == "" {
		return nil, false
	}
	full := filepath.Join(dir, name)
	fi, err := os.Stat(full)
	if err != nil || fi.Size() != rec.SizeBytes || !fi.ModTime().UTC().Equal(rec.ModTime) {
		return nil, false
	}
	sum, ok := backupDigests.digest(full, fi)
	if !ok || sum != rec.SHA256 {
		return nil, false
	}
	return &rec, true
}

// digestCache remembers each archive's SHA-256 against the identity of the file it was computed from.
type digestCache struct {
	mu sync.Mutex
	m  map[string]digestEntry
}

type digestEntry struct {
	id  fileIdentity
	sum string
}

var backupDigests = &digestCache{m: map[string]digestEntry{}}

// digestFiles counts full digests computed; tests read it.
var digestFiles atomic.Int64

func (c *digestCache) digest(path string, fi os.FileInfo) (string, bool) {
	id, known := identityOf(fi)
	if known {
		c.mu.Lock()
		e, hit := c.m[path]
		c.mu.Unlock()
		if hit && e.id == id {
			return e.sum, true
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	digestFiles.Add(1)
	sum := hex.EncodeToString(h.Sum(nil))
	if known {
		// Re-check after reading: a file changed while it was read is not cached.
		if after, err := os.Stat(path); err == nil {
			if id2, ok := identityOf(after); ok && id2 == id {
				c.mu.Lock()
				c.m[path] = digestEntry{id: id, sum: sum}
				c.mu.Unlock()
			}
		}
	}
	return sum, true
}

func artifactKind(name string, dir bool) string {
	switch {
	case strings.HasPrefix(name, "db-"):
		return "database"
	case strings.HasPrefix(name, "deploy-"), strings.HasPrefix(name, "testdeploy-"):
		return "deployment rollback"
	case strings.HasPrefix(name, "gatep-"):
		return "privilege configuration"
	case dir:
		return "directory"
	default:
		return "archive"
	}
}

var reDBBackupFile = regexp.MustCompile(`^db-(safety-)?[0-9TZ]+\.sql\.gz(\.age)?$`)

// downloadPolicy is the one rule for what leaves the appliance (audit AD-9, Product-Owner decision): only a database
// dump (db-*) can be downloaded, and only when it is encrypted. Deployment, privilege-configuration and other artefacts
// are listed but not downloadable; an unencrypted database dump is refused outright.
func downloadPolicy(name string, dir bool) (downloadable, encrypted bool, refusal string) {
	encrypted = strings.HasSuffix(name, ".age")
	switch {
	case dir:
		return false, false, "This is a folder of deployment files and cannot be downloaded."
	case !strings.HasPrefix(name, "db-") || !reDBBackupFile.MatchString(name):
		return false, false, "Only database backups can be downloaded. This file is kept on the appliance."
	case !encrypted:
		return false, false, "This database backup is not encrypted, so it cannot be downloaded. Take a new backup (it is encrypted to your backup key holders) and download that one."
	}
	return true, true, ""
}

func (s *server) listBackupArtifacts(w http.ResponseWriter, r *http.Request) {
	out := []backupArtifact{}
	add := func(base string, e os.DirEntry) {
		fi, err := e.Info()
		if err != nil {
			return
		}
		// Markers and metadata files are bookkeeping, not artefacts.
		if strings.HasSuffix(e.Name(), ".verified") || strings.HasSuffix(e.Name(), ".meta.json") || strings.HasSuffix(e.Name(), ".partial") {
			return
		}
		a := backupArtifact{
			Name:         e.Name(),
			Kind:         artifactKind(e.Name(), e.IsDir()),
			SizeBytes:    fi.Size(),
			ModTime:      fi.ModTime().UTC().Format(time.RFC3339),
		}
		a.Downloadable, a.Encrypted, a.DownloadRefusal = downloadPolicy(e.Name(), e.IsDir())
		if rec, ok := readVerification(base, e.Name()); ok {
			a.VerifiedAt = rec.VerifiedAt.Format(time.RFC3339)
			a.VerifiedTables = rec.Tables
		}
		if a.Kind == "database" && base == dbBackupDir && reDBBackupFile.MatchString(e.Name()) {
			if n, ok := readBackupNote(base, e.Name()); ok {
				a.BackupKind, a.Reason, a.By, a.Restorable, a.LedgerSeq = n.Kind, n.Reason, n.By, n.Restorable, n.LedgerSeq
			} else {
				a.BackupKind = "earlier"
			}
		}
		out = append(out, a)
	}
	if entries, err := os.ReadDir(backupRoot); err == nil {
		for _, e := range entries {
			if e.Name() == "db" {
				continue // listed below, with its own kind
			}
			add(backupRoot, e)
		}
	}
	if entries, err := os.ReadDir(dbBackupDir); err == nil {
		for _, e := range entries {
			add(dbBackupDir, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })
	writeList(w, out)
}

// resolveArtifact maps a caller-supplied name onto a real file: it must match safeArtifact and the resolved path must stay inside the backup tree.
func resolveArtifact(name string) (string, bool) {
	if !safeArtifact.MatchString(name) {
		return "", false
	}
	for _, dir := range []string{dbBackupDir, backupRoot} {
		p := filepath.Join(dir, name)
		abs, err := filepath.Abs(p)
		if err != nil || !strings.HasPrefix(abs, backupRoot+string(os.PathSeparator)) {
			continue
		}
		// Lstat, not Stat: a symlink in the backup tree must not serve whatever it points at (audit P9 L7).
		if fi, err := os.Lstat(abs); err == nil && fi.Mode().IsRegular() {
			return abs, true
		}
	}
	return "", false
}

type runBackupReq struct {
	Password string `json:"password"`
}

// runDatabaseBackup requires step-up even though it destroys nothing: the artefact is a complete copy of the
// site's data, so producing one is a privileged, attributed act.
func (s *server) runDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	var in runBackupReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	// The dump runs in scd: PostgreSQL is in a container, so it needs the Docker socket (effectively root),
	// which edged deliberately does not have. Authorization, step-up and audit stay here.
	// No public.backup_records row is written: neither service role can insert into it; the artefacts are the history.
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/run",
		map[string]any{"kind": "manual", "by": protectionActor(sessFrom(r.Context()))})
	if err == nil && code == http.StatusConflict {
		// No key holder yet: backups are always encrypted, so none was taken.
		detail := scdDetail(body, nil)
		s.audit(r, "backup.refused", "backup", "", map[string]any{"error": detail})
		jsonErr(w, http.StatusConflict, "no_key_holder", detail)
		return
	}
	if err != nil || code != http.StatusOK {
		detail := scdDetail(body, err)
		s.audit(r, "backup.failed", "backup", "", map[string]any{"error": detail})
		jsonErr(w, http.StatusInternalServerError, "backup_failed",
			"the database backup did not complete: "+detail)
		return
	}
	var res struct {
		Name       string `json:"name"`
		SizeBytes  int64  `json:"size_bytes"`
		Restorable bool   `json:"restorable"`
	}
	if jerr := json.Unmarshal(body, &res); jerr != nil || res.Name == "" {
		jsonErr(w, http.StatusInternalServerError, "backup_failed",
			"the backup completed but the appliance did not report its name")
		return
	}
	name, size := res.Name, res.SizeBytes
	// The audit log is the record of who took it and when.
	s.audit(r, "backup.created", "backup", name, map[string]any{"size_bytes": size, "kind": "manual", "restorable": res.Restorable})
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "size_bytes": size, "restorable": res.Restorable})
}

type verifyResult struct {
	OK       bool   `json:"ok"`
	Tables   int    `json:"tables"`
	Detail   string `json:"detail,omitempty"`
	Duration string `json:"duration"`
}

// verifyBackupArtifact loads the dump into a throwaway database; the live site is not read, written or locked.
func (s *server) verifyBackupArtifact(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, ok := resolveArtifact(name); !ok || !strings.HasPrefix(name, "db-") {
		jsonErr(w, http.StatusNotFound, "not_found", "no such database backup")
		return
	}
	// An encrypted (.age) backup is opened with a key holder's key passed to scd for this call only; the body is optional.
	var in struct {
		Identity string `json:"identity"`
	}
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10)); len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
	}
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/verify",
		map[string]any{"name": name, "identity": in.Identity})
	if err == nil && code == http.StatusBadRequest {
		jsonErr(w, http.StatusBadRequest, "backup_key", scdDetail(body, nil))
		return
	}
	if err != nil || code != http.StatusOK {
		jsonErr(w, http.StatusInternalServerError, "verify_failed", scdDetail(body, err))
		return
	}
	var res verifyResult
	if jerr := json.Unmarshal(body, &res); jerr != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the verification result could not be read")
		return
	}
	s.audit(r, "backup.verified", "backup", name, map[string]any{"ok": res.OK, "tables": res.Tables})
	writeJSON(w, http.StatusOK, res)
}

func scdDetail(body []byte, err error) string {
	if err != nil {
		return "the appliance service did not respond: " + err.Error()
	}
	var e struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if e.Error != "" {
			return e.Error
		}
	}
	return lastLine(string(body))
}

// lastLine returns the final non-empty line of command output.
func lastLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	last := ""
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			last = t
		}
	}
	if last == "" {
		return "no detail reported"
	}
	if len(last) > 300 {
		return last[:300] + "…"
	}
	return last
}

// restoreDatabaseBackup handles who may restore (permission, step-up, audit); the mechanics are scd's (cmd/scd/restore.go).
// The audit is written first on purpose: a restore replaces the audit table, so a later record would land in
// restored data, whereas this one is captured by scd's safety backup.
func (s *server) restoreDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, ok := resolveArtifact(name); !ok || !strings.HasPrefix(name, "db-") {
		jsonErr(w, http.StatusNotFound, "not_found", "no such database backup")
		return
	}
	var in struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
		// Key holder's key for an encrypted backup; passed to scd for this restore only.
		Identity string `json:"identity"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	// Two confirmations: the password proves who asks; the typed backup name proves which backup.
	if in.Confirm != name {
		jsonErr(w, http.StatusBadRequest, "confirm_mismatch",
			"type the name of the backup you are restoring to confirm")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r,
			"confirm your password. Restoring replaces this site's data with the contents of the backup.")
		return
	}

	s.audit(r, "backup.restore_started", "backup", name, map[string]any{"backup": name})

	// This call starts the restore and does not wait: a restore can take 45 minutes and a timed-out call would
	// report "failed" while it still runs, inviting a second one. scd answers 202 and records the outcome durably
	// (polled via /last-restore, which survives edged's restart). No restore_succeeded audit entry is written
	// here because the swap replaces the audit table.
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/restore",
		map[string]any{"name": name, "identity": in.Identity, "by": protectionActor(sessFrom(r.Context()))})
	if err == nil && code == http.StatusBadRequest {
		// The key did not open the backup (or none was given); scd checks before touching anything.
		s.audit(r, "backup.restore_refused", "backup", name, map[string]any{"detail": scdDetail(body, nil)})
		jsonErr(w, http.StatusBadRequest, "backup_key", scdDetail(body, nil))
		return
	}
	if err != nil || (code != http.StatusOK && code != http.StatusAccepted) {
		detail := scdDetail(body, err)
		s.audit(r, "backup.restore_failed", "backup", name, map[string]any{"detail": detail})
		jsonErr(w, http.StatusBadGateway, "restore_failed", detail)
		return
	}
	var out map[string]any
	if jerr := json.Unmarshal(body, &out); jerr != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the restore result could not be read")
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// backupMaintenance reports whether the appliance is withholding service (for example mid-restore).
func (s *server) backupMaintenance(w http.ResponseWriter, r *http.Request) {
	code, body, err := s.scd.call(r.Context(), http.MethodGet, "/v1/maintenance", nil)
	if err != nil || code != http.StatusOK {
		// scd being unreachable is not proof of maintenance.
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "unknown": true})
		return
	}
	var out map[string]any
	if json.Unmarshal(body, &out) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "unknown": true})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// lastRestoreResult serves the durable outcome of the last restore, which survives service restarts including edged's.
func (s *server) lastRestoreResult(w http.ResponseWriter, r *http.Request) {
	code, body, err := s.scd.call(r.Context(), http.MethodGet, "/v1/backup/last-restore", nil)
	if err != nil || code != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{"present": false})
		return
	}
	var out map[string]any
	if json.Unmarshal(body, &out) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"present": false})
		return
	}
	writeJSON(w, http.StatusOK, out)
}
