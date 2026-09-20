package main

// RESTORING THE SITE DATABASE — the one operation here that can lose a hotel's data.
//
// Everything in this file is shaped by one decision: THE DESTRUCTIVE STEP IS A RENAME, NOT A LOAD.
//
// The obvious implementation is to drop the live database and load the dump into a fresh one. It is also the
// one that loses everything when the dump turns out to be truncated at 80%, because by then the only copy of
// the real data is gone. So instead:
//
//   1. the dump is loaded into a STAGING database while the live one is untouched. A dump that will not load
//      fails here, having destroyed nothing, and the appliance goes back to serving.
//   2. only once staging holds a complete database do the two swap, by rename. The live database is not
//      dropped -- it is renamed aside and kept.
//   3. integrity is checked on the newly-live database. If it fails, the rename is undone, which is fast,
//      certain, and does not depend on a second dump being good.
//
// A safety dump is taken before any of this as well, because a rename-back protects against a bad restore
// and not against a bad appliance.
//
// WHAT "NO SILENT RETRY" MEANS HERE. Nothing in this file loops. Every step runs once; a failure stops the
// sequence, rolls back what was already done, and reports. An operator who wants another attempt makes
// another decision.
//
// AUTHORISATION IS edged'S JOB. It performs the password step-up, the RBAC check and the audit record before
// calling this, over scd's private socket, which is not reachable from the network.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// maintenanceFile and restoreResultFile are vars rather than consts so tests can redirect them. Nothing in
// the product reassigns them.
//
// maintenanceFile is the appliance's "not serving" flag. It lives under /run because a restore must not
// survive a reboot as a permanently closed appliance: if the machine dies mid-restore, it comes back
// serving from whichever database is live, which is either the original or the fully-loaded replacement --
// never a half-written one, because no step here ever writes into a live database.
var maintenanceFile = "/run/stayconnect/maintenance"

// restoreResultFile is where the outcome is written BEFORE any service restart, so the answer survives the
// restart that follows it.
var restoreResultFile = "/run/stayconnect/last-restore.json"

// verifiedMarker is written by a successful verification and REQUIRED by restore.
//
// It records the size and modification time of the archive it vouches for. A backup file that has been
// replaced since -- same name, different content -- no longer matches its marker and is refused, because
// "this name was verified once" is not the same claim as "these bytes were verified".
func verifiedMarker(name string) string { return filepath.Join(scdBackupDir, name+".verified") }

type verifiedRecord struct {
	VerifiedAt time.Time `json:"verified_at"`
	SizeBytes  int64     `json:"size_bytes"`
	ModTime    time.Time `json:"mod_time"`
	Tables     int       `json:"tables"`
}

func writeVerifiedMarker(name string, tables int) {
	fi, err := os.Stat(filepath.Join(scdBackupDir, name))
	if err != nil {
		return
	}
	b, _ := json.Marshal(verifiedRecord{
		VerifiedAt: time.Now().UTC(), SizeBytes: fi.Size(), ModTime: fi.ModTime().UTC(), Tables: tables,
	})
	path := verifiedMarker(name)
	if err := os.WriteFile(path, b, 0o640); err != nil {
		slog.Error("verification marker not written", "backup", name, "err", err)
		return
	}
	// HAND IT TO THE SERVICE GROUP, exactly as the dump itself is handed over.
	//
	// Found on the appliance: verify reported ok and 141 tables, the marker was written, and the operator
	// screen still showed the backup as unverified. scd writes as root; edged reads as `stayconnect`. The
	// archives are chowned root:stayconnect at 0640 so the unprivileged side can read them, and the marker
	// -- which is now the thing Restore eligibility depends on -- was left root:root. It was a fact nobody
	// could read, which is indistinguishable from no fact at all.
	if gid, ok := serviceGroupID(); ok {
		if err := os.Chown(path, 0, gid); err != nil {
			slog.Error("verification marker not handed to the service group", "backup", name, "err", err)
		}
	}
}

// readVerifiedMarker returns the record only if it still describes the file on disk.
func readVerifiedMarker(name string) (*verifiedRecord, error) {
	b, err := os.ReadFile(verifiedMarker(name))
	if err != nil {
		return nil, fmt.Errorf("this backup has not been verified")
	}
	var rec verifiedRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("this backup's verification record could not be read")
	}
	fi, err := os.Stat(filepath.Join(scdBackupDir, name))
	if err != nil {
		return nil, fmt.Errorf("the backup file is missing")
	}
	if fi.Size() != rec.SizeBytes || !fi.ModTime().UTC().Equal(rec.ModTime) {
		return nil, fmt.Errorf("this backup has changed since it was verified; verify it again")
	}
	return &rec, nil
}

