package main

import (
	"context"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment/assignmenttest"
)

// The scope comes from the verified assignment; a document signed outside the trust registry is refused.
func TestModeRequiresAVerifiedAssignment(t *testing.T) {
	envFor := func(f *assignmenttest.Fixture) func(string) string {
		return env(map[string]string{
			"STAYCONNECT_PHASE3_MASTER":     "true",
			"NETD_IDENTITY_DIR":             f.IdentityDir,
			"NETD_ASSIGNMENT_DIR":           f.Paths.Dir,
			"NETD_ASSIGNMENT_REGISTRY":      f.Paths.RegistryPath,
			"NETD_ASSIGNMENT_REGISTRY_ROOT": f.Paths.RegistryRootPath,
			"NETD_ASSIGNMENT_TRUST":         f.Paths.TrustPath,
		})
	}
	good := assignmenttest.New(t)
	good.WriteAssignment(t, good.Doc(assignment.StateAssigned, 3))
	mode, err := loadPhase3Mode(context.Background(), envFor(good))
	if err != nil || !mode.Active || mode.TenantID != assignmenttest.TenantID || mode.AssignGen != 3 ||
		mode.ApplianceID != good.ApplianceID {
		t.Fatalf("a verified assignment must activate the scope: %+v err=%v", mode, err)
	}

	rogue := assignmenttest.New(t)
	rogue.WriteAssignment(t, rogue.Rogue(3))
	mode, err = loadPhase3Mode(context.Background(), envFor(rogue))
	if err != nil || mode.Active || mode.TenantID != "" {
		t.Fatalf("an unverifiable assignment must leave netd inactive with no scope: %+v err=%v", mode, err)
	}
}

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// With the flags off netd resolves no scope and reads neither identity nor assignment.
func TestModeIsDarkByDefault(t *testing.T) {
	mode, err := loadPhase3Mode(context.Background(), env(nil))
	if err != nil {
		t.Fatalf("the default flag set failed to load: %v", err)
	}
	if mode.Active {
		t.Fatal("Phase-3 shaping was active with no flags set")
	}
	if mode.TenantID != "" || mode.SiteID != "" || mode.ApplianceID != "" {
		t.Fatalf("a dark netd resolved a scope: %+v", mode)
	}
}

// Enforcement follows the master flag, not a surface flag such as checkout grace; scope must still resolve.
func TestModeFollowsTheMasterFlag(t *testing.T) {
	mode, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_MASTER": "true",
		"NETD_IDENTITY_DIR":         t.TempDir(),
		"NETD_ASSIGNMENT_DIR":       t.TempDir(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Empty dirs enroll no identity, so it stays inactive.
	if mode.Active {
		t.Fatal("shaping went live without a resolvable scope")
	}

	if _, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_CHECKOUT_GRACE": "true", // child without master
	})); err == nil {
		t.Fatal("a child flag without its master was accepted")
	}

	if _, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_MASTER": "yes-please",
	})); err == nil {
		t.Fatal("a malformed flag was tolerated")
	}
}

// Without a resolvable scope netd stays inactive rather than trusting the plan's own tenant/site/appliance.
func TestLiveFlagsWithoutScopeStayInactive(t *testing.T) {
	dir := t.TempDir()
	mode, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_MASTER":         "true",
		"STAYCONNECT_PHASE3_CHECKOUT_GRACE": "true",
		"NETD_IDENTITY_DIR":                 dir + "/identity",
		"NETD_ASSIGNMENT_DIR":               dir + "/assignment",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if mode.Active {
		t.Fatalf("shaping went live without a resolvable scope: %+v", mode)
	}
}
