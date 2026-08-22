package pmsd

// The regression: CentralAssignmentLoader collapsed every identity failure into the factory-clean answer —
//
//	if err != nil || ident == nil || ident.ApplianceID == "" { return Assignment{}, false, nil }
//
// so an unreadable or tampered identity.json produced assigned=false with no error, pmsd logged "no
// assignment" and exited 0, and a damaged appliance was indistinguishable from one that had never been
// enrolled. identity.Store.LoadPublic already separates the two cases; this branch threw the distinction away.
//
// Every case below asserts on the ERROR, not on `assigned`. All four states report assigned=false, so a test
// that checked only that flag would pass against the bug — which is precisely how it survived review.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// loaderFor builds the loader over two temp dirs. The assignment side is deliberately left empty: these tests
// are about the IDENTITY gate, which runs first, and an empty assignment dir keeps the assignment layer from
// deciding the outcome for us.
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
	// The file EXISTS and does not parse. This is the case that used to be reported as factory-clean.
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
	// Valid JSON carrying neither an appliance id nor a public key. A pre-enrolment identity always has the
	// public half, so this is a damaged file wearing a well-formed shape — the kind of corruption that parses.
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
	// A real pre-enrolment identity: the keypair exists, Central has not yet minted an appliance id. This is
	// an ordinary state of a new appliance and must stay distinct from the corrupt cases above.
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
	// Enrolled (an appliance id exists) but no assignment has been adopted. The identity gate passes and the
	// assignment layer reports OutcomeAbsent, which is factory-clean rather than a refusal — this is the
	// boundary between the two classifications, so it is asserted rather than assumed.
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
