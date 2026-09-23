package main

// EVERY HANDLER MUST BE REACHABLE.
//
// The backup settings handlers were written, built, vetted, unit-tested and merged through four green gates
// while being mounted on NOTHING. scd had no route for them, so edged proxied to a path that answered 404 and
// the operator saw "404 page not found" where a retention policy should have been. Nothing in the build or
// the test suite noticed, because an unreferenced-but-exported method is perfectly legal Go and every test
// called the handler directly rather than through the router.
//
// This reads the routes actually registered on scd's mux and asserts the ones edged depends on are there. It
// is deliberately about REACHABILITY, not behaviour: the behaviour is tested elsewhere, and the thing that
// went wrong was the wiring.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// edgedDependsOn lists the scd paths edged calls. When edged gains a new proxy target, it belongs here — that
// is the point: this file is where "I added a handler" and "the caller can reach it" are reconciled.
// METHOD AND PATH, not path alone. The first version of this checked paths only, and a probe that deleted the
// GET route still passed because the POST on the same path kept the path "registered" — a test that would
// have missed half of the very defect it exists for.
var edgedDependsOn = []struct{ method, path string }{
	{"Post", "/v1/backup/run"},
	{"Post", "/v1/backup/verify"},
	{"Get", "/v1/backup/settings"},
	{"Post", "/v1/backup/settings"},
	{"Get", "/v1/tenant/branding"},
	{"Get", "/v1/tenant/auth-methods"},
	{"Get", "/v1/license/status"},
	{"Post", "/v1/license/refresh"},
	{"Get", "/v1/setup/status"},
	// The voucher operator surface. Issuance was routed here for a whole delivery with no caller anywhere
	// -- the inverse of the defect this file exists for, and just as invisible: a reachable route nothing
	// reaches is as useless as an unreachable handler. edged now proxies all six.
	{"Post", "/v1/vouchers/issue"},
	{"Get", "/v1/vouchers"},
	{"Get", "/v1/vouchers/summary"},
	{"Post", "/v1/vouchers/export"},
	{"Post", "/v1/vouchers/{id}/reveal"},
	{"Post", "/v1/vouchers/{id}/revoke"},
	{"Get", "/v1/voucher-key-generations"},
	{"Post", "/v1/voucher-key-generations/{id}/supersede"},
}

func TestEveryPathEdgedCallsIsRoutedOnSCD(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read scd routes: %v", err)
	}
	// Matches r.Get("/v1/..."), r.Post(...), r.Put(...), r.Delete(...) — the registration itself, rather than
	// a mention of the path in a comment or a string somewhere else.
	registered := map[string]bool{}
	re := regexp.MustCompile(`r\.(Get|Post|Put|Delete|Patch|Handle)\(\s*"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		registered[m[1]+" "+m[2]] = true
	}
	if len(registered) == 0 {
		t.Fatal("no routes were found at all; this test is not reading what it thinks it is")
	}
	for _, want := range edgedDependsOn {
		if !registered[want.method+" "+want.path] {
			t.Errorf("edged calls %s %s but scd does not route it — the caller gets 404 and the handler is dead code",
				strings.ToUpper(want.method), want.path)
		}
	}
}

func TestSCDRouteHandlersAreNotOrphaned(t *testing.T) {
	// The mirror of the test above: a handler defined in this package and never mounted is either dead or a
	// route somebody forgot. Checked for the backup surface specifically, which is where it happened.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := string(src)
	for _, handler := range []string{
		"s.backupRun", "s.backupVerify", "s.backupSettingsGet", "s.backupSettingsSet", "s.tenantBranding",
	} {
		if !strings.Contains(routes, handler) {
			t.Errorf("%s is defined but never mounted; it cannot be called by anything", handler)
		}
	}
}
