package main

import (
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment/assignmenttest"
)

func scopeEnv(f *assignmenttest.Fixture, extra map[string]string) func(string) string {
	m := map[string]string{
		"EDGED_IDENTITY_DIR":             f.IdentityDir,
		"EDGED_ASSIGNMENT_DIR":           f.Paths.Dir,
		"EDGED_ASSIGNMENT_REGISTRY":      f.Paths.RegistryPath,
		"EDGED_ASSIGNMENT_REGISTRY_ROOT": f.Paths.RegistryRootPath,
		"EDGED_ASSIGNMENT_TRUST":         f.Paths.TrustPath,
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

// F5/F6: Hotel Admin's tenant/site is the VERIFIED assignment's, and an env pair never stands in for one.
func TestResolveSiteScope(t *testing.T) {
	envTenant := map[string]string{"EDGED_TENANT_ID": "env-tenant", "EDGED_SITE_ID": "env-site"}

	good := assignmenttest.New(t)
	good.WriteAssignment(t, good.Doc(assignment.StateAssigned, 1))
	if ten, site, _ := resolveSiteScope(scopeEnv(good, envTenant)); ten != assignmenttest.TenantID || site != assignmenttest.SiteID {
		t.Fatalf("verified assignment must win, got %q/%q", ten, site)
	}

	rogue := assignmenttest.New(t)
	rogue.WriteAssignment(t, rogue.Rogue(1))
	ten, site, r := resolveSiteScope(scopeEnv(rogue, envTenant))
	if ten != "" || site != "" || r.Outcome != assignment.OutcomeUnverifiable {
		t.Fatalf("an unverifiable assignment must yield NO tenant (never the env pair): %q/%q %v", ten, site, r.Outcome)
	}

	revoked := assignmenttest.New(t)
	revoked.WriteAssignment(t, revoked.Doc(assignment.StateRevoked, 2))
	if ten, _, _ := resolveSiteScope(scopeEnv(revoked, envTenant)); ten != "" {
		t.Fatal("a revoked assignment must yield no tenant")
	}

	// Factory clean + env pair: the default build is production, which never uses the env scope.
	clean := assignmenttest.New(t)
	e := scopeEnv(clean, envTenant)
	cleanEnv := func(k string) string {
		if k == "EDGED_ASSIGNMENT_DIR" {
			return filepath.Join(clean.Root, "no-assignment-here")
		}
		return e(k)
	}
	if ten, _, r := resolveSiteScope(cleanEnv); ten != "" || r.Outcome != assignment.OutcomeAbsent {
		t.Fatalf("production build must not take tenant from env: %q %v", ten, r.Outcome)
	}
}
