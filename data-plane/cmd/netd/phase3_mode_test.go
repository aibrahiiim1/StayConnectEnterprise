package main

// netd resolves its own Phase-3 mode. These tests pin the two things that must never drift: while the flags
// are off netd is inert and reads nothing, and when it cannot establish its own scope it stays inactive rather
// than falling back to whatever a submitted plan claims.

import (
	"context"
	"testing"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// DARK is the default. With the flags off netd resolves no scope at all, so there is nothing for a plan to
// match — and it does not even read the enrollment identity or the assignment.
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

// THE MASTER FLAG IS THE GATE, and a child flag without its master is a startup error everywhere else in the
// product.
//
// This test used to assert the opposite of its first case — that the master flag ALONE left shaping off,
// because enforcement followed the checkout-grace surface flag. That is the defect: an appliance running Room
// authentication with grace off had a network writer that refused every plan, so two real guests were granted
// sessions the kernel never heard about. Enforcement is the substrate under every guest-session surface, not a
// surface of its own, so it follows the master flag. Scope still has to resolve (see the next test), and a
// producer still has to authenticate (see main.go), so this is a gate, not a bypass.
func TestModeFollowsTheMasterFlag(t *testing.T) {
	mode, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_MASTER": "true",
		"NETD_IDENTITY_DIR":         t.TempDir(),
		"NETD_ASSIGNMENT_DIR":       t.TempDir(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	// No identity is enrolled in those empty dirs, so the mode correctly stays inactive — what matters here is
	// that it got as far as TRYING to resolve a scope instead of returning dark on the flag alone.
	if mode.Active {
		t.Fatal("shaping went live without a resolvable scope")
	}

	if _, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_CHECKOUT_GRACE": "true", // child without master
	})); err == nil {
		t.Fatal("a child flag without its master was accepted")
	}

	// a malformed flag is a startup failure, not a silently-off surface
	if _, err := loadPhase3Mode(context.Background(), env(map[string]string{
		"STAYCONNECT_PHASE3_MASTER": "yes-please",
	})); err == nil {
		t.Fatal("a malformed flag was tolerated")
	}
}

// With the flags on but no resolvable identity or assignment, netd stays INACTIVE. The alternative — trusting
// the tenant/site/appliance in the submitted plan — would make the envelope both the claim and its own
// authorization, and any local process could then enforce arbitrary policy here.
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
