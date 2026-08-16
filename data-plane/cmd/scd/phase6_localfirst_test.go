package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LOCAL-FIRST, ASSERTED STRUCTURALLY.
//
// The integration test proves the flow WORKS with Central unreachable. That is a behavioural proof and it is
// the important one, but it has a blind spot: a dependency that is merely optional, cached, or tried on a
// background timer would not show up in a run that completes in under a second. This asserts the stronger,
// static property -- the Phase-6 code cannot reach the Control Plane, because nothing in its import graph
// knows how to.
//
// The device self-service package is held to the strictest form of that rule: it may not import ANY network
// package at all. It speaks to a local connection pool and to nothing else, and if that ever changes it will
// change here first.

// centralFacing lists the import paths that mean "this talks to something off the appliance". They are the
// packages scd itself uses for exactly that, so the list is not hypothetical.
var centralFacing = []string{
	"internal/updates", "internal/assignment", "internal/commands",
	"internal/applianceauth", "internal/appliancecert",
	"nats-io", "internal/outbox",
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, im := range f.Imports {
		out = append(out, strings.Trim(im.Path.Value, `"`))
	}
	return out
}

// The Phase-6 surfaces, in all three services, import nothing that reaches the Control Plane.
func TestPhase6SurfacesImportNothingCentralFacing(t *testing.T) {
	files := []string{
		"phase6_device.go",
		filepath.Join("..", "edged", "resources_phase6_setting.go"),
		filepath.Join("..", "portald", "phase6_device_handlers.go"),
	}
	for _, rel := range files {
		if _, err := os.Stat(rel); err != nil {
			t.Fatalf("a Phase-6 surface is missing: %v", err)
		}
		for _, im := range importsOf(t, rel) {
			for _, bad := range centralFacing {
				if strings.Contains(im, bad) {
					t.Errorf("%s imports %s: the guest capability must work with no Control Plane", rel, im)
				}
			}
		}
	}
}

// THE DURABLE CORE TALKS TO THE LOCAL DATABASE AND TO NOTHING ELSE. No net, no http, no client of any kind.
func TestDeviceSelfServicePackageHasNoNetworkDependencyAtAll(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "deviceselfservice")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		seen++
		for _, im := range importsOf(t, filepath.Join(dir, name)) {
			switch {
			case im == "net" || strings.HasPrefix(im, "net/"):
				t.Errorf("%s imports %s; this package must reach nothing but the local pool", name, im)
			case strings.Contains(im, "nats-io"), strings.Contains(im, "internal/updates"),
				strings.Contains(im, "internal/assignment"), strings.Contains(im, "internal/outbox"):
				t.Errorf("%s imports %s; the setting and the release must not depend on Central", name, im)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no source files were examined, so this proves nothing")
	}
}

// The per-appliance setting is read from the LOCAL database on the request path -- not from a cache
// populated by Central, and not from a config file Central writes.
func TestTheSettingIsReadFromTheLocalDatabaseOnEveryRequest(t *testing.T) {
	src, err := os.ReadFile("phase6_device.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "p.svc.EnabledForAppliance(ctx") {
		t.Fatal("the request path does not read the setting")
	}
	// A cached copy would be the natural "optimisation" that quietly reintroduces a Central dependency, so
	// its absence is asserted rather than assumed.
	for _, cached := range []string{"cachedSetting", "settingCache", "lastKnownSetting"} {
		if strings.Contains(body, cached) {
			t.Fatalf("the setting is served from %s rather than the local database", cached)
		}
	}
}
