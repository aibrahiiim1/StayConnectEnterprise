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
	"encoding/json"
	"fmt"
	"log/slog"
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

// backupRoot is where the appliance already keeps its artefacts; the retention sweep owns the same tree.
const backupRoot = "/opt/stayconnect/backups"

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
	r.Get("/", s.listBackupRecords)
	r.Get("/health", s.backupHealth)
	r.Get("/artifacts", s.listBackupArtifacts)
	r.Post("/run", s.runDatabaseBackup)
	r.Get("/artifacts/{name}/download", s.downloadBackupArtifact)
	r.Post("/artifacts/{name}/verify", s.verifyBackupArtifact)
	return r
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
			if e.IsDir() {
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

func (s *server) listBackupArtifacts(w http.ResponseWriter, r *http.Request) {
	out := []backupArtifact{}
	add := func(base string, e os.DirEntry) {
		fi, err := e.Info()
		if err != nil {
			return
		}
		out = append(out, backupArtifact{
			Name:         e.Name(),
			Kind:         artifactKind(e.Name(), e.IsDir()),
			SizeBytes:    fi.Size(),
			ModTime:      fi.ModTime().UTC().Format(time.RFC3339),
			Downloadable: !e.IsDir(),
		})
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

func (s *server) downloadBackupArtifact(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path, ok := resolveArtifact(name)
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "no such backup artifact")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the artifact could not be opened")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the artifact could not be measured")
		return
	}
	s.audit(r, "backup.downloaded", "backup", name, map[string]any{"size_bytes": fi.Size()})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// ------------------------------------------------------------------------------------- taking a backup

type runBackupReq struct {
	Password string `json:"password"`
}

// runDatabaseBackup takes a real pg_dump of the site database and records it.
//
// STEP-UP IS REQUIRED even though a backup destroys nothing: the artefact it produces is a complete copy of
// the site's data, and the download endpoint above will hand it to whoever asks next. Producing one is
// therefore a privileged act, and it is attributed.
func (s *server) runDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	var in runBackupReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	if err := os.MkdirAll(dbBackupDir, 0o750); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the backup directory could not be created")
		return
	}

	started := time.Now().UTC()
	name := "db-" + started.Format("20060102T150405Z") + ".sql.gz"
	dest := filepath.Join(dbBackupDir, name)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()

	// Written to a .partial first and renamed only on success, so a failed or interrupted dump can never be
	// left behind looking like a backup somebody could restore from.
	partial := dest + ".partial"
	id, recErr := s.recordBackupStart(ctx, "database", dest)
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c",
		"docker exec stayconnect-pg pg_dump -U stayconnect -d stayconnect_site --no-owner --no-privileges"+
			" | gzip -c > "+shellQuote(partial)).CombinedOutput()
	if err != nil {
		_ = os.Remove(partial)
		detail := lastLine(string(out))
		s.finishBackupRecord(ctx, id, "failed", 0, detail)
		s.audit(r, "backup.failed", "backup", name, map[string]any{"error": detail})
		jsonErr(w, http.StatusInternalServerError, "backup_failed",
			"the database backup did not complete: "+detail)
		return
	}
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		s.finishBackupRecord(ctx, id, "failed", 0, "the completed dump could not be moved into place")
		jsonErr(w, http.StatusInternalServerError, "internal", "the completed dump could not be moved into place")
		return
	}
	var size int64
	if fi, err := os.Stat(dest); err == nil {
		size = fi.Size()
	}
	// A zero-length or trivially small dump is a failure wearing a success's clothes -- gzip of nothing still
	// exits 0. Refused rather than recorded as a backup.
	if size < 1024 {
		_ = os.Remove(dest)
		s.finishBackupRecord(ctx, id, "failed", size, "the dump was empty")
		jsonErr(w, http.StatusInternalServerError, "backup_failed", "the dump was empty and has been discarded")
		return
	}
	s.finishBackupRecord(ctx, id, "ok", size, "")
	s.audit(r, "backup.created", "backup", name, map[string]any{"size_bytes": size, "kind": "database"})
	if recErr != nil {
		slog.Warn("backup taken but not recorded in backup_records", "name", name, "err", recErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "size_bytes": size, "started_at": started})
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
	path, ok := resolveArtifact(name)
	if !ok || !strings.HasPrefix(name, "db-") {
		jsonErr(w, http.StatusNotFound, "not_found", "no such database backup")
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()

	scratch := "verify_" + time.Now().UTC().Format("20060102150405")
	run := func(sql string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", "exec", "stayconnect-pg",
			"psql", "-U", "stayconnect", "-d", "postgres", "-tAc", sql).CombinedOutput()
	}
	if out, err := run("CREATE DATABASE " + scratch); err != nil {
		jsonErr(w, http.StatusInternalServerError, "verify_failed",
			"a scratch database could not be created: "+lastLine(string(out)))
		return
	}
	// Dropped on every path, including the failure paths below.
	defer func() { _, _ = run("DROP DATABASE IF EXISTS " + scratch + " WITH (FORCE)") }()

	load := exec.CommandContext(ctx, "/bin/sh", "-c",
		"gzip -dc "+shellQuote(path)+" | docker exec -i stayconnect-pg psql -U stayconnect -d "+scratch+" -v ON_ERROR_STOP=1 -q")
	if out, err := load.CombinedOutput(); err != nil {
		s.audit(r, "backup.verify_failed", "backup", name, map[string]any{"detail": lastLine(string(out))})
		writeJSON(w, http.StatusOK, verifyResult{
			OK: false, Detail: "the dump did not load: " + lastLine(string(out)),
			Duration: time.Since(started).Round(time.Second).String(),
		})
		return
	}
	// A dump that loads but contains nothing is not a usable backup. Counting tables is the cheapest
	// assertion that distinguishes "restored" from "restored something".
	cnt, err := exec.CommandContext(ctx, "docker", "exec", "stayconnect-pg",
		"psql", "-U", "stayconnect", "-d", scratch, "-tAc",
		"SELECT count(*) FROM information_schema.tables WHERE table_schema IN ('public','iam_v2')").Output()
	tables := 0
	if err == nil {
		fmt.Sscanf(strings.TrimSpace(string(cnt)), "%d", &tables)
	}
	res := verifyResult{
		OK:       tables > 0,
		Tables:   tables,
		Duration: time.Since(started).Round(time.Second).String(),
	}
	if !res.OK {
		res.Detail = "the dump loaded but produced no tables"
	}
	s.audit(r, "backup.verified", "backup", name, map[string]any{"ok": res.OK, "tables": tables})
	writeJSON(w, http.StatusOK, res)
}

// ------------------------------------------------------------------------------------------- plumbing

func (s *server) recordBackupStart(ctx context.Context, kind, path string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO backup_records (started_at, status, kind, path) VALUES (now(),'running',$1,$2) RETURNING id`,
		kind, path).Scan(&id)
	return id, err
}

func (s *server) finishBackupRecord(ctx context.Context, id int64, status string, size int64, errText string) {
	if id == 0 {
		return
	}
	var e *string
	if errText != "" {
		e = &errText
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE backup_records SET finished_at=now(), status=$2, size_bytes=$3, error=$4 WHERE id=$1`,
		id, status, size, e); err != nil {
		slog.Warn("backup record not finished", "id", id, "err", err)
	}
}

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
