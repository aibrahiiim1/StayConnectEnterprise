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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	scdBackupDir = "/opt/stayconnect/backups/db"
	pgContainer  = "stayconnect-pg"
	pgUser       = "stayconnect"
	pgDatabase   = "stayconnect_site"
)

// safeBackupName bounds what verify/download may name. The caller is edged, but a path joined from a string
// that came over HTTP is worth constraining regardless of who is holding it.
var safeBackupName = regexp.MustCompile(`^db-[0-9TZ]{1,24}\.sql\.gz$`)

// backupRun takes a compressed logical dump of the site database.
func (s *server) backupRun(w http.ResponseWriter, r *http.Request) {
	if err := os.MkdirAll(scdBackupDir, 0o750); err != nil {
		httpErr(w, http.StatusInternalServerError, "the backup directory could not be created")
		return
	}
	started := time.Now().UTC()
	name := "db-" + started.Format("20060102T150405Z") + ".sql.gz"
	dest := filepath.Join(scdBackupDir, name)
	partial := dest + ".partial"

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()

	// Written to .partial and renamed only on success, so an interrupted dump is never left behind looking
	// like a backup somebody could restore from.
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c",
		"docker exec "+pgContainer+" pg_dump -U "+pgUser+" -d "+pgDatabase+
			" --no-owner --no-privileges | gzip -c > "+shq(partial)).CombinedOutput()
	if err != nil {
		_ = os.Remove(partial)
		slog.Error("database backup failed", "err", err, "detail", tailLine(string(out)))
		httpErr(w, http.StatusInternalServerError, "the database backup did not complete: "+tailLine(string(out)))
		return
	}
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		httpErr(w, http.StatusInternalServerError, "the completed dump could not be moved into place")
		return
	}
	fi, err := os.Stat(dest)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "the completed dump could not be measured")
		return
	}
	// gzip of nothing still exits 0, so an empty dump is a failure wearing a success's clothes.
	if fi.Size() < 1024 {
		_ = os.Remove(dest)
		httpErr(w, http.StatusInternalServerError, "the dump was empty and has been discarded")
		return
	}
	// Readable by the operator surface, which runs as the service user and must be able to list and stream it.
	_ = os.Chmod(dest, 0o640)
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "size_bytes": fi.Size(), "started_at": started,
	})
}

type verifyReq struct {
	Name string `json:"name"`
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
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
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
	defer func() { _, _ = admin("DROP DATABASE IF EXISTS " + scratch + " WITH (FORCE)") }()

	load := exec.CommandContext(ctx, "/bin/sh", "-c",
		"gzip -dc "+shq(path)+" | docker exec -i "+pgContainer+" psql -U "+pgUser+" -d "+scratch+
			" -v ON_ERROR_STOP=1 -q")
	if out, err := load.CombinedOutput(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "tables": 0, "duration": time.Since(started).Round(time.Second).String(),
			"detail": "the dump did not load: " + tailLine(string(out)),
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
	}
	writeJSON(w, http.StatusOK, res)
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