// ---------------------------------------------------------------------------- maintenance ---------------

type maintenanceState struct {
	Active bool      `json:"active"`
	Reason string    `json:"reason,omitempty"`
	Since  time.Time `json:"since,omitempty"`
	Backup string    `json:"backup,omitempty"`
}

func maintenanceOn(reason, backup string) error {
	if err := os.MkdirAll(filepath.Dir(maintenanceFile), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(maintenanceState{Active: true, Reason: reason, Since: time.Now().UTC(), Backup: backup})
	return os.WriteFile(maintenanceFile, b, 0o644)
}

func maintenanceOff() {
	if err := os.Remove(maintenanceFile); err != nil && !os.IsNotExist(err) {
		slog.Error("maintenance flag not cleared", "err", err)
	}
}

func readMaintenance() maintenanceState {
	b, err := os.ReadFile(maintenanceFile)
	if err != nil {
		return maintenanceState{}
	}
	var m maintenanceState
	if json.Unmarshal(b, &m) != nil {
		// An unreadable flag is treated as ON. Being wrong that way costs availability until somebody looks;
		// being wrong the other way serves guests from a database mid-swap.
		return maintenanceState{Active: true, Reason: "maintenance state unreadable"}
	}
	return m
}

// maintenanceStatus serves GET /v1/maintenance so edged can report it and refuse writes.
func (s *server) maintenanceStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, readMaintenance())
}

// lastRestore serves the durable outcome of the most recent restore, which outlives the service restart.
func (s *server) lastRestore(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(restoreResultFile)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"present": false})
		return
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"present": false})
		return
	}
	v["present"] = true
	writeJSON(w, http.StatusOK, v)
}

// ---------------------------------------------------------------------------- the restore ---------------

type restoreReq struct {
	Name string `json:"name"`
}

type restoreStep struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// dependentServices reconnect after the swap. scd is deliberately absent: it is running this, and its own
// pool is reset in place rather than by suicide mid-request.
var dependentServices = []string{
	"stayconnect-edged", "stayconnect-portald", "stayconnect-pmsd", "stayconnect-acctd",
}

// restoreInFlight is the in-process single-flight guard.
//
// The maintenance flag is the DURABLE guard -- it survives a crash and fails closed -- but it is only raised
// after the safety dump, which leaves a window where a second request would start a second restore. One
// destructive operation at a time is not negotiable, so the window is closed here.
var restoreInFlight atomic.Bool

func (s *server) backupRestore(w http.ResponseWriter, r *http.Request) {
	var in restoreReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !safeBackupName.MatchString(in.Name) {
		httpErr(w, http.StatusBadRequest, "a database backup name is required")
		return
	}
	if m := readMaintenance(); m.Active {
		httpErr(w, http.StatusConflict, "this appliance is already in maintenance: "+m.Reason)
		return
	}

	// ONLY A VERIFIED BACKUP. Checked here as well as in the UI, because the UI is not the security boundary.
	rec, err := readVerifiedMarker(in.Name)
	if err != nil {
		httpErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	if !restoreInFlight.CompareAndSwap(false, true) {
		httpErr(w, http.StatusConflict, "a restore is already running on this appliance")
		return
	}

	// THE ANSWER IS THE RECORD ON DISK, NOT THIS RESPONSE.
	//
	// Found on the appliance, and it was the worst possible way to be wrong. edged's socket call gave up after
	// fifteen seconds and told the operator the restore had FAILED; scd finished it successfully ten seconds
	// later. An operator reading "failed" while a destructive operation is still running is one keystroke away
	// from starting a second one -- and the deliberate design decision not to retry destructive steps is worth
	// nothing if the screen invites the human to do it instead.
	//
	// A restore can take 45 minutes. No HTTP client, no reverse proxy and no browser tab should be load-bearing
	// for that long. So the request STARTS the restore and returns; the durable record in
	// /run/stayconnect/last-restore.json is where the outcome lives, and it is already the thing that survives
	// the edged restart the restore itself performs. The screen polls it.
	started := time.Now().UTC()
	s.markRestoreRunning(in.Name, started)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": true, "backup": in.Name, "started_at": started,
		"detail": "the restore is running; its outcome is recorded on the appliance",
	})

	// Its own deadline and its own context. A restore outlives any client, and a caller giving up -- or a
	// request whose context ends the moment this handler returns -- must not kill it halfway.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	go func() {
		defer cancel()
		defer restoreInFlight.Store(false)
		s.runRestore(ctx, in.Name, rec, started)
	}()
}

