package main

// BACKUPS, AS AN OPERATOR FEATURE RATHER THAN AN EMPTY VIEWER.
//
// WHAT WAS THERE, AND WHY IT WAS EMPTY
// ------------------------------------
// The Backups screen listed public.backup_records. That table has never had a row written to it on this
// appliance -- nothing produces one -- so the screen showed "No backups yet" forever while the appliance was
// in fact retaining 444 MB of deploy and rollback artefacts under /opt/stayconnect/backups and running a
// daily retention sweep that already knew its disk pressure, its protected paths and what it had reclaimed.
// All of that was real, and none of it was reachable from Hotel Admin.
//
// So this does not invent a backup subsystem. It surfaces the one that exists, and adds the one thing the
// appliance genuinely lacked: a database backup an operator can take, download and verify.
//
// WHAT IS DELIBERATELY NOT HERE
// -----------------------------
// There is no live restore. Restoring a database over a running appliance is destructive and irreversible in
// the direction that matters, and the mission forbids proving a UI with one. What is offered instead is
// VERIFICATION: a dump is restored into a disposable scratch database and reported on, which answers the only
// question an operator actually has about a backup -- "would this work?" -- without betting the site on it.
// The restore runbook is presented alongside, so the path exists and is explicit.

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
	"time"

	"github.com/go-chi/chi/v5"
)

// backupRoot is where the appliance already keeps its artefacts; the retention sweep owns the same tree. A var
// only so the download tests can point it at a temporary directory; nothing in the product assigns it.
var backupRoot = "/opt/stayconnect/backups"

// retentionStatusPath is written by stayconnect-backup-cleanup on every run. It is the appliance's OWN
// account of its retention posture -- reading it is why this screen can report real numbers instead of
// plausible ones.
const retentionStatusPath = "/opt/stayconnect/backup-retention-status.json"

// dbBackupDir keeps database dumps separate from deploy/rollback artefacts. The retention sweep already
// carries a keep_db policy for exactly this category; until now nothing ever produced one.
var dbBackupDir = filepath.Join(backupRoot, "db")

// safeArtifact bounds what a caller may name. Artefact names come from a listing WE produced, but a handler
// that joins a caller-supplied string onto a path is one typo away from serving /etc/shadow, so the name is
// constrained to a single path element of known shape rather than merely checked for "..".
var safeArtifact = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *server) backupsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", s.backupHealth)
	r.Get("/settings", s.backupSettingsGet)
	r.Put("/settings", s.backupSettingsSet)
	r.Get("/artifacts", s.listBackupArtifacts)
	r.Post("/run", s.runDatabaseBackup)
	// Download is NOT here any more: it is the backup-download resource (backup_download.go), site_admin and
	// the IT manager only, behind the password step-up (decision D-B).
	r.Post("/artifacts/{name}/verify", s.verifyBackupArtifact)
	// THE DESTRUCTIVE ONE. Password step-up and a typed confirmation inside the handler; see
	// restoreDatabaseBackup. Only a backup that has passed Verify is accepted, enforced again by scd.
	r.Post("/artifacts/{name}/restore", s.restoreDatabaseBackup)
	r.Get("/maintenance", s.backupMaintenance)
	r.Get("/last-restore", s.lastRestoreResult)
	// THE ERASURE LEDGER AND THE BACKUP STATUS (ledger_admin.go).
	r.Get("/ledger", s.ledgerStatusGet)
	r.Post("/ledger/destination/probe", s.ledgerDestinationProbe)
	r.Post("/ledger/destination/test", s.ledgerDestinationTest)
	r.Put("/ledger/destination", s.ledgerDestinationSave)
	r.Post("/ledger/recover", s.ledgerRecover)
	r.Get("/status", s.backupStatusGet)
	r.Post("/earlier/remove", s.earlierBackupsRemove)
	return r
}

// ------------------------------------------------------------------------------- retention and schedule

// Retention and the nightly schedule are SETTINGS (§0C). scd owns the files -- /etc/stayconnect and the
// systemd drop-in are root's -- so these proxy, and everything that decides whether the change may happen
// stays here: the RBAC mount, the step-up below, and the audit record.
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
	// Step-up: retention decides how far back this appliance can recover, and a schedule that never fires is
	// indistinguishable from a backup policy that works right up until it is needed.
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
		// The appliance refuses with a reason an operator can act on -- a bound, or the warning/critical
		// ordering -- so it is passed through rather than flattened into "invalid".
		jsonErr(w, http.StatusBadRequest, "validation", scdDetail(body, nil))
		return
	}
	s.audit(r, "backup.settings_changed", "backup", "retention",
		map[string]any{"retention": in.Retention, "schedule": in.Schedule})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// ---------------------------------------------------------------------------------------------- health

