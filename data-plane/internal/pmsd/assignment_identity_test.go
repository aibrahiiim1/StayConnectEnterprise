package pmsd

// Every case asserts on the error, not on `assigned`: all four states report assigned=false, so that flag
// alone cannot tell a damaged identity from a factory-clean one.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// loaderFor leaves the assignment dir empty so only the identity gate decides the outcome.
func loaderFor(t *testing.T, identityDir string) func(context.Context) (Assignment, bool, error) {
	t.Helper()
	adir := t.TempDir()
	return CentralAssignmentLoader(assignment.Paths{
		Dir:              adir,
		RegistryPath:     filepath.Join(adir, "registry.json"),
		RegistryRootPath: filepath.Join(adir, "root.pub"),
		TrustPath:        filepath.Join(adir, "trust.json"),
	}, identityDir, nil)
}

func TestCentralAssignmentLoader_AbsentIdentityIsFactoryClean(t *testing.T) {
	_, assigned, err := loaderFor(t, t.TempDir())(context.Background())
	if err != nil {
		t.Fatalf("a missing identity.json is factory-clean, not an error: %v", err)
	}
	if assigned {
		t.Fatal("a factory-clean appliance must not resolve a scope")
	}
}

func TestCentralAssignmentLoader_CorruptIdentityIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, assigned, err := loaderFor(t, dir)(context.Background())
	if !errors.Is(err, ErrIdentityUnreadable) {
		t.Fatalf("a corrupt identity.json must fail with ErrIdentityUnreadable, got %v", err)
	}
	if assigned {
		t.Fatal("a corrupt identity must not resolve a scope")
	}
}

func TestCentralAssignmentLoader_EmptyIdentityIsAnError(t *testing.T) {
	dir := t.TempDir()
	// Valid JSON with neither an appliance id nor a public key is a damaged file.
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, assigned, err := loaderFor(t, dir)(context.Background())
	if !errors.Is(err, ErrIdentityUnreadable) {
		t.Fatalf("an identity with no id and no key must fail with ErrIdentityUnreadable, got %v", err)
	}
	if assigned {
		t.Fatal("an invalid identity must not resolve a scope")
	}
}

func TestCentralAssignmentLoader_AwaitingEnrolmentIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	// Pre-enrolment: keypair exists, no appliance id yet. Ordinary, and distinct from the corrupt cases.
	body := `{"appliance_id":"","serial":"SC-TEST","public_key":"bm90LWEtcmVhbC1rZXk"}`
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, assigned, err := loaderFor(t, dir)(context.Background())
	if err != nil {
		t.Fatalf("awaiting enrolment is not a fault: %v", err)
	}
	if assigned {
		t.Fatal("an un-enrolled appliance must not resolve a scope")
	}
}

func TestCentralAssignmentLoader_EnrolledButUnassignedIsFactoryClean(t *testing.T) {
	dir := t.TempDir()
	// Enrolled but no assignment adopted: OutcomeAbsent is factory-clean, not a refusal.
	body := `{"appliance_id":"appl-1","serial":"SC-TEST","public_key":"bm90LWEtcmVhbC1rZXk"}`
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, assigned, err := loaderFor(t, dir)(context.Background())
	if err != nil {
		t.Fatalf("an enrolled appliance with no assignment is factory-clean, not an error: %v", err)
	}
	if assigned {
		t.Fatal("no assignment means no scope")
	}
}
