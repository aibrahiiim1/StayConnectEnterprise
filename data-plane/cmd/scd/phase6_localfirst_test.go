package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Local-first asserted structurally: the device self-service code cannot reach the Control Plane because
// nothing in its import graph can (a behavioural run misses optional, cached or background-timer dependencies).
// The deviceselfservice package may import no network package at all.

// centralFacing lists the import paths that mean talking to something off the appliance.
var centralFacing = []string{
	"internal/assignment", "internal/applianceauth", "internal/appliancecert",
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
			case strings.Contains(im, "internal/assignment"), strings.Contains(im, "internal/applianceauth"),
				strings.Contains(im, "internal/appliancecert"):
				t.Errorf("%s imports %s; the setting and the release must not depend on Central", name, im)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no source files were examined, so this proves nothing")
	}
}

func TestTheSettingIsReadFromTheLocalDatabaseOnEveryRequest(t *testing.T) {
	src, err := os.ReadFile("phase6_device.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "p.svc.EnabledForAppliance(ctx") {
		t.Fatal("the request path does not read the setting")
	}
	// A cached copy would quietly reintroduce a Central dependency.
	for _, cached := range []string{"cachedSetting", "settingCache", "lastKnownSetting"} {
		if strings.Contains(body, cached) {
			t.Fatalf("the setting is served from %s rather than the local database", cached)
		}
	}
}