// runRestore performs the restore. It reports only durably: by the time it finishes, the HTTP request that
// asked for it is long gone and edged has been restarted.
func (s *server) runRestore(ctx context.Context, name string, rec *verifiedRecord, started time.Time) {
	in := restoreReq{Name: name}
	steps := []restoreStep{}
	stamp := started.Format("20060102T150405Z")

	// LOWERCASE, because an unquoted SQL identifier is folded and a connection name is not.
	//
	// Found on the appliance. `CREATE DATABASE restore_20260920T090146Z` succeeded and created
	// `restore_20260920t090146z`; `psql -d restore_20260920T090146Z` then reported that the database did not
	// exist, and the restore failed at the load step with a message about a missing database it had just
	// created. The two halves disagreed because only one of them folds case: SQL folds unquoted identifiers,
	// libpq passes the dbname through verbatim.
	//
	// The timestamp is the right name -- it ties the staging database, the set-aside database and the safety
	// dump to one restore -- so the fix is to make the name one that survives both paths unchanged. Every
	// database identifier in this function is now lowercase (pgDatabase already was), which makes folding a
	// no-op rather than something each call site has to remember.
	dbStamp := strings.ToLower(stamp)
	staging := "restore_" + dbStamp
	aside := pgDatabase + "_before_" + dbStamp

	fail := func(step, detail string) {
		steps = append(steps, restoreStep{Step: step, OK: false, Detail: detail})
		s.finishRestore(in.Name, started, steps, false, detail)
	}
	ok := func(step, detail string) {
		steps = append(steps, restoreStep{Step: step, OK: true, Detail: detail})
	}

	admin := func(sql string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", "exec", pgContainer,
			"psql", "-U", pgUser, "-d", "postgres", "-tAc", sql).CombinedOutput()
	}

	// ---- 0. what the services are allowed to do, captured while the live database still says it ---------
	//
	// A backup does not contain this: dumps are taken --no-owner --no-privileges. Read first, because it is a
	// read -- failing here costs nothing, and discovering it after the swap would mean an appliance that
	// cannot serve and no record of what it was allowed to do. See privileges.go.
	privileges, err := capturePrivileges(ctx, pgDatabase)
	if err != nil {
		fail("privileges", "the database's access rules could not be read, so the restore was not attempted: "+err.Error())
		return
	}
	ok("privileges", fmt.Sprintf("%d ownership and access rules captured from the live database", len(privileges)))

	// ---- 1. a safety dump of the CURRENT state, before anything is touched ------------------------------
	safety, err := s.dumpTo(ctx, "db-safety-"+stamp+".sql.gz")
	if err != nil {
		fail("safety_backup", "the safety backup failed, so the restore was not attempted: "+err.Error())
		return
	}
	ok("safety_backup", safety)

	// ---- 2. stop serving --------------------------------------------------------------------------------
	if err := maintenanceOn("restoring the database from "+in.Name, in.Name); err != nil {
		fail("maintenance", "the appliance could not be put into maintenance: "+err.Error())
		return
	}
	ok("maintenance", "the appliance stopped serving while the database was replaced")
	// From here on every exit must clear it.
	defer maintenanceOff()

	// ---- 3. load into STAGING. The live database is still untouched. -----------------------------------
	if out, err := admin("CREATE DATABASE " + staging); err != nil {
		fail("staging", "a staging database could not be created: "+tailLine(string(out)))
		return
	}
	dropStaging := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer dcancel()
		_, _ = exec.CommandContext(dctx, "docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres",
			"-tAc", "DROP DATABASE IF EXISTS "+staging+" WITH (FORCE)").CombinedOutput()
	}

	load := exec.CommandContext(ctx, "/bin/sh", "-c",
		"gzip -dc "+shq(filepath.Join(scdBackupDir, in.Name))+" | docker exec -i "+pgContainer+
			" psql -U "+pgUser+" -d "+staging+" -v ON_ERROR_STOP=1 -q")
	if out, err := load.CombinedOutput(); err != nil {
		dropStaging()
		// NOTHING WAS LOST. The live database was never opened.
		fail("load", "the backup did not load, so nothing was replaced: "+firstProblem(string(out)))
		return
	}
	ok("load", fmt.Sprintf("the backup loaded into a staging database (%d tables when verified)", rec.Tables))

	// ---- 4. the swap: rename aside, rename in ----------------------------------------------------------
	//
	// Connections must be gone first or the rename is refused. The dependent services are stopped rather
	// than merely disconnected, because a service that reconnects during the swap would reopen the database
	// being renamed.
	for _, svc := range dependentServices {
		_ = exec.CommandContext(ctx, "systemctl", "stop", svc).Run()
	}
	if out, err := admin("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '" +
		pgDatabase + "' AND pid <> pg_backend_pid()"); err != nil {
		slog.Warn("could not terminate all connections before swap", "detail", tailLine(string(out)))
	}

	if out, err := admin("ALTER DATABASE " + pgDatabase + " RENAME TO " + aside); err != nil {
		dropStaging()
		s.startDependents(ctx)
		fail("swap", "the live database could not be set aside, so nothing was replaced: "+tailLine(string(out)))
		return
	}
	if out, err := admin("ALTER DATABASE " + staging + " RENAME TO " + pgDatabase); err != nil {
		// ROLL BACK THE HALF-DONE SWAP IMMEDIATELY: put the original name back.
		if out2, err2 := admin("ALTER DATABASE " + aside + " RENAME TO " + pgDatabase); err2 != nil {
			s.startDependents(ctx)
			fail("swap", "CRITICAL: the restored database could not be renamed in AND the original could not "+
				"be renamed back. The original data is intact under "+aside+". "+tailLine(string(out2)))
			return
		}
		dropStaging()
		s.startDependents(ctx)
		fail("swap", "the restored database could not be renamed in; the original was put back unchanged: "+
			tailLine(string(out)))
		return
	}
	ok("swap", "the restored database is live; the previous one is kept as "+aside)

	// ---- 5. put the access rules back, and prove they took ---------------------------------------------
	//
	// The restored database arrived owned by the superuser with PUBLIC holding the default EXECUTE on every
	// function. Replaying is only half the job: the same capture is run again and compared, because a replay
	// that half-worked leaves an appliance that starts and then fails on whichever table the operator reaches
	// last -- which is worse than one that fails now and puts the original data back.
	rollback := func(step, detail string) {
		if out, err := admin("ALTER DATABASE " + pgDatabase + " RENAME TO " + staging); err == nil {
			if out2, err2 := admin("ALTER DATABASE " + aside + " RENAME TO " + pgDatabase); err2 == nil {
				dropStaging()
				s.startDependents(ctx)
				fail(step, detail+" The appliance was rolled back and is running on its original data.")
				return
			} else {
				_ = out2
			}
		} else {
			_ = out
		}
		s.startDependents(ctx)
		fail(step, "CRITICAL: "+detail+" The rollback ALSO failed. The original data is intact under "+
			aside+" and must be renamed back by hand.")
	}

	if err := applyPrivileges(ctx, pgDatabase, privileges); err != nil {
		rollback("privileges", "the database's access rules could not be put back: "+err.Error())
		return
	}
	if after, err := capturePrivileges(ctx, pgDatabase); err != nil {
		rollback("privileges", "the restored database's access rules could not be read back: "+err.Error())
		return
	} else if diff := privilegesMatch(privileges, after); diff != "" {
		rollback("privileges", "the access rules did not come back as they were: "+diff)
		return
	}
	ok("privileges", fmt.Sprintf("all %d ownership and access rules reinstated and verified identical", len(privileges)))

	// ---- 6. integrity, on what is now live -------------------------------------------------------------
	detail, good := s.restoreIntegrity(ctx)
	if !good {
		// ROLL BACK. A rename back is fast and certain, and does not depend on a second dump being good.
		_, _ = admin("ALTER DATABASE " + pgDatabase + " RENAME TO " + staging)
		if out, err := admin("ALTER DATABASE " + aside + " RENAME TO " + pgDatabase); err != nil {
			s.startDependents(ctx)
			fail("integrity", "CRITICAL: the restored database failed its integrity check and the original "+
				"could not be renamed back. The original data is intact under "+aside+". "+tailLine(string(out)))
			return
		}
		dropStaging()
		s.startDependents(ctx)
		fail("integrity", "the restored database failed its integrity check and was rolled back; the "+
			"appliance is running on its original data. "+detail)
		return
	}
	ok("integrity", detail)

	// ---- 7. back in service ----------------------------------------------------------------------------
	s.resetPool()
	maintenanceOff()
	s.startDependents(ctx)
	ok("services", "the appliance is serving again")

	s.finishRestore(in.Name, started, steps, true,
		"restored from "+in.Name+"; the previous database is kept as "+aside)
}

