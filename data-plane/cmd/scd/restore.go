package main

// Restoring the site database, the one operation here that can lose a hotel's data. The destructive step is
// a rename, not a load (dropping the live database loses everything if the dump is truncated):
//
//   1. the dump loads into a STAGING database while the live one is untouched; a bad dump fails here;
//   2. once staging is complete the two swap by rename; the live database is renamed aside and kept;
//   3. integrity is checked on the newly-live database; on failure the rename is undone.
//
// A safety dump is taken first, since a rename-back does not protect against a bad appliance.
//
// No silent retry: every step runs once; a failure stops the sequence, rolls back and reports.
//
// Authorisation is edged's job (step-up, RBAC, audit) before calling this over scd's private socket.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// maintenanceFile and restoreResultFile are vars only so tests can redirect them.
//
// maintenanceFile is the "not serving" flag. It lives under /run so a reboot mid-restore does not leave a
// permanently closed appliance: it comes back serving the original or the fully-loaded replacement, never a
// half-written database, because no step writes into a live database.
var maintenanceFile = "/run/stayconnect/maintenance"

// restoreResultFile is written before any service restart so the answer survives it.
var restoreResultFile = "/run/stayconnect/last-restore.json"

// verifiedMarker is written by a successful verification and required by restore. It records the SHA-256 of
// the archive: size and mtime are only metadata anyone able to write the file can put back (padding plus
// `touch -r` made a corrupt backup look verified). Size and mtime are still checked first (cheap, clearer
// message) but the digest is what the answer rests on, recomputed at the restore boundary. A marker with no
// digest is NOT verified.
func verifiedMarker(name string) string { return filepath.Join(scdBackupDir, name+".verified") }

type verifiedRecord struct {
	VerifiedAt time.Time `json:"verified_at"`
	SizeBytes  int64     `json:"size_bytes"`
	ModTime    time.Time `json:"mod_time"`
	Tables     int       `json:"tables"`
	SHA256     string    `json:"sha256"`
}

// fileDigest is the hex SHA-256 of a file, streamed.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeVerifiedMarker(name string, tables int) error {
	full := filepath.Join(scdBackupDir, name)
	fi, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("the backup could not be read to mark it verified: %w", err)
	}
	// No digest, no marker: one without it would read as unverified anyway.
	sum, err := fileDigest(full)
	if err != nil {
		slog.Error("verification marker not written: the archive could not be digested", "backup", name, "err", err)
		return fmt.Errorf("the backup could not be digested: %w", err)
	}
	b, _ := json.Marshal(verifiedRecord{
		VerifiedAt: time.Now().UTC(), SizeBytes: fi.Size(), ModTime: fi.ModTime().UTC(),
		Tables: tables, SHA256: sum,
	})
	path := verifiedMarker(name)
	if err := os.WriteFile(path, b, 0o640); err != nil {
		slog.Error("verification marker not written", "backup", name, "err", err)
		return fmt.Errorf("the verification marker could not be written: %w", err)
	}
	// Hand it to the service group like the dump: scd writes as root but edged reads as `stayconnect`, so a
	// root:root marker is unreadable and the backup would show as unverified.
	if gid, ok := serviceGroupID(); ok {
		if err := os.Chown(path, 0, gid); err != nil {
			slog.Error("verification marker not handed to the service group", "backup", name, "err", err)
		}
	}
	return nil
}

