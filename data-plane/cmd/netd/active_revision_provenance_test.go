package main

// THE ACTIVE REVISION IS ONE THE APPLIANCE ACTUALLY APPLIED, AND THERE IS NO SECOND WAY IN.
//
// netd's boot reconcile treats the ACTIVE revision's intent snapshot as the truth about what this appliance is
// supposed to be forwarding, and rollback rebuilds from it. So "active" has to mean "this configuration was
// rendered, put on the wire, health-checked and kept" -- not "somebody said so".
//
// It did not always mean that. POST /v1/adopt (edged's POST /network/adopt) read the EDITABLE guest_networks
// rows -- the ones the Hotel Admin UI writes directly, which may hold a draft nobody has applied -- created a
// revision from them, and marked it active through a requirePending=false variant of markActive. No validation,
// no apply, no health check, no confirmation window, and not one check that the live Linux network resembled
// what had just been recorded as live. An unapplied edit could become the appliance's definition of reality at
// the next reboot.
//
// That route is retired and the bypass parameter is gone. This file is what stops either coming back: a flag
// that decides whether an invariant holds is one a future caller can pass as false.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// netdSources returns every non-test Go file in this package.
func netdSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(raw)
	}
	if len(out) == 0 {
		t.Fatal("no netd sources were read; this test would pass vacuously")
	}
	return out
}

// THERE IS NO ADOPT ROUTE, AND NO BYPASS OF THE PENDING GATE.
func TestNoRouteActivatesARevisionWithoutApplyingIt(t *testing.T) {
	// Each needle is a way the retired path could return: the route, the handler, the store bypass, or a
	// re-introduced flag that makes the pending gate optional.
	banned := []struct{ needle, why string }{
		{`"/v1/adopt"`, "the adopt route declared the editable DB rows active without applying them"},
		{"markActiveAdopt", "the store bypass that activated a revision that was never pending"},
		{"requirePending", "a flag that lets a caller switch the pending gate off is not an invariant"},
	}
	for name, src := range netdSources(t) {
		for _, b := range banned {
			if strings.Contains(src, b.needle) {
				// The explanatory comment in store.go names the retired symbols on purpose; a mention inside a
				// comment is history, a mention in code is the defect. Only the latter is a failure.
				if mentionedOnlyInComments(src, b.needle) {
					continue
				}
				t.Errorf("%s contains %q in code: %s", name, b.needle, b.why)
			}
		}
	}
}

// ...AND markActive IS STILL THE ONLY WRITER OF state='active'.
//
// If a second writer ever appears, the pending gate stops being the single thing that decides provenance and
// this whole guarantee becomes a convention instead of a property.
func TestOnlyMarkActiveWritesTheActiveState(t *testing.T) {
	writers := map[string]int{}
	for name, src := range netdSources(t) {
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
				continue
			}
			// A SQL fragment that SETS the revision state to active. Reads (`WHERE state='active'`) are
			// plentiful and harmless; it is the assignment that confers provenance.
			if strings.Contains(line, "SET state='active'") {
				writers[name]++
			}
		}
	}
	// store.go's markActive holds the one UPDATE ... SET state='active'. Anything else is a second way in.
	for name, n := range writers {
		if name != "store.go" {
			t.Errorf("%s writes state='active' in %d place(s); markActive must be the only writer", name, n)
		}
	}
	if writers["store.go"] == 0 {
		t.Error("no writer of state='active' was found at all, so this test is not checking anything")
	}
}

// mentionedOnlyInComments reports whether every occurrence of needle sits on a comment line.
func mentionedOnlyInComments(src, needle string) bool {
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			return false
		}
	}
	return true
}
