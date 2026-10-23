package main

// Safety properties of the one operation that can lose a hotel's data. They pin decisions a later "simplifying" edit could quietly reverse;
// that a restore works is proven on the appliance.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOnlyAVerifiedBackupMayBeRestored(t *testing.T) {
	dir := t.TempDir()
	old := scdBackupDirForTest(t, dir)
	defer old()

	name := "db-20260920T101010Z.sql.gz"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not really a dump, but present"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Unverified is refused here too: the screen is not the security boundary.
	if _, err := readVerifiedMarker(name); err == nil {
		t.Fatal("an unverified backup was accepted for restore")
	}

	writeVerifiedMarker(name, 63)
	rec, err := readVerifiedMarker(name)
	if err != nil {
		t.Fatalf("a verified backup was refused: %v", err)
	}
	if rec.Tables != 63 {
		t.Errorf("the marker recorded %d tables, want 63", rec.Tables)
	}
}

func TestAVerifiedBackupThatCHANGEDIsRefused(t *testing.T) {
	// A name verified once is not bytes verified: a replaced file (re-run, hand copy, partial write) must lose its verification.
	dir := t.TempDir()
	defer scdBackupDirForTest(t, dir)()

	name := "db-20260920T111111Z.sql.gz"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("original contents"), 0o640); err != nil {
		t.Fatal(err)
	}
	writeVerifiedMarker(name, 63)
	if _, err := readVerifiedMarker(name); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := os.WriteFile(path, []byte("replaced contents, longer than before"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := readVerifiedMarker(name)
	if err == nil {
		t.Fatal("a backup that changed since verification was still accepted")
	}
	if !strings.Contains(err.Error(), "verify it again") {
		t.Errorf("the refusal does not tell the operator what to do: %q", err)
	}
}

func TestMaintenanceFailsClosedWhenUnreadable(t *testing.T) {
	// An unreadable flag is treated as ON. Being wrong that way costs availability until somebody looks;
	// being wrong the other way serves guests from a database in the middle of being swapped.
	dir := t.TempDir()
	defer maintenanceFileForTest(t, filepath.Join(dir, "maintenance"))()

	if m := readMaintenance(); m.Active {
		t.Error("no flag present should mean serving normally")
	}
	if err := os.WriteFile(maintenanceFile, []byte("{this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := readMaintenance(); !m.Active {
		t.Error("an unreadable maintenance flag must fail CLOSED, not open")
	}
}

func TestMaintenanceRoundTrips(t *testing.T) {
	dir := t.TempDir()
	defer maintenanceFileForTest(t, filepath.Join(dir, "maintenance"))()

	if err := maintenanceOn("restoring the database from db-x.sql.gz", "db-x.sql.gz"); err != nil {
		t.Fatal(err)
	}
	m := readMaintenance()
	if !m.Active || m.Backup != "db-x.sql.gz" {
		t.Fatalf("maintenance state did not round-trip: %+v", m)
	}
	if m.Since.IsZero() || time.Since(m.Since) > time.Minute {
		t.Error("maintenance did not record when it started")
	}
	maintenanceOff()
	if readMaintenance().Active {
		t.Error("maintenance was not cleared")
	}
}

// The order of operations is the safety model, asserted on the source: staging load before any swap, rename not drop, integrity check after,
// rollback on both failure paths. A drop-and-load "simplification" would pass behavioural tests and lose data.
func TestTheDestructiveStepIsARenameAndComesLast(t *testing.T) {
	s := restoreSequence(t)

	// Nothing may DROP the live database. Staging may be dropped; the live one is renamed aside and kept.
	for _, forbidden := range []string{
		"DROP DATABASE " + pgDatabase,
		`DROP DATABASE "+pgDatabase`,
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("the live database is dropped somewhere (%q); it must be renamed aside and kept", forbidden)
		}
	}

	order := []struct{ what, needle string }{
		{"the safety dump", "s.takeBackup(ctx,"},
		{"maintenance on", "maintenanceOn("},
		{"the staging load", "identity, staging)"},
		{"the rename aside", "RENAME TO \" + aside"},
		{"the rename in", "RENAME TO \" + pgDatabase"},
		{"the integrity check", "s.restoreIntegrity(ctx)"},
	}
	last := -1
	for _, step := range order {
		i := strings.Index(s, step.needle)
		if i < 0 {
			t.Fatalf("%s is missing from the restore sequence (%q)", step.what, step.needle)
		}
		if i < last {
			t.Errorf("%s happens out of order; the sequence is what makes this safe", step.what)
		}
		last = i
	}

	if strings.Count(s, "RENAME TO \" + pgDatabase") < 2 {
		t.Error("there is no rollback rename; a failed restore must put the original database back")
	}
	// And nothing may retry a destructive step on its own.
	for _, loop := range []string{"for attempt", "for retry", "for i := 0; i < 3"} {
		if strings.Contains(s, loop) {
			t.Errorf("a retry loop (%q) appears in the restore path; destructive steps run once", loop)
		}
	}
}

func TestTheSafetyBackupIsTakenBeforeAnythingIsTouched(t *testing.T) {
	s := restoreSequence(t)
	safety := strings.Index(s, "s.takeBackup(ctx,")
	firstDestructive := strings.Index(s, "ALTER DATABASE")
	if safety < 0 || firstDestructive < 0 {
		t.Fatal("the restore sequence could not be read")
	}
	if safety > firstDestructive {
		t.Error("the safety backup is taken after the database is altered, which is not a safety backup")
	}
	// And a failed safety backup must abort rather than continue.
	if !strings.Contains(s, "the safety backup failed, so the restore was not attempted") {
		t.Error("a failed safety backup does not abort the restore")
	}
}

func TestTheRestoreOutcomeSurvivesTheServiceRestart(t *testing.T) {
	// A restore restarts edged. If the answer only existed in the HTTP response, an operator whose browser
	// reconnected to a restarted service would never learn what happened.
	src, _ := os.ReadFile("restore.go")
	s := string(src)
	write := strings.Index(s, "writeResultAtomically")
	restart := strings.Index(s, "func (s *server) startDependents")
	if write < 0 {
		t.Fatal("the restore outcome is never written durably")
	}
	// AND IT IS WRITTEN IN ONE STEP. A screen polls this file every few seconds while it is being rewritten;
	// os.WriteFile truncates first, so a direct write hands a reader half a document about a hotel's data.
	if !strings.Contains(s, `os.Rename(tmp, restoreResultFile)`) {
		t.Error("the outcome record is not written atomically; a poller can read a half-written document")
	}
	if restart < 0 {
		t.Fatal("dependent services are never restarted")
	}
	if !strings.Contains(s, "finishRestore") {
		t.Error("the outcome is not funnelled through one place")
	}
}

func TestDatabaseNamesSurviveIdentifierFolding(t *testing.T) {
	// Staging names must be lowercase: SQL folds unquoted identifiers but libpq passes dbname verbatim, so CREATE DATABASE restore_...T...Z
	// created a database the next psql could not find.
	s := restoreSequence(t)
	if !strings.Contains(s, "strings.ToLower(stamp)") {
		t.Error("the database-name stamp is no longer lowercased; SQL folds identifiers and libpq does not")
	}
	for _, built := range []string{`staging := "restore_" + stamp`, `aside := pgDatabase + "_before_" + stamp`} {
		if strings.Contains(s, built) {
			t.Errorf("a database name is built from the raw stamp (%q), which contains T and Z", built)
		}
	}
}

func TestTheRequestDoesNotWaitForTheRestore(t *testing.T) {
	// The handler starts the work and returns and the durable record is the answer: a socket timeout reporting FAILED while the restore ran
	// invites a second destructive run. Asserted on the source so removing the goroutine fails.
	src, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	h := s[strings.Index(s, "func (s *server) backupRestore("):]
	if end := strings.Index(h, "func (s *server) runRestore("); end > 0 {
		h = h[:end]
	}
	if !strings.Contains(h, "http.StatusAccepted") {
		t.Error("the restore request does not return 202; a caller is being made to wait for a 45-minute operation")
	}
	if !strings.Contains(h, "go func()") {
		t.Error("the restore runs inside the request; a client timeout would report a failure that did not happen")
	}
	if !strings.Contains(h, "s.markRestoreRunning(") {
		t.Error("nothing records that a restore is in progress, so the screen would show the PREVIOUS outcome")
	}
	// The worker must not inherit the request context: it ends when the handler returns.
	if !strings.Contains(h, "context.Background()") {
		t.Error("the restore's context is tied to the request, which ends the moment the handler returns")
	}

	// ONE AT A TIME. The maintenance flag is the durable guard but is only raised after the safety dump.
	if !strings.Contains(h, "restoreInFlight.CompareAndSwap(false, true)") {
		t.Error("two concurrent requests could start two restores in the window before maintenance is raised")
	}
	if !strings.Contains(s, "restoreInFlight.Store(false)") {
		t.Error("the single-flight guard is never released; one restore would wedge the appliance forever")
	}

	if !strings.Contains(s, `"running":  false`) {
		t.Error("the finished record does not clear `running`, so a polling screen never stops waiting")
	}
}

func TestVerificationIsBoundToTHEBYTESNotTheirSizeAndTimestamp(t *testing.T) {
	// The marker once recorded only size and mtime, which touch -r restores, so a corrupt backup presented itself as verified. It must hash content.
	dir := t.TempDir()
	defer scdBackupDirForTest(t, dir)()

	name := "db-20260921T090000Z.sql.gz"
	path := filepath.Join(dir, name)
	original := []byte("the contents that were actually verified")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	writeVerifiedMarker(name, 141)

	rec, err := readVerifiedMarker(name)
	if err != nil {
		t.Fatalf("an unchanged verified backup was refused: %v", err)
	}
	if rec.SHA256 == "" {
		t.Fatal("the marker records no digest, so it still vouches for metadata rather than content")
	}
	before, _ := os.Stat(path)

	replaced := []byte("the contents that were NOT verified.....")
	if len(replaced) != len(original) {
		t.Fatalf("the test's own premise is broken: %d vs %d bytes", len(replaced), len(original))
	}
	if err := os.WriteFile(path, replaced, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the test failed to forge size and mtime, so it is not testing what it claims")
	}

	_, err = readVerifiedMarker(name)
	if err == nil {
		t.Fatal("a backup whose CONTENT changed was still accepted for restore; the check is not content-bound")
	}
	if !strings.Contains(err.Error(), "verify it again") {
		t.Errorf("the refusal does not tell the operator what to do: %q", err)
	}
	if !strings.Contains(err.Error(), "size and timestamp are unchanged") {
		t.Errorf("the refusal does not explain why it looks unchanged, which is the confusing part: %q", err)
	}
}

func TestAMarkerWithNoDigestIsNotVerified(t *testing.T) {
	// Markers written before content checks existed carry no sha256. Treating one as verified would be
	// trusting the single field that was never recorded, on the one operation that can lose a hotel's data.
	dir := t.TempDir()
	defer scdBackupDirForTest(t, dir)()

	name := "db-20260921T091111Z.sql.gz"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("an archive from before digests"), 0o640); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	legacy, _ := json.Marshal(map[string]any{
		"verified_at": time.Now().UTC(), "size_bytes": fi.Size(),
		"mod_time": fi.ModTime().UTC(), "tables": 141,
	})
	if err := os.WriteFile(verifiedMarker(name), legacy, 0o640); err != nil {
		t.Fatal(err)
	}

	_, err := readVerifiedMarker(name)
	if err == nil {
		t.Fatal("a pre-digest marker was accepted; what it vouched for cannot be established")
	}
	if !strings.Contains(err.Error(), "verify it again") {
		t.Errorf("the refusal does not tell the operator what to do: %q", err)
	}
}

