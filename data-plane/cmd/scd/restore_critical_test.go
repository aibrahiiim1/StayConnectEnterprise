package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// P4 L13: when both renames fail no database carries the live name, so the services are not restarted (they would
// crash-loop) and maintenance stays on. Asserted on the source, like the other restore safety rules.
func TestDoubleRenameFailureDoesNotRestartServices(t *testing.T) {
	src, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "critical := func(step, detail string)") {
		t.Fatal("no single critical-failure path")
	}
	if n := strings.Count(s, `critical(`); n < 3 { // swap, rollback, integrity
		t.Fatalf("critical failures route through critical() %d times, want 3 call sites", n)
	}
	if regexp.MustCompile(`startDependents\(ctx\)\s*\n\s*fail\([^\n]*"CRITICAL`).MatchString(s) {
		t.Fatal("a CRITICAL restore failure still restarts the dependent services")
	}
	if regexp.MustCompile(`startDependents\(ctx\)\s*\n\s*critical\(`).MatchString(s) {
		t.Fatal("a CRITICAL restore failure still restarts the dependent services")
	}
}
