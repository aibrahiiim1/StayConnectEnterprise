package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment/assignmenttest"
)

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