// resetPool replaces scd's own database connections after the swap.
//
// A PostgreSQL connection follows the database OID, not the name. After the rename, every connection scd
// already held is still talking to the database that was renamed ASIDE -- the old data -- while the name now
// resolves to the restored one. Reads would look plausible and be wrong, which is the worst available
// outcome. pgxpool has no "reconnect"; closing the idle connections forces new ones, which resolve the name
// afresh.
func (s *server) resetPool() {
	if s.db == nil {
		return
	}
	s.db.Reset()
}

// startDependents brings the stopped services back. Failures are logged rather than returned: by the time
// this runs the database question is already settled, and an operator needs to be told which service did not
// come back rather than have it hidden behind an earlier error.
func (s *server) startDependents(ctx context.Context) {
	for _, svc := range dependentServices {
		if out, err := exec.CommandContext(ctx, "systemctl", "start", svc).CombinedOutput(); err != nil {
			slog.Error("service did not restart after restore", "service", svc, "detail", tailLine(string(out)))
		}
	}
}

// restoreIntegrity asks whether what is now live is a usable StayConnect database.
//
// Deliberately structural rather than exhaustive: the tables the appliance cannot run without, the migration
// ledger, and a readable row count. A deep consistency check would be slower, would still not prove the data
// is the RIGHT data, and would delay the moment the hotel is serving again.
func (s *server) restoreIntegrity(ctx context.Context) (string, bool) {
	q := func(sql string) string {
		out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer,
			"psql", "-U", pgUser, "-d", pgDatabase, "-tAc", sql).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	tables := q("SELECT count(*) FROM information_schema.tables WHERE table_schema IN ('public','iam_v2')")
	if tables == "" || tables == "0" {
		return "the restored database has no tables", false
	}
	for _, must := range []string{"public.tenants", "iam_v2.stays", "iam_v2.sessions", "iam_v2.entitlements"} {
		parts := strings.SplitN(must, ".", 2)
		got := q(fmt.Sprintf(
			"SELECT count(*) FROM information_schema.tables WHERE table_schema='%s' AND table_name='%s'",
			parts[0], parts[1]))
		if got != "1" {
			return "the restored database is missing " + must, false
		}
	}
	if q("SELECT count(*) FROM public.tenants") == "" {
		return "the restored database could not be read", false
	}
	return "the restored database holds " + tables + " tables and every table the appliance needs", true
}

