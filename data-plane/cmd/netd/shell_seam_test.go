package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// NOTHING IN PRODUCTION MAY CALL THE TEST SEAM DIRECTLY.
//
// a.runFn and a.outFn are the seams a test substitutes; they are nil in production, where a.run and
// a.output are the real entry points. A helper that called a.runFn directly therefore panicked on the FIRST
// real apply — inside an HTTP handler, where the router's Recoverer turned the nil-pointer dereference into
// a 500. The apply never reached its own error path, so the revision was left stuck in "applying" with no
// event and no rollback.
//
// Every unit test passed, because tests always set the seam. That is the point: no behavioural test in this
// package can see this class of mistake, because the mistake is invisible precisely when a test is running.
// So the guard reads the source instead.
func TestNoDirectSeamCallsInProductionCode(t *testing.T) {
	// a.runFn( / a.outFn( — a CALL, not the assignment in a struct literal or a nil check.
	call := regexp.MustCompile(`\ba\.(runFn|outFn)\s*\(`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx] // ignore prose that merely mentions the seam
			}
			// The two legitimate sites are the dispatchers themselves, which nil-check first.
			if strings.Contains(code, "if a.runFn != nil") || strings.Contains(code, "if a.outFn != nil") {
				continue
			}
			if call.MatchString(code) {
				offenders = append(offenders, name+":"+itoaTest(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	// The dispatchers call the seam on the line AFTER their nil check; allow exactly those.
	filtered := offenders[:0]
	for _, o := range offenders {
		if strings.Contains(o, "return a.runFn(ctx, name, args...)") ||
			strings.Contains(o, "return a.outFn(ctx, name, args...)") {
			continue
		}
		filtered = append(filtered, o)
	}
	if len(filtered) > 0 {
		t.Fatalf("production code calls the TEST SEAM directly; it is nil outside tests and will panic on the "+
			"first real apply. Call a.run / a.output instead:\n  %s", strings.Join(filtered, "\n  "))
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