func TestTheDigestIsCheckedAtTheRestoreBoundaryNotOnlyInTheUI(t *testing.T) {
	// The screen checks too, and the screen is not the security boundary: it can be stale, a second tab, or
	// a curl. Asserted on the source because the property is WHERE the check happens.
	src, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	read := s[strings.Index(s, "func readVerifiedMarker("):]
	if end := strings.Index(read, "\n// ---"); end > 0 {
		read = read[:end]
	}
	if !strings.Contains(read, "fileDigest(full)") {
		t.Error("readVerifiedMarker does not recompute the digest; it is trusting the marker's own claim")
	}

	h := s[strings.Index(s, "func (s *server) backupRestore("):]
	if end := strings.Index(h, "func (s *server) runRestore("); end > 0 {
		h = h[:end]
	}
	gate := strings.Index(h, "readVerifiedMarker(in.Name)")
	start := strings.Index(h, "go func()")
	if gate < 0 {
		t.Fatal("the restore no longer checks verification at all")
	}
	if start >= 0 && gate > start {
		t.Error("verification is checked after the restore has already been started")
	}
}

// restoreSequence returns the body of runRestore: the ordering assertions are about call sites, and helpers are also defined in this file.
func restoreSequence(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "func (s *server) runRestore(")
	if i < 0 {
		t.Fatal("runRestore not found")
	}
	body := s[i:]
	if end := strings.Index(body, "// resetPool replaces"); end > 0 {
		body = body[:end]
	}
	return body
}

