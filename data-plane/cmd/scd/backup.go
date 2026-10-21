package main

// TAKING AND VERIFYING A DATABASE BACKUP — the privileged half.
//
// WHY THIS LIVES IN scd RATHER THAN edged
// ---------------------------------------
// PostgreSQL runs in a container on this appliance and there is no pg_dump on the host, so a dump means
// reaching the container. edged runs as the unprivileged `stayconnect` user and cannot reach the Docker
// socket — which is correct and must stay that way: membership of the `docker` group is effectively root, so
// granting it to the service that serves the operator HTTP surface would hand root to anything that got a
// foothold there.
//
// scd already runs as root and is already the appliance's privileged helper: edged proxies certificate
// rotation, licence installation and PMS administration to it for exactly this reason. A backup is the same
// shape of operation, so it takes the same route rather than inventing a second privileged path.
//
// AUTHORISATION LIVES ON THE OTHER SIDE. edged performs the password step-up, the RBAC check and the audit
// record before it calls any of this. These endpoints are on scd's private socket, which is not reachable
// from the network.

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

// scdBackupDir is a var rather than a const so tests can point it at a temporary directory. Nothing in the
// product reassigns it.
var scdBackupDir = "/opt/stayconnect/backups/db"

const (
	pgContainer = "stayconnect-pg"
	pgUser      = "stayconnect"
	pgDatabase  = "stayconnect_site"
)

// safeBackupName bounds what verify/download may name. The caller is edged, but a path joined from a string
// that came over HTTP is worth constraining regardless of who is holding it.
// THE SAFETY DUMPS COUNT. This was `^db-[0-9TZ]{1,24}\.sql\.gz$`, which matches db-<stamp>.sql.gz and
// nothing else -- so every db-safety-<stamp>.sql.gz a restore takes before it touches anything was refused by
// verify and by restore. The one copy an operator would most want back after a bad restore was the one copy
// the guard would not name, and the refusal read "a database backup name is required" about a file the
// screen had just listed.
//
// Still strict, and for the reason it was written: this bounds a string that gets joined onto a path, so it
// admits no separator, no dot-dot and no arbitrary text -- only the shapes this appliance produces, each
// either in the clear or encrypted to the hotel's keys (.age, migration 0129).
var safeBackupName = regexp.MustCompile(`^db-(safety-)?[0-9TZ]{1,24}\.sql\.gz(\.age)?$`)

// backupRunReq says why a backup is wanted. edged sends "manual" and the operator's name; a controlled migration or deployment
// (scd backup-now, on the socket only root can reach) sends "pre-change" and a reason.
type backupRunReq struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	By     string `json:"by"`
}

// backupRun takes one backup with the one engine (backups.go): encrypted as it is written, loaded into a scratch database to prove
// it restores, and recorded in a note. It answers when the backup is complete and verified.
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
	// Identity is a backup key holder's private key, needed for an encrypted backup. Used for this request
	// only; never stored or logged.
	Identity string `json:"identity"`
}

// backupVerify answers "would this restore?" WITHOUT restoring anything anyone depends on.
//
// The dump is loaded into a throwaway database created for the purpose and dropped immediately afterwards,
// on every path. Nothing about the live site is read, written or locked. This is why there is no restore
// endpoint beside it: the question an operator has about a backup can be answered safely, and a live restore
// cannot be undone by asking again.
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
	// NOT r.Context(), for the reason the live appliance demonstrated: edged's client gives up after 15s, so
	// a verification that takes longer had its psql killed and reported "signal: killed" as a load failure.
	// Verification of a 23 MB dump takes minutes; it runs on its own deadline.
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
	// THE CLEANUP MUST NOT RIDE ON THE REQUEST CONTEXT. The deferred drop runs after the response is written,
	// by which time r.Context() is cancelled -- so the DROP was killed before it ran and PRE-LIVE was left
	// with a verify_* database after the first verification. Its own background context and deadline.
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
		// NAME THE FAILURE, NOT THE LAST LINE. tailLine returns the final non-empty line, and psql keeps
		// printing normal output after an error -- so a genuine load failure was reported as "(1 row)", which
		// tells an operator nothing and sent me looking in the wrong place. The first ERROR/FATAL line is the
		// cause; the raw error is logged either way.
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
		// A SUCCESSFUL VERIFICATION IS RECORDED, because restore requires one. The marker names the size and
		// mtime of the archive it vouches for, so a file replaced under the same name stops being verified.
		writeVerifiedMarker(in.Name, tables)
	}
	writeJSON(w, http.StatusOK, res)
}

// serviceGroupID resolves the unprivileged group edged runs under, so a root-written artefact can be handed
// to it without widening the mode to world-readable. A backup is a complete copy of the site's data.
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

// firstProblem returns the first ERROR/FATAL line in command output, which is the cause, rather than the
// last line, which is usually whatever the tool printed afterwards.
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

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// tailLine returns the final non-empty line of command output: the part that says what went wrong.
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