// readVerifiedMarker returns the record only if the archive on disk is still, byte for byte, the one verified.
// This is the restore boundary: the screen is not a security boundary (stale, second tab, curl), so
// eligibility is decided here, just before the restore begins, from the bytes as they are now.
func readVerifiedMarker(name string) (*verifiedRecord, error) {
	b, err := os.ReadFile(verifiedMarker(name))
	if err != nil {
		return nil, fmt.Errorf("this backup has not been verified")
	}
	var rec verifiedRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("this backup's verification record could not be read")
	}
	if rec.SHA256 == "" {
		return nil, fmt.Errorf("this backup was verified before content checks existed, so what it " +
			"contained cannot be confirmed; verify it again")
	}
	full := filepath.Join(scdBackupDir, name)
	fi, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("the backup file is missing")
	}
	if fi.Size() != rec.SizeBytes || !fi.ModTime().UTC().Equal(rec.ModTime) {
		return nil, fmt.Errorf("this backup has changed since it was verified; verify it again")
	}
	// The binding check: size and time can be put back by whoever changed the file; the digest cannot.
	sum, err := fileDigest(full)
	if err != nil {
		return nil, fmt.Errorf("this backup could not be read to confirm its contents")
	}
	if sum != rec.SHA256 {
		return nil, fmt.Errorf("this backup's contents no longer match what was verified, even though its " +
			"size and timestamp are unchanged; verify it again")
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
	// Identity is a backup key holder's private key for an encrypted backup; held in memory only, never stored or logged.
	Identity string `json:"identity"`
	// By is the operator who asked, from edged's session.
	By string `json:"by"`
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

// restoreInFlight is the in-process single-flight guard. The durable maintenance flag is only raised after the
// safety dump, so this closes the window where a second request could start a second restore.
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

	// Only a verified backup, checked here as well as in the UI.
	rec, err := readVerifiedMarker(in.Name)
	if err != nil {
		httpErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	// The key is checked now, before the safety dump and maintenance: a wrong key must not close the appliance.
	src, closer, err := openBackup(filepath.Join(scdBackupDir, in.Name), in.Identity)
	if err != nil {
		if msg, isKey := keyProblem(err); isKey {
			httpErr(w, http.StatusBadRequest, msg)
			return
		}
		if errors.Is(err, errNotSQLBackup) {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpErr(w, http.StatusInternalServerError, "the backup could not be opened: "+err.Error())
		return
	}
	_, _ = io.ReadFull(src, make([]byte, 16))
	closer.Close()

	if !restoreInFlight.CompareAndSwap(false, true) {
		httpErr(w, http.StatusConflict, "a restore is already running on this appliance")
		return
	}

	// The answer is the record on disk, not this response: a restore can take 45 minutes and a client timing out
	// early would report "failed" while it runs, inviting a second destructive attempt. The request only STARTS
	// the restore; the outcome lives in /run/stayconnect/last-restore.json (which survives the edged restart) and
	// the screen polls it.
	started := time.Now().UTC()
	s.markRestoreRunning(in.Name, started)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": true, "backup": in.Name, "started_at": started,
		"detail": "the restore is running; its outcome is recorded on the appliance",
	})

	// Its own deadline and context: a caller giving up, or the request ending, must not kill it halfway.
	ctx, cancel := context.WithTimeout(context.Background(), restoreOverallDeadline())
	go func() {
		defer cancel()
		defer restoreInFlight.Store(false)
		s.runRestore(ctx, in.Name, in.Identity, in.By, rec, started)
	}()
}

