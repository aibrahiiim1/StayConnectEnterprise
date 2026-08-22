package assignment

// The regression this file exists for: Resolve returned a bare Resolution{} for a factory-clean appliance AND
// for a document that failed verification. Both have empty TenantID and empty State, so a caller inspecting
// the fields could not tell "never assigned" from "assignment refused" — and pmsd, which switched on
// State == "", took the factory-clean branch for a bad signature and exited 0 as though the box were new.
//
// These cases are asserted on Outcome specifically. Asserting on TenantID would pass whether or not the bug
// is present, which is what let it through the first time.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolve_AbsentIsNotUnverifiable(t *testing.T) {
	dir := t.TempDir()
	p := Paths{
		Dir:              dir,
		RegistryPath:     filepath.Join(dir, "registry.json"),
		RegistryRootPath: filepath.Join(dir, "root.pub"),
		TrustPath:        filepath.Join(dir, "trust.json"),
	}
	got := Resolve(p, ApplianceBinding{ApplianceID: "appl-1", Serial: "SC-1"}, time.Now(), nil)
	if got.Outcome != OutcomeAbsent {
		t.Fatalf("no assignment file must be Absent, got %v", got.Outcome)
	}
	if got.Assigned() || got.Refused() {
		t.Fatalf("factory-clean must be neither assigned nor refused: %+v", got)
	}
}

func TestResolve_UnreadableAssignmentIsRefusedNotAbsent(t *testing.T) {
	dir := t.TempDir()
	// A file that EXISTS and cannot be parsed. Treating this as "not assigned yet" would hide a corrupt or
	// tampered assignment behind the most routine-looking state the system has.
	if err := os.WriteFile(filepath.Join(dir, "assignment.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Paths{
		Dir:              dir,
		RegistryPath:     filepath.Join(dir, "registry.json"),
		RegistryRootPath: filepath.Join(dir, "root.pub"),
		TrustPath:        filepath.Join(dir, "trust.json"),
	}
	got := Resolve(p, ApplianceBinding{ApplianceID: "appl-1", Serial: "SC-1"}, time.Now(), nil)
	if got.Outcome != OutcomeUnverifiable {
		t.Fatalf("an unreadable assignment must be Unverifiable, got %v", got.Outcome)
	}
	if !got.Refused() || got.Assigned() {
		t.Fatalf("an unreadable assignment must be refused: %+v", got)
	}
}

func TestResolve_NoTrustAnchorRefusesRatherThanReportsAbsent(t *testing.T) {
	dir := t.TempDir()
	// A well-formed document with no trust anchor anywhere to judge it by. There is nothing to verify it
	// against, so it is unverifiable — not absent, and certainly not granting.
	doc := `{"assignment_id":"a1","appliance_id":"appl-1","serial":"SC-1",` +
		`"tenant_id":"36f3ba78-f8d8-41b6-aa2e-50d797e2d9c8","site_id":"0d40f7c8-4a98-428a-a2f5-74f0cabf5d42",` +
		`"version":1,"state":"assigned","signer_key_id":"deadbeef","signature":"AA=="}`
	if err := os.WriteFile(filepath.Join(dir, "assignment.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Paths{
		Dir:              dir,
		RegistryPath:     filepath.Join(dir, "registry.json"),
		RegistryRootPath: filepath.Join(dir, "root.pub"),
		TrustPath:        filepath.Join(dir, "trust.json"),
	}
	got := Resolve(p, ApplianceBinding{ApplianceID: "appl-1", Serial: "SC-1"}, time.Now(), nil)
	if got.Outcome == OutcomeAbsent {
		t.Fatal("a present document with no trust anchor must not be reported as factory-clean")
	}
	if !got.Refused() {
		t.Fatalf("a present document with no trust anchor must be refused, got %v", got.Outcome)
	}
	// scd reads State/Version and has always seen them empty for anything that did not verify. Keeping that
	// true is what makes this change invisible to scd.
	if got.State != "" || got.Version != 0 {
		t.Fatalf("unverified documents must not surface State/Version (scd contract): %+v", got)
	}
}