type backupHealth struct {
	// Retention is the appliance's own status document, passed through rather than re-derived. A second
	// opinion computed here could disagree with the sweep that actually deletes things.
	Retention map[string]any `json:"retention,omitempty"`
	// RetentionReadable says whether the document above could be read at all, so the UI can distinguish
	// "the sweep reports no failures" from "I could not ask".
	RetentionReadable bool   `json:"retention_readable"`
	RetentionError    string `json:"retention_error,omitempty"`

	TimerActive   bool   `json:"timer_active"`
	TimerNextRun  string `json:"timer_next_run,omitempty"`
	TimerLastRun  string `json:"timer_last_run,omitempty"`
	TimerReadable bool   `json:"timer_readable"`

	// DatabaseBackups is the count and newest timestamp of the category an operator most wants and which
	// this appliance has never had. Reported explicitly so "none yet" is a statement, not an absence.
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

	// systemd is the authority on whether the sweep is actually scheduled. A status document from last week
	// looks healthy on its own; the timer is what says it will run again.
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

// ------------------------------------------------------------------------------------------- artefacts

type backupArtifact struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	ModTime   string `json:"modified_at"`
	// Downloadable is false for directories: a deploy rollback set is a tree, and offering a download that
	// cannot be produced is worse than not offering one.
	Downloadable bool `json:"downloadable"`

	// VERIFICATION STATE, READ FROM THE APPLIANCE rather than remembered by the browser.
	//
	// Restore eligibility depends on it, so it has to be a fact about the file on disk and not a flag the
	// screen set after a successful verify. A page reloaded, opened in a second tab, or opened by a different
	// operator must reach the same answer -- and the answer must go stale the moment the archive changes,
	// which is what the marker's size/mtime check gives. scd enforces the same rule again before it restores
	// anything; this is what lets the screen explain WHY the button is unavailable instead of just disabling it.
	VerifiedAt     string `json:"verified_at,omitempty"`
	VerifiedTables int    `json:"verified_tables,omitempty"`

	// The note beside a backup the one engine made: why it exists, who asked and what it holds. A backup with no note is an
	// earlier backup (before there was one system); it is listed, restorable if it was verified, and never removed automatically.
	BackupKind string `json:"backup_kind,omitempty"` // scheduled | manual | pre-change | pre-restore | earlier
	Reason     string `json:"reason,omitempty"`
	By         string `json:"by,omitempty"`
	Restorable bool   `json:"restorable,omitempty"`
	LedgerSeq  int64  `json:"ledger_seq,omitempty"`
}

// backupNote is the part of scd's note edged reports. Read-only.
type backupNote struct {
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	By         string `json:"by"`
	Restorable bool   `json:"restorable"`
	LedgerSeq  int64  `json:"ledger_seq"`
}

// readBackupNote reads the note of a database backup by name; ok is false for a backup with none.
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

// verifiedRecord mirrors the marker scd writes beside a verified archive. Read-only here: edged reports
// verification, it does not confer it.
type verifiedRecord struct {
	VerifiedAt time.Time `json:"verified_at"`
	SizeBytes  int64     `json:"size_bytes"`
	ModTime    time.Time `json:"mod_time"`
	Tables     int       `json:"tables"`
	SHA256     string    `json:"sha256"`
}

// readVerification returns the marker only when the archive on disk is, byte for byte, the one that was
// verified.
//
// IT RE-READS THE FILE. Size and modification time are metadata: anybody who can change the file can change
// them back, so a check built on them is a change NOTICE, not an integrity check. The screen must not show
// "Verified" for content that no longer matches -- an operator deciding whether to replace a hotel's data
// should not be reading a claim that only holds while nobody is adversarial or unlucky.
//
// A marker with no digest is not verified: it was written before content checks existed and there is no way
// to know what it vouched for.
//
// The cost is honest and bounded. Only artefacts that CLAIM verification are digested -- on the appliance
// two of seven -- at roughly 250ms per 27 MB archive, on a page an operator opens to answer "could we
// recover from this?". There is deliberately no cache keyed on size and modification time, because that is
// the exact signal this replaced.
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
	f, err := os.Open(full)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, false
	}
	if hex.EncodeToString(h.Sum(nil)) != rec.SHA256 {
		return nil, false
	}
	return &rec, true
}

