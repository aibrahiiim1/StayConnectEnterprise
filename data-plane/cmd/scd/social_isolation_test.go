package main

// Evidence that sign-in with a Site's own Google account stays on the appliance: the social sign-in code can reach no
// OneGate Central endpoint, and OneGate Central's code has no social sign-in at all. This is a source guard, not a
// promise about runtime: runtime evidence is the network capture and Central's request log recorded in the delivery.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The appliance-to-Central messages that exist (CLAUDE.md 0E). A new kind is a product decision.
var centralPathPrefixes = []string{"/v1/appliance", "/v1/appliances/register", "/cloud/v1"}

func nonTestGoFiles(t *testing.T, dir string, match func(name string) bool) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || !match(n) {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

func TestSocialSignInCodeNeverReachesCentral(t *testing.T) {
	var files []string
	files = append(files, nonTestGoFiles(t, ".", func(n string) bool { return strings.HasPrefix(n, "social") })...)
	files = append(files, nonTestGoFiles(t, filepath.Join("..", "portald"), func(n string) bool { return strings.HasPrefix(n, "social") })...)
	files = append(files, nonTestGoFiles(t, filepath.Join("..", "..", "internal", "social"), func(string) bool { return true })...)
	files = append(files, nonTestGoFiles(t, filepath.Join("..", "..", "internal", "socialloader"), func(string) bool { return true })...)
	if len(files) < 8 {
		t.Fatalf("the guard found only %d social files: it would guard nothing", len(files))
	}
	forbidden := append([]string{"ptHTTPCentral", "centralClient", "ctrlapi", "central.onegate", "CENTRAL_URL", "centralEndpoint"}, centralPathPrefixes...)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, bad := range forbidden {
			if strings.Contains(src, bad) {
				t.Errorf("%s mentions %q: social sign-in must not talk to OneGate Central", f, bad)
			}
		}
	}
}

func TestCentralHasNoSocialSignIn(t *testing.T) {
	root := filepath.Join("..", "..", "..", "control-plane")
	hits := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		low := strings.ToLower(string(raw))
		for _, w := range []string{"social_oauth", "oauth2", "googleusercontent", "id_token", "authorize_url"} {
			if strings.Contains(low, w) {
				t.Errorf("%s mentions %q: OneGate Central must not hold social sign-in", path, w)
				hits++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("%d hits", hits)
	}
}
