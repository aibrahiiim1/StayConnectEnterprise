package main

// Every handler must be reachable: backup handlers once shipped mounted on nothing, so edged proxied to a 404 and no test noticed.
// This reads the routes registered on the scd mux and asserts the ones edged depends on; it checks wiring, not behaviour.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// edgedDependsOn lists the scd paths edged calls; a new edged proxy target belongs here. Keyed by METHOD AND PATH, since a GET probe once passed on the POST.
var edgedDependsOn = []struct{ method, path string }{
	{"Post", "/v1/backup/run"},
	{"Post", "/v1/backup/verify"},
	{"Get", "/v1/backup/settings"},
	{"Post", "/v1/backup/settings"},
	{"Get", "/v1/tenant/branding"},
	{"Get", "/v1/tenant/auth-methods"},
	{"Get", "/v1/central/status"},
	{"Post", "/v1/central/refresh"},
	{"Get", "/v1/central/offline-request"},
	{"Post", "/v1/central/offline-package"},
	{"Post", "/v1/license/install"},
	// The voucher operator surface: a route with no caller is as useless as an unreachable handler. edged proxies all six.
	{"Post", "/v1/vouchers/issue"},
	{"Get", "/v1/vouchers"},
	{"Get", "/v1/vouchers/summary"},
	{"Post", "/v1/vouchers/export"},
	{"Post", "/v1/vouchers/{id}/reveal"},
	{"Post", "/v1/vouchers/{id}/revoke"},
	{"Post", "/v1/voucher-batches/{id}/revoke"},
	{"Get", "/v1/voucher-key-generations"},
	{"Post", "/v1/voucher-key-generations/{id}/supersede"},
}

func TestEveryPathEdgedCallsIsRoutedOnSCD(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read scd routes: %v", err)
	}
	// Matches the registration itself (r.Get, r.Post, ...), not a mention of the path in a comment or string.
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
	// A handler defined here and never mounted is dead or a forgotten route; checked for the backup surface.
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