// artifactKind names what an artefact IS in the operator's terms, from the naming the appliance already uses.
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

func (s *server) listBackupArtifacts(w http.ResponseWriter, r *http.Request) {
	out := []backupArtifact{}
	add := func(base string, e os.DirEntry) {
		fi, err := e.Info()
		if err != nil {
			return
		}
		// A verification marker is bookkeeping, not an artefact. Listing it would put a second entry beside
		// every verified backup that an operator could neither download nor restore.
		if strings.HasSuffix(e.Name(), ".verified") || strings.HasSuffix(e.Name(), ".meta.json") || strings.HasSuffix(e.Name(), ".partial") {
			return
		}
		a := backupArtifact{
			Name:         e.Name(),
			Kind:         artifactKind(e.Name(), e.IsDir()),
			SizeBytes:    fi.Size(),
			ModTime:      fi.ModTime().UTC().Format(time.RFC3339),
			Downloadable: !e.IsDir(),
		}
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

// resolveArtifact maps a caller-supplied name onto a real file, or refuses. It never joins the raw name onto
// a directory: the name must match safeArtifact AND the resolved path must still sit inside the backup tree.
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
		if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
			return abs, true
		}
	}
	return "", false
}

// ------------------------------------------------------------------------------------- taking a backup

type runBackupReq struct {
	Password string `json:"password"`
}

// runDatabaseBackup takes a real pg_dump of the site database and records it.
//
// STEP-UP IS REQUIRED even though a backup destroys nothing: the artefact it produces is a complete copy of
// the site's data, and the download endpoint (backup_download.go) will hand it to whoever may download next. Producing one is
// therefore a privileged act, and it is attributed.
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
	// NAME THE PATH AND THE REASON. edged runs as the unprivileged `stayconnect` user while
	// /opt/stayconnect/backups is root-owned, so on an appliance where the database directory has not been
	// provisioned this fails -- and "the backup directory could not be created" sent the first operator who
	// hit it looking at disk space. The directory is created by deployment, owned by the service user; if it
	// is missing, say exactly that.
	// THE DUMP ITSELF IS PRIVILEGED AND HAPPENS IN scd.
	//
	// PostgreSQL runs in a container and there is no pg_dump on the host, so taking a dump means reaching the
	// Docker socket -- and edged runs as the unprivileged service user precisely so that it cannot. Docker
	// group membership is effectively root; granting it to the process serving the operator HTTP surface
	// would hand root to anything that got a foothold here. scd already runs as root and is already the
	// appliance's privileged helper for certificates, licensing and PMS administration, so a backup takes the
	// same established route instead of opening a second one.
	//
	// Everything that decides WHETHER this may happen stays on this side: the RBAC mount, the password
	// step-up above, the audit record and the backup ledger below.
	// NO LEDGER ROW IS WRITTEN, and that is a finding rather than an omission.
	//
	// public.backup_records exists and the operator surface used to list it, but NEITHER service role can
	// write to it: svc_edged holds SELECT only and svc_scd holds nothing at all. Nothing has ever inserted a
	// row, which is why the screen read "No backups yet" forever. Granting INSERT is a migration, and this
	// mission does not authorise one.
	//
	// So the history is the ARTEFACTS -- which are real, enumerable, and the thing an operator actually
	// restores from. Adding a second permanently-empty viewer beside the first would have repeated the defect
	// rather than fixed it.
	// ONE ENGINE: the same encrypted, verified backup the schedule takes, asked for by a person.
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/run",
		map[string]any{"kind": "manual", "by": protectionActor(sessFrom(r.Context()))})
	if err == nil && code == http.StatusConflict {
		// No key holder yet: a backup is always encrypted, so none was taken. Say so plainly.
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
	// The audit log IS the record of who took it and when -- written through the same path as every other
	// privileged operator action, and readable on the Audit screen.
	s.audit(r, "backup.created", "backup", name, map[string]any{"size_bytes": size, "kind": "manual", "restorable": res.Restorable})
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "size_bytes": size, "restorable": res.Restorable})
}

// ------------------------------------------------------------------------------------------ verifying

type verifyResult struct {
	OK       bool   `json:"ok"`
	Tables   int    `json:"tables"`
	Detail   string `json:"detail,omitempty"`
	Duration string `json:"duration"`
}

