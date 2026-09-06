package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runFn/outFn are TEST SEAMS. They are nil in production, where a.run/a.output fall back to real exec.
//
// Calling them directly compiles, passes every test that sets them, and panics with a nil pointer the
// first time it runs on an appliance. That is exactly what happened: the Kea bootstrap called a.runFn
// directly, panicked inside an HTTP handler, and chi's Recoverer turned it into a 500 — so the apply never
// reached its own error path and the revision sat in "applying" with no event and no rollback.
//
// A grep is a blunt guard, but this class of bug is invisible to every other kind of test we have here.
func TestShellSeamsAreNeverCalledDirectly(t *testing.T) {
	direct := regexp.MustCompile(`\ba\.(runFn|outFn)\(`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			// The wrapper definitions themselves are the one legitimate place.
			if strings.Contains(line, "if a.runFn != nil") || strings.Contains(line, "if a.outFn != nil") ||
				strings.Contains(line, "return a.runFn(ctx, name, args...)") ||
				strings.Contains(line, "return a.outFn(ctx, name, args...)") {
				continue
			}
			if direct.MatchString(line) {
				t.Errorf("%s:%d calls the test seam directly — use a.run/a.output, which are non-nil in "+
					"production:\n    %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// And the wrappers really do tolerate a nil seam, which is the property the rule depends on.
func TestRunWrapperToleratesNilSeam(t *testing.T) {
	a := &applier{} // no runFn, no outFn — production shape
	// A command that does not exist returns an error; the point is that it does not panic.
	if err := a.run(context.Background(), "stayconnect-no-such-binary-xyz"); err == nil {
		t.Fatal("expected an error from a missing binary")
	}
	if _, err := a.output(context.Background(), "stayconnect-no-such-binary-xyz"); err == nil {
		t.Fatal("expected an error from a missing binary")
	}
}
