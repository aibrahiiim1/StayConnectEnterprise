package main

import (
	"os"
	"path/filepath"
	"testing"
)

// P4 L12: the removed-from-Central marker is replaced whole (temp, fsync, rename), never truncated in place, and
// leaves no temporary file behind.
func TestRemovedMarkerIsReplacedAtomically(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "removed.json")
	if err := os.WriteFile(p, []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRemoved(p, removedRecord{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 || string(b) == `{"old":true}` {
		t.Fatalf("marker not replaced: %q %v", b, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("stray files: %d", len(ents))
	}
}