// verifyBackupArtifact answers "would this restore?" WITHOUT restoring anything an operator depends on.
//
// The dump is loaded into a throwaway database created for the purpose and dropped immediately afterwards.
// Nothing about the live site is read, written or locked. This is the whole reason there is no "restore"
// button here: the question an operator has is whether the backup is good, and that can be answered safely,
// whereas a live restore cannot be undone by clicking again.
func (s *server) verifyBackupArtifact(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, ok := resolveArtifact(name); !ok || !strings.HasPrefix(name, "db-") {
		jsonErr(w, http.StatusNotFound, "not_found", "no such database backup")
		return
	}
	// AN ENCRYPTED BACKUP (.age, migration 0129) is opened with a backup key holder's key, sent with this
	// request and passed to scd for it alone. The body is optional: a backup in the clear needs none.
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

// scdDetail turns a privileged-helper failure into one actionable sentence rather than a transport error.
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

// ------------------------------------------------------------------------------------------- plumbing

// shellQuote makes a path safe to embed in the one place this file uses a shell -- the dump pipeline, which
// needs a pipe. Paths here are constructed, never caller-supplied, but a quoting helper that exists is one
// that gets used when that stops being true.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// lastLine returns the final non-empty line of command output: the part that says what went wrong, without
// the pages of context that precede it.
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

// ------------------------------------------------------------------ RESTORE, THE AUTHORISED HALF --------

// restoreDatabaseBackup is the operator-facing door to the one destructive operation in the product.
//
// WHAT THIS SIDE IS RESPONSIBLE FOR. Everything about WHO: the permission, the password step-up and the
// audit record. The mechanics -- safety backup, staging load, rename swap, integrity check, rollback -- are
// scd's, over its private socket. See cmd/scd/restore.go.
//
// THE AUDIT IS WRITTEN FIRST, DELIBERATELY. A restore replaces the database, including the audit table, so a
// record written afterwards lands in restored data and describes an event that data has never seen. Written
// first, it is captured by the safety backup scd takes -- which is the copy that would be used if anything
// went wrong, and therefore the copy that most needs to say what was attempted.
func (s *server) restoreDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, ok := resolveArtifact(name); !ok || !strings.HasPrefix(name, "db-") {
		jsonErr(w, http.StatusNotFound, "not_found", "no such database backup")
		return
	}
	var in struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
		// A backup key holder's key, for an encrypted backup. Passed to scd for this restore only.
		Identity string `json:"identity"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	// TWO INDEPENDENT CONFIRMATIONS, because one is a click and this is not a click-sized decision. The
	// password proves who is asking; typing the backup's own name proves they know WHICH backup they chose.
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

	// THIS CALL STARTS THE RESTORE; IT DOES NOT WAIT FOR IT.
	//
	// It used to wait, and on the appliance that produced the worst available outcome: the socket call gave up
	// after fifteen seconds and this handler told the operator the restore had FAILED, while scd went on and
	// finished it successfully ten seconds later. Someone reading "failed" about a destructive operation that
	// is still running is one keystroke away from starting a second one -- and scd's deliberate refusal to
	// retry destructive steps is worth nothing if the screen invites the human to do the retrying.
	//
	// A restore can take 45 minutes; no HTTP client, proxy or browser tab should be load-bearing for that
	// long. scd answers 202 immediately and records the outcome durably. The screen polls /last-restore, which
	// already had to survive the edged restart the restore itself performs.
	//
	// Consequently there is no restore_succeeded audit entry written from here. There could not be an honest
	// one: the swap replaces the audit table, so a row written after it either lands in restored data or
	// describes an outcome this process never saw. The durable record in scd is the outcome; restore_started,
	// written above and captured by the safety backup, is what the audit trail can truthfully hold.
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/restore",
		map[string]any{"name": name, "identity": in.Identity, "by": protectionActor(sessFrom(r.Context()))})
	if err == nil && code == http.StatusBadRequest {
		// The key did not open the backup (or none was given): checked by scd before anything was touched.
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

// backupMaintenance reports whether the appliance is currently withholding service, and why.
//
// Read by the Backups screen so an operator who reloads mid-restore is told what is happening rather than
// meeting a wall of failed requests.
func (s *server) backupMaintenance(w http.ResponseWriter, r *http.Request) {
	code, body, err := s.scd.call(r.Context(), http.MethodGet, "/v1/maintenance", nil)
	if err != nil || code != http.StatusOK {
		// scd being unreachable is not proof of maintenance, and claiming it would be its own false alarm.
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

// lastRestoreResult serves the durable outcome of the most recent restore, which survives the service
// restarts a restore performs -- including edged's own.
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