func scdBackupDirForTest(t *testing.T, dir string) func() {
	t.Helper()
	old := scdBackupDir
	scdBackupDir = dir
	return func() { scdBackupDir = old }
}

func maintenanceFileForTest(t *testing.T, path string) func() {
	t.Helper()
	old := maintenanceFile
	maintenanceFile = path
	return func() { maintenanceFile = old }
}

var _ = json.Marshal

func TestTheAccessRulesTravelWithTheRestore(t *testing.T) {
	// Backups are --no-owner --no-privileges, so without the privilege replay the restored database is superuser-owned with PUBLIC EXECUTE on
	// every function and the services cannot work.
	s := restoreSequence(t)

	capture := strings.Index(s, "capturePrivileges(ctx, pgDatabase)")
	dump := strings.Index(s, "s.takeBackup(ctx,")
	apply := strings.Index(s, "applyPrivileges(ctx, pgDatabase")
	swap := strings.Index(s, "RENAME TO \" + pgDatabase")
	if capture < 0 || apply < 0 {
		t.Fatal("the restore does not carry the database's access rules across the swap")
	}
	if capture > dump {
		t.Error("the access rules are captured after the safety dump; the read that costs nothing should come first")
	}
	if apply < swap {
		t.Error("the access rules are replayed before the swap, onto the database being replaced")
	}

	// Replaying is half the job. A replay that half-worked leaves an appliance that fails later.
	if !strings.Contains(s, "privilegesMatch(privileges, after)") {
		t.Error("the replay is never verified; a partial replay would be indistinguishable from success")
	}
	if !strings.Contains(s, `rollback("privileges"`) {
		t.Error("a failed privilege replay does not roll back, leaving an appliance that cannot serve")
	}

	// The revokes are the half that is easy to forget: an absent privilege is as deliberate as a present one.
	src, err := os.ReadFile("privileges.go")
	if err != nil {
		t.Fatal(err)
	}
	p := string(src)
	for _, needed := range []string{
		"REVOKE ALL ON FUNCTION ", "REVOKE ALL ON SCHEMA ", "REVOKE ALL ON TABLE ",
	} {
		if !strings.Contains(p, needed) {
			t.Errorf("the capture emits no %q; PUBLIC would keep the default EXECUTE the migrations revoke", needed)
		}
	}
	if !strings.Contains(p, "ALTER FUNCTION ") {
		t.Error("function ownership is not captured; the owner is the grantor recorded in every acl entry")
	}
	if !strings.Contains(p, "ON_ERROR_STOP=1") {
		t.Error("a privilege replay that continues past an error produces a half-privileged database")
	}
}