// finishRestore writes the outcome durably BEFORE responding, so the answer survives the service restarts,
// and audits it on the way out.
func (s *server) finishRestore(name string, started time.Time,
	steps []restoreStep, success bool, summary string) {

	res := map[string]any{
		"backup":   name,
		"started":  started,
		"finished": time.Now().UTC(),
		"duration": time.Since(started).Round(time.Second).String(),
		"ok":       success,
		"running":  false,
		"summary":  summary,
		"steps":    steps,
	}
	if b, err := json.Marshal(res); err == nil {
		if err := os.WriteFile(restoreResultFile, b, 0o644); err != nil {
			slog.Error("restore result not written", "err", err)
		}
	}
	if success {
		slog.Info("database restore completed", "backup", name, "summary", summary)
	} else {
		slog.Error("database restore failed", "backup", name, "summary", summary)
	}
}

// markRestoreRunning replaces the previous outcome with an in-progress record, the moment the restore starts.
//
// Without it the screen would keep showing the PREVIOUS restore's result while a new one ran -- and if that
// previous one said "ok", an operator would be reading a success banner about a database currently being
// replaced. The record is overwritten by finishRestore either way.
func (s *server) markRestoreRunning(name string, started time.Time) {
	b, err := json.Marshal(map[string]any{
		"backup": name, "started": started, "running": true, "ok": false,
		"summary": "the restore is running",
		"steps":   []restoreStep{},
	})
	if err != nil {
		return
	}
	if err := os.WriteFile(restoreResultFile, b, 0o644); err != nil {
		slog.Error("restore progress not written", "err", err)
	}
}
