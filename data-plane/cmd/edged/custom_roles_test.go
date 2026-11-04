package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The feature list a custom role chooses from is exactly the set of resources edged enforces (roles itself excepted): a new
// resource cannot be left out of the editor, and the editor cannot offer something nothing checks.
func TestCustomRoleFeaturesAreEveryEnforcedResource(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`mountResource\(r, s, "([a-z0-9-]+)"|requireRole\("([a-z0-9-]+)"|resourcePermission\("([a-z0-9-]+)"|permFor\([^,]+, "([a-z0-9-]+)"`)
	enforced := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			for _, g := range m[1:] {
				if g != "" {
					enforced[g] = true
				}
			}
		}
	}
	delete(enforced, "roles")
	offered := map[string]bool{}
	for _, f := range roleFeatures {
		if offered[f.Key] {
			t.Errorf("%s is offered twice", f.Key)
		}
		offered[f.Key] = true
		if f.Label == "" || f.Group == "" || f.Help == "" {
			t.Errorf("%s needs a label, a group and a description", f.Key)
		}
	}
	for k := range enforced {
		if !offered[k] {
			t.Errorf("edged enforces %q but the role editor does not offer it", k)
		}
	}
	for k := range offered {
		if !enforced[k] {
			t.Errorf("the role editor offers %q, which nothing enforces", k)
		}
	}
}

// No custom role can do what only Site admin does today; the cap also holds for a row edited in the database.
func TestACustomRoleNeverExceedsTheBuiltInRoles(t *testing.T) {
	for _, k := range []string{"operators", "modules", "pms-financial-onboarding", "pms-routing", "pms-reconciliation", "license"} {
		if maxGrantable(k) == permWrite {
			t.Errorf("%s can be managed by a custom role, but only Site admin manages it today", k)
		}
	}
	if maxGrantable("roles") != permNone {
		t.Error("a custom role could hold role management")
	}
	got := cappedPerms(map[string]string{"modules": "write", "sessions": "write", "roles": "write", "nonsense": "write", "usage": "admin"})
	if got["modules"] != permRead || got["sessions"] != permWrite || got["roles"] != permNone || len(got) != 2 {
		t.Errorf("capped = %v, want modules read and sessions write only", got)
	}
	if _, problem := validateRole("Night desk", "", map[string]string{"operators": "write"}); problem == "" {
		t.Error("a role managing staff accounts was accepted")
	}
	if _, problem := validateRole("Site IT manager", "", map[string]string{"usage": "read"}); problem == "" {
		t.Error("a role named like a built-in one was accepted")
	}
}

// permFor answers a custom role from the loaded set, and only up to its level.
func TestPermForReadsCustomRoles(t *testing.T) {
	prev := customRoleCache.Load()
	t.Cleanup(func() { customRoleCache.Store(prev) })
	m := map[string]cachedRole{"custom:r1": {name: "Night desk", perms: map[string]perm{"sessions": permWrite, "usage": permRead}}}
	customRoleCache.Store(&m)
	roles := []string{"custom:r1"}
	if !permFor(roles, "sessions", permWrite) || !permFor(roles, "usage", permRead) {
		t.Error("granted features refused")
	}
	if permFor(roles, "usage", permWrite) || permFor(roles, "vouchers", permRead) || permFor(roles, "roles", permRead) {
		t.Error("a feature beyond the role was allowed")
	}
	if permFor([]string{"custom:unknown"}, "sessions", permRead) {
		t.Error("an unknown custom role was allowed")
	}
}
