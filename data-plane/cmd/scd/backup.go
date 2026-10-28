package main

// Taking and verifying a database backup, the privileged half: Postgres runs in a container and only root scd may reach Docker.
// edged does the step-up, RBAC and audit before calling these; the endpoints are on scd's private socket.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// scdBackupDir is a var so tests can point it at a temporary directory.
var scdBackupDir = "/opt/stayconnect/backups/db"

const (
	pgContainer = "stayconnect-pg"
	pgUser      = "stayconnect"
	pgDatabase  = "stayconnect_site"
)

// safeBackupName bounds what verify/download may name, since it is joined onto a path: no separator, dot-dot or arbitrary text.
// It must admit the db-safety-<stamp> copies and the encrypted .age forms.
var safeBackupName = regexp.MustCompile(`^db-(safety-)?[0-9TZ]{1,24}\.sql\.gz(\.age)?$`)

// backupRunReq says why a backup is wanted: "manual" from edged, "pre-change" from scd backup-now.
type backupRunReq struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	By     string `json:"by"`
}

// backupRun takes one backup, encrypted as written and proven restorable in a scratch database.
func (s *server) backupRun(w http.ResponseWriter, r *http.Request) {
	var in backupRunReq
	_ = json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in)
	if in.Kind == "" {
		in.Kind = kindManual
	}
	// Only the kinds an operator or a controlled change may ask for; a scheduled or pre-restore backup is the appliance's own.
	if in.Kind != kindManual && in.Kind != kindPreChange {
		httpErr(w, http.StatusBadRequest, "a backup on request is manual or pre-change")
		return
	}
	if in.Kind == kindPreChange && len(strings.TrimSpace(in.Reason)) < 4 {
		httpErr(w, http.StatusBadRequest, "a pre-change backup needs a reason (what is about to change)")
		return
	}
	started := time.Now().UTC()
	// NOT r.Context(): its own deadline, independent of whoever asked, so a caller giving up cannot leave a half-written backup.
	ctx := context.WithoutCancel(r.Context())
	meta, err := s.runBackupTracked(ctx, backupOpts{Kind: in.Kind, Reason: in.Reason, By: in.By})
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errNoBackupKeyHolder) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": "backup_failed", "message": "the backup did not complete: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": meta.Name, "size_bytes": meta.SizeBytes, "started_at": started, "encrypted": true, "kind": meta.Kind,
		"restorable": meta.Restorable, "ledger_seq": meta.LedgerSeq, "tables": meta.Tables, "etc_files": meta.EtcFiles,
	})
}

type verifyReq struct {
	Name string `json:"name"`
	// A backup key holder's private key: used for this request only, never stored or logged.
	Identity string `json:"identity"`
}

// backupVerify loads the dump into a throwaway database dropped on every path; nothing live is read, written or locked.
func (s *server) backupVerify(w http.ResponseWriter, r *http.Request) {
	var in verifyReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !safeBackupName.MatchString(in.Name) {
		httpErr(w, http.StatusBadRequest, "a database backup name is required")
		return
	}
	path := filepath.Join(scdBackupDir, in.Name)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		httpErr(w, http.StatusNotFound, "no such database backup")
		return
	}
	started := time.Now()
	// Own deadline, not r.Context(): edged gives up after 15s and verifying a large dump takes minutes.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Minute)
	defer cancel()

	scratch := "verify_" + time.Now().UTC().Format("20060102150405")
	admin := func(sql string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", "exec", pgContainer,
			"psql", "-U", pgUser, "-d", "postgres", "-tAc", sql).CombinedOutput()
	}
	if out, err := admin("CREATE DATABASE " + scratch); err != nil {
		httpErr(w, http.StatusInternalServerError,
			"a scratch database could not be created: "+tailLine(string(out)))
		return
	}
	// Clean up on its own context: r.Context() is cancelled by now and the DROP would be killed.
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		if out, err := exec.CommandContext(dctx, "docker", "exec", pgContainer,
			"psql", "-U", pgUser, "-d", "postgres", "-tAc",
			"DROP DATABASE IF EXISTS "+scratch+" WITH (FORCE)").CombinedOutput(); err != nil {
			slog.Error("scratch verification database not dropped", "db", scratch, "detail", tailLine(string(out)))
		}
	}()

	out, err := loadBackupInto(ctx, path, in.Identity, scratch)
	if msg, isKey := keyProblem(err); isKey {
		httpErr(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		// Report the first ERROR/FATAL line, not the last: psql keeps printing after an error.
		detail := firstProblem(string(out))
		if len(out) == 0 {
			detail = err.Error() // refused before psql ran, e.g. errNotSQLBackup
		}
		slog.Error("backup verification load failed", "err", err, "detail", detail)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "tables": 0, "duration": time.Since(started).Round(time.Second).String(),
			"detail": "the dump did not load: " + detail,
		})
		return
	}
	// A dump that loads but contains nothing is not a usable backup.
	cnt, _ := exec.CommandContext(ctx, "docker", "exec", pgContainer,
		"psql", "-U", pgUser, "-d", scratch, "-tAc",
		"SELECT count(*) FROM information_schema.tables WHERE table_schema IN ('public','iam_v2')").Output()
	tables := 0
	fmt.Sscanf(strings.TrimSpace(string(cnt)), "%d", &tables)

	res := map[string]any{
		"ok": tables > 0, "tables": tables,
		"duration": time.Since(started).Round(time.Second).String(),
	}
	if tables == 0 {
		res["detail"] = "the dump loaded but produced no tables"
	} else {
		// Restore requires a recorded verification; the marker names the archive's size and mtime so a replaced file stops being verified.
		if merr := writeVerifiedMarker(in.Name, tables); merr != nil {
			res["ok"] = false
			res["detail"] = "the backup loads, but it could not be marked verified, so it cannot be restored from the screen: " + merr.Error()
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// serviceGroupID resolves edged's group so a root-written backup can be handed over without making it world-readable.
func serviceGroupID() (int, bool) {
	g, err := user.LookupGroup("stayconnect")
	if err != nil {
		return 0, false
	}
	var gid int
	if _, err := fmt.Sscanf(g.Gid, "%d", &gid); err != nil {
		return 0, false
	}
	return gid, true
}

// firstProblem returns the first ERROR/FATAL line, the cause, rather than the tool's trailing output.
func firstProblem(s string) string {
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		up := strings.ToUpper(t)
		if strings.HasPrefix(up, "ERROR") || strings.HasPrefix(up, "FATAL") || strings.Contains(up, "PSQL: ERROR") {
			if len(t) > 300 {
				return t[:300] + "…"
			}
			return t
		}
	}
	return tailLine(s)
}

func tailLine(s string) string {
	last := ""
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
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