// runRestore performs the restore and reports only durably (the HTTP request is long gone and edged restarted).
func (s *server) runRestore(ctx context.Context, name, identity, by string, rec *verifiedRecord, started time.Time) {
	in := restoreReq{Name: name}
	steps := []restoreStep{}
	stamp := started.Format("20060102T150405Z")

	// Lowercase: SQL folds unquoted identifiers but libpq passes the dbname verbatim, so a mixed-case name
	// is created under one spelling and looked up under another.
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
	// Dumps carry no privileges. Read first: failing here costs nothing, after the swap it would leave an
	// appliance that cannot serve. See privileges.go.
	if kinds, err := unhandledPrivileges(ctx, pgDatabase); err != nil {
		fail("privileges", "the database's access rules could not be inspected, so the restore was not attempted: "+err.Error())
		return
	} else if kinds != "" {
		fail("privileges", "this database uses access rules this appliance cannot carry across a restore ("+
			kinds+"), so the restore was not attempted. Restoring would silently drop them.")
		return
	}
	privileges, err := capturePrivileges(ctx, pgDatabase)
	if err != nil {
		fail("privileges", "the database's access rules could not be read, so the restore was not attempted: "+err.Error())
		return
	}
	ok("privileges", fmt.Sprintf("%d ownership and access rules captured from the live database", len(privileges)))

	// ---- 0b. every erasure the live database knows is durable outside it before anything is replaced ------------
	if detail, good := s.restoreLedgerPrepare(ctx); !good {
		fail("ledger_prepare", detail)
		return
	} else {
		ok("ledger_prepare", detail)
	}

	// ---- 1. a safety dump of the CURRENT state, before anything is touched ------------------------------
	// Same engine as every backup (encrypted, proven restorable); the restoring key holder is a recipient too.
	// No plaintext fallback exists.
	safetyMeta, err := s.takeBackup(ctx, backupOpts{Kind: kindPreRestore, Reason: "before restoring " + in.Name, By: by, Identity: identity})
	if err != nil {
		fail("safety_backup", "the safety backup failed, so the restore was not attempted: "+err.Error())
		return
	}
	safety := safetyMeta.Name
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

	if out, err := loadBackupInto(ctx, filepath.Join(scdBackupDir, in.Name), identity, staging); err != nil {
		dropStaging()
		detail := firstProblem(string(out))
		if msg, isKey := keyProblem(err); isKey {
			detail = msg
		} else if len(out) == 0 {
			detail = err.Error()
		}
		fail("load", "the backup did not load, so nothing was replaced: "+detail)
		return
	}
	ok("load", fmt.Sprintf("the backup loaded into a staging database (%d tables when verified)", rec.Tables))

	// ---- 3b. who may open this hotel's backups is decided now, not by the backup (backup_key_carry.go) ----
	if carried, detail := keyRegisterStep(ctx, pgDatabase, staging); !carried {
		dropStaging()
		fail("key_holders", detail)
		return
	} else {
		ok("key_holders", detail)
	}

	// ---- 3c. the erasure ledger is verified and applied to the STAGING database, before the swap -------------------
	// Erasures already promised to guests are re-applied to the restored data before it goes live
	// (ledger_restore.go); if no ledger copy can be trusted the restore stops with nothing replaced.
	if detail, good := s.carryLedgerDestination(ctx, staging); !good {
		dropStaging()
		fail("ledger_storage", detail)
		return
	} else {
		ok("ledger_storage", detail)
	}
	if detail, good := s.restoreLedgerApply(ctx, staging, identity); !good {
		dropStaging()
		fail("ledger", detail)
		return
	} else {
		ok("ledger", detail)
	}

	// ---- 3d. retention is applied to the staging data, to the end, with the periods in force HERE (restore_retention.go) -------------
	// Not after the swap: a failed run must leave live data as it was, never a restored database serving
	// personal data past its retention period.
	if detail, good := s.restoreRetention(ctx, staging); !good {
		dropStaging()
		fail("retention", detail)
		return
	} else {
		ok("retention", detail)
	}

	// ---- 4. the swap: rename aside, rename in ----------------------------------------------------------
	//
	// Connections must be gone first or the rename is refused; services are stopped, not just disconnected,
	// since one reconnecting mid-swap would reopen the database being renamed.
	//
	// Financial guarantees (restore_financial.go): the quiesce is proven and the marker advanced before anything
	// is replaced.
	markerGen, err := readMarkerGeneration()
	if err != nil {
		dropStaging()
		fail("financial_marker", "the restore marker could not be read, so the restore was not attempted: "+err.Error())
		return
	}
	nextGen := markerGen + 1
	if err := proveQuiesced(ctx); err != nil {
		dropStaging()
		s.startDependents(ctx)
		fail("quiesce", "the restore was not attempted and nothing was replaced: "+err.Error())
		return
	}
	ok("quiesce", "every service that can write financial state was stopped and proven stopped")
	if err := advanceMarker(ctx, nextGen, rec.SHA256); err != nil {
		dropStaging()
		s.startDependents(ctx)
		fail("financial_marker", "the restore was not attempted and nothing was replaced: "+err.Error())
		return
	}
	ok("financial_marker", fmt.Sprintf("the restore marker was advanced from generation %d to %d before the swap", markerGen, nextGen))
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
		// Roll back the half-done swap: put the original name back.
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
	// The restored database arrives superuser-owned with default PUBLIC EXECUTE. The capture is rerun and
	// compared, since a half-worked replay fails later, which is worse than failing now with the data put back.
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
		// A rename back is fast and certain, and does not depend on a second dump being good.
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

	// ---- 6-fin. financial recovery: hypertables registered, and the restore recorded so money movement is HELD -----------
	// Both failures roll the swap back; the marker has already advanced, so money movement stays held (the safe direction).
	if hdetail, herr := assertHypertables(ctx, pgDatabase); herr != nil {
		rollback("hypertables", herr.Error()+" Money movement stays held.")
		return
	} else {
		ok("hypertables", hdetail)
	}
	var backupTaken time.Time
	if b, rerr := os.ReadFile(metaPathFor(in.Name)); rerr == nil {
		var bm backupMeta
		if json.Unmarshal(b, &bm) == nil {
			backupTaken = bm.TakenAt
		}
	}
	if rerr := recordSupportedRestore(ctx, pgDatabase, s.tenID, s.siteID, nextGen, rec.SHA256, backupTaken, by); rerr != nil {
		rollback("financial_recovery", rerr.Error()+" Money movement stays held.")
		return
	}
	ok("financial_recovery", "the restore is recorded: the site is in financial recovery mode and no money moves until an operator has reconciled what was in flight")

	// ---- 6a. the missing copies of the ledger are rebuilt and synchronised ------------------------------------------
	// Both ledger copies are brought into step with the restored database; an unreachable secondary does not
	// undo the restore, it is reported and retried every hour.
	if st := s.ledgerReconcileAfterRestore(ctx); st != "" {
		steps = append(steps, restoreStep{Step: "ledger_synced", OK: false, Detail: st})
	} else {
		ok("ledger_synced", "both copies of the erasure ledger are in step with the restored database")
	}

	// ---- 6a2. the hourly run is made once more, straight away ------------------------------------------------------------------
	// Takes records that crossed their cutoff since the staging run (step 3d) rather than waiting an hour; it
	// cannot expose anything staging did not handle, so its failure is only reported.
	{
		rctx, rcancel := context.WithTimeout(ctx, 10*time.Minute)
		s.runDataRetention(rctx)
		rcancel()
		ok("retention_reapplied", "the retention run was made once more on the live data, for whatever crossed its cutoff since the restored data was prepared")
	}

	// ---- 6b. keep exactly ONE database aside -----------------------------------------------------------
	//
	// The replaced database is kept so a restore can be undone; older set-aside ones are dropped only after this
	// restore passed its integrity and privilege checks, since until then the kept copy may be the only good one.
	if out, err := admin("SELECT datname FROM pg_database WHERE datname LIKE '" + pgDatabase +
		`\_before\_%' AND datname <> '` + aside + "'"); err == nil {
		dropped := 0
		for _, old := range strings.Fields(string(out)) {
			if _, err := admin("DROP DATABASE IF EXISTS " + old + " WITH (FORCE)"); err == nil {
				dropped++
			} else {
				slog.Warn("an older set-aside database could not be dropped", "database", old)
			}
		}
		if dropped > 0 {
			ok("housekeeping", fmt.Sprintf("%d older set-aside database(s) removed; %s is kept so this "+
				"restore can be undone", dropped, aside))
		}
	}

	// ---- 7. back in service ----------------------------------------------------------------------------
	s.resetPool()
	maintenanceOff()
	s.startDependents(ctx)
	ok("services", "the appliance is serving again")

	s.finishRestore(in.Name, started, steps, true,
		"restored from "+in.Name+"; the previous database is kept as "+aside)
}

