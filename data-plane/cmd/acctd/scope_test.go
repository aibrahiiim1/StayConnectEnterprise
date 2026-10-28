package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment/assignmenttest"
)

// Audit P6 L13: an assignment that changed after acctd resolved its scope must not become the watcher's
// baseline, or acctd would never re-execute and would keep billing the previous customer.
func TestWatchBaselineIsTheResolvedVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "assignment.json"),
		[]byte(`{"current":{"assignment_id":"a2","version":9},"version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &assignment.Store{Dir: dir}
	if got := watchBaseline(5, store); got != 5 {
		t.Fatalf("baseline = %d, want the resolved version 5 (the change to 9 must trigger a re-exec)", got)
	}
	// With no verified version (development fallback) the stored record is the baseline, so the watcher does
	// not re-execute forever.
	if got := watchBaseline(0, store); got != 9 {
		t.Fatalf("fallback baseline = %d, want 9", got)
	}
}

func acctdEnv(f *assignmenttest.Fixture) func(string) string {
	m := map[string]string{
		"ACCTD_IDENTITY_DIR":             f.IdentityDir,
		"ACCTD_ASSIGNMENT_DIR":           f.Paths.Dir,
		"ACCTD_ASSIGNMENT_REGISTRY":      f.Paths.RegistryPath,
		"ACCTD_ASSIGNMENT_REGISTRY_ROOT": f.Paths.RegistryRootPath,
		"ACCTD_ASSIGNMENT_TRUST":         f.Paths.TrustPath,
		"ACCTD_TENANT_ID":                "env-tenant",
		"ACCTD_APPLIANCE_ID":             "env-appliance",
	}
	return func(k string) string { return m[k] }
}

// F5/F6: usage is attributed to the VERIFIED assignment's tenant; an unverifiable one bills nobody, and the env
// pair never stands in for it.
func TestResolveAcctdScope(t *testing.T) {
	good := assignmenttest.New(t)
	good.WriteAssignment(t, good.Doc(assignment.StateAssigned, 4))
	s := resolveAcctdScope(acctdEnv(good))
	if s.TenantID != assignmenttest.TenantID || s.ApplianceID != good.ApplianceID || s.Version != 4 {
		t.Fatalf("verified scope expected, got %+v", s)
	}

	rogue := assignmenttest.New(t)
	rogue.WriteAssignment(t, rogue.Rogue(4))
	s = resolveAcctdScope(acctdEnv(rogue))
	if s.TenantID != "" || s.Outcome != assignment.OutcomeUnverifiable {
		t.Fatalf("an unverifiable assignment must bill nobody (and never the env tenant): %+v", s)
	}

	clean := assignmenttest.New(t) // registered, never assigned
	s = resolveAcctdScope(acctdEnv(clean))
	if s.TenantID != "" || s.Outcome != assignment.OutcomeAbsent {
		t.Fatalf("the production build must not take the tenant from env: %+v", s)
	}
}
