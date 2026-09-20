package main

// THE SAFETY PROPERTIES OF THE ONE OPERATION THAT CAN LOSE A HOTEL'S DATA.
//
// These are not tests of whether a restore works — that is proven on the appliance, against a real database,
// because a restore that only works against a mock is not evidence of anything. These pin the properties
// that make it safe to attempt at all, each of which is a decision that could be quietly reversed by a later
// edit that looked like a simplification.

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

	// UNVERIFIED IS REFUSED. The screen also checks, but the screen is not the security boundary.
	if _, err := readVerifiedMarker(name); err == nil {
		t.Fatal("an unverified backup was accepted for restore")
	}

	// A verification makes it eligible.
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
	// "This NAME was verified once" is not the same claim as "these BYTES were verified". A file replaced
	// under the same name — a re-run backup, a copy dropped in by hand, a partial write — must lose its
	// verification rather than inherit it.
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

	// Same name, different bytes.
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

// THE ORDER OF OPERATIONS IS THE SAFETY MODEL.
//
// Asserted on the source because the sequence is the property: a load into STAGING before any swap, a rename
// rather than a drop, an integrity check after, and a rollback on both failure paths. A future edit that
// "simplified" this into drop-and-load would pass every behavioural test and lose a hotel's data the first
// time a dump was truncated.
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
		{"the safety dump", "s.dumpTo(ctx,"},
		{"maintenance on", "maintenanceOn("},
		{"the staging load", "-d \"+staging+\""},
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

	// Both failure paths must put the original back.
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
	safety := strings.Index(s, "s.dumpTo(ctx,")
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
	write := strings.Index(s, "os.WriteFile(restoreResultFile")
	restart := strings.Index(s, "func (s *server) startDependents")
	if write < 0 {
		t.Fatal("the restore outcome is never written durably")
	}
	if restart < 0 {
		t.Fatal("dependent services are never restarted")
	}
	if !strings.Contains(s, "finishRestore") {
		t.Error("the outcome is not funnelled through one place")
	}
}

func TestDatabaseNamesSurviveIdentifierFolding(t *testing.T) {
	// FOUND ON THE APPLIANCE, not here, which is why it is pinned here now.
	//
	// The staging name was built from an RFC3339-ish stamp containing `T` and `Z`. `CREATE DATABASE
	// restore_20260920T090146Z` created `restore_20260920t090146z` -- SQL folds unquoted identifiers -- and
	// the very next command, `psql -d restore_20260920T090146Z`, was told no such database existed, because
	// libpq passes a dbname through verbatim. The restore failed at the load step complaining about a
	// database it had just successfully created.
	//
	// It failed safely: the live database had not been opened. But "safe" is not "working", and the next
	// person to reintroduce an uppercase character in a database name would get the same hour back.
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

// ---- helpers --------------------------------------------------------------------------------------------

// restoreSequence returns the BODY of backupRestore.
//
// Scoped to the function rather than the file because the ordering assertions are about CALL SITES: every
// helper is also DEFINED in this file, and matching a definition would have the test asserting the order
// things were written in rather than the order they happen in.
func restoreSequence(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "func (s *server) backupRestore(")
	if i < 0 {
		t.Fatal("backupRestore not found")
	}
	body := s[i:]
	if end := strings.Index(body, "// startDependents brings"); end > 0 {
		body = body[:end]
	}
	return body
}

// scdBackupDirForTest points the backup directory at a temporary one for the duration of a test.
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