// resetPool replaces scd's own database connections after the swap. A connection follows the database OID, not
// the name, so held connections still talk to the renamed-aside old data; closing idle ones forces new connections
// that resolve the name afresh.
func (s *server) resetPool() {
	if s.db == nil {
		return
	}
	s.db.Reset()
}

// startDependents brings the stopped services back; failures are logged so an operator learns which did not return.
func (s *server) startDependents(ctx context.Context) {
	for _, svc := range dependentServices {
		if out, err := exec.CommandContext(ctx, "systemctl", "start", svc).CombinedOutput(); err != nil {
			slog.Error("service did not restart after restore", "service", svc, "detail", tailLine(string(out)))
		}
	}
}

// restoreIntegrity asks whether what is now live is a usable StayConnect database. Deliberately structural
// (required tables, migration ledger, readable row count): a deep check is slower and cannot prove the data is right.
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

// finishRestore writes the outcome durably so it survives the service restarts, and audits it.
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
		// Write then rename: the file is polled, and os.WriteFile truncates first so a reader could see half a document.
		if err := writeResultAtomically(b); err != nil {
			slog.Error("restore result not written", "err", err)
		}
	}
	if success {
		slog.Info("database restore completed", "backup", name, "summary", summary)
	} else {
		slog.Error("database restore failed", "backup", name, "summary", summary)
	}
}

// writeResultAtomically replaces the outcome record in one step, so a poller never reads a partial document.
func writeResultAtomically(b []byte) error {
	tmp := restoreResultFile + ".partial"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, restoreResultFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// markRestoreRunning replaces the previous outcome with an in-progress record, so the screen never shows a
// previous "ok" banner while a database is being replaced.
func (s *server) markRestoreRunning(name string, started time.Time) {
	b, err := json.Marshal(map[string]any{
		"backup": name, "started": started, "running": true, "ok": false,
		"summary": "the restore is running",
		"steps":   []restoreStep{},
	})
	if err != nil {
		return
	}
	if err := writeResultAtomically(b); err != nil {
		slog.Error("restore progress not written", "err", err)
	}
}