func TestColumnPrivilegesAreCarriedToo(t *testing.T) {
	// Column privileges live in pg_attribute.attacl, not nspacl/relacl/proacl; a capture is only as complete as the catalogs it reads.
	// The self-check was blind to the same gap.
	src, err := os.ReadFile("privileges.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	if !strings.Contains(s, "pg_attribute") || !strings.Contains(s, "att.attacl") {
		t.Error("column privileges are not captured; a column grant would be silently dropped by a restore")
	}
	if !strings.Contains(s, "GRANT ' || a.privilege_type || ' (' || quote_ident(att.attname) || ') ON TABLE ") {
		t.Error("the column grant is not emitted in PostgreSQL's GRANT <priv> (<col>) ON TABLE form")
	}
	// Dropped columns keep a row in pg_attribute; replaying a grant for one would fail the whole replay.
	if !strings.Contains(s, "NOT att.attisdropped") {
		t.Error("dropped columns are not excluded; the replay would fail on a table that ever lost a column")
	}

	// AND WHAT IT STILL CANNOT CARRY MUST REFUSE, NOT PROCEED.
	if !strings.Contains(s, "unhandledPrivilegeSQL") {
		t.Fatal("nothing checks for privilege kinds the capture cannot reproduce")
	}
	for _, kind := range []string{"pg_default_acl", "typacl", "pg_policy"} {
		if !strings.Contains(s, kind) {
			t.Errorf("%s is neither carried nor refused; it would be lost in silence", kind)
		}
	}

	rsrc, _ := os.ReadFile("restore.go")
	seq := string(rsrc)
	seq = seq[strings.Index(seq, "func (s *server) runRestore("):]
	guard := strings.Index(seq, "unhandledPrivileges(ctx, pgDatabase)")
	capture := strings.Index(seq, "capturePrivileges(ctx, pgDatabase)")
	dump := strings.Index(seq, "s.takeBackup(ctx,")
	if guard < 0 {
		t.Fatal("the restore does not refuse privilege kinds it cannot carry")
	}
	if guard > capture || guard > dump {
		t.Error("the refusal happens after the restore has already started doing work")
	}
}
