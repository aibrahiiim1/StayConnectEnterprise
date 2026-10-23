package assignment

// Regression: Resolve returned a bare Resolution{} both for a factory-clean appliance and for a document that failed verification, so pmsd (switching
// on State == "") treated a bad signature as a new box. Asserted on Outcome, since asserting TenantID passes whether or not the bug exists.

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
	// A file that EXISTS and cannot be parsed must not read as "not assigned yet": it hides a corrupt or tampered assignment.
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
	// A well-formed document with no trust anchor to judge it by is unverifiable, not absent and certainly not granting.
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
	// scd reads State/Version and has always seen them empty for anything that did not verify.
	if got.State != "" || got.Version != 0 {
		t.Fatalf("unverified documents must not surface State/Version (scd contract): %+v", got)
	}
}
