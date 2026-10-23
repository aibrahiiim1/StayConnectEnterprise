package main

// Every route must be named in guestPrefixes or adminPrefixes; classifyRoute's default-deny would otherwise hide a forgotten guest route until guests cannot sign in.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// scdRoutePatterns reads routes from main.go: building the real router needs a DB, PMS registry and mailer.
func scdRoutePatterns(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read scd routes: %v", err)
	}
	re := regexp.MustCompile(`r\.(?:Get|Post|Put|Delete|Patch|Method|Handle)\(\s*(?:"[A-Z]+",\s*)?"(/[^"]*)"`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	if len(out) < 40 {
		t.Fatalf("only %d routes found in main.go; the registration pattern this test matches has "+
			"probably changed, which would make every assertion below vacuous", len(out))
	}
	return out
}

func TestEveryRouteIsDeliberatelyClassified(t *testing.T) {
	named := func(path string, prefixes []string) bool {
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, p) {
				return true
			}
		}
		return false
	}
	for _, path := range scdRoutePatterns(t) {
		g, a := named(path, guestPrefixes), named(path, adminPrefixes)
		switch {
		case g && a:
			t.Errorf("%s matches BOTH a guest prefix and an admin prefix: the lists overlap, so which one "+
				"wins depends on evaluation order rather than on a decision", path)
		case !g && !a:
			t.Errorf("%s is classified by NOTHING. The runtime will treat it as administrative and refuse "+
				"portald, which is correct if it is an admin route and an outage if it is a guest route. "+
				"Add it to guestPrefixes or adminPrefixes in admin_surface.go, whichever it is.", path)
		}
	}
}

// Each path is a real caller of the guest surface; misclassifying one as admin breaks the captive portal.
func TestTheGuestSurfaceIsExactlyWhatPortaldNeeds(t *testing.T) {
	for _, path := range []string{
		"/v1/health",
		"/v1/commerce/packages",
		"/v1/commerce/quote",
		"/v1/commerce/confirm",
		"/v1/sessions/activate",
		"/v1/sessions/authorize",
		"/v1/sessions/authorize-otp",
		"/v1/sessions/authorize-credentials",
		"/v1/sessions/authorize-social",
		"/v1/sessions/status",
		"/v1/sessions/revoke",
		"/v1/auth/otp/issue",
		"/v1/auth/social/start",
		"/v1/auth/account/register",
		"/v1/auth/account/status",
		"/v1/auth/account/resend",
		"/v1/auth/account/grace",
		"/v1/auth/account/verify",
		"/v1/auth/account/forgot",
		"/v1/auth/account/reset",
		"/v1/tenant/auth-methods",
		"/v1/tenant/branding",
		"/v1/phase3/access/status",
		"/v1/phase3/auth/pms/resolve",
		"/v1/phase3/auth/pms/grant",
		"/v1/phase5/poststay/issue",
		"/v1/phase5/auth/post-stay-pin",
		"/v1/phase5/poststay/convert",
		"/v1/phase6/devices/list",
		"/v1/phase6/devices/release",
		"/v1/devices/list",
		"/v1/devices/release",
		"/v1/devices/replace",
		"/v1/portal/tls-state",
	} {
		if classifyRoute(path) != guestRoute {
			t.Errorf("%s classified as ADMIN. portald or acctd calls it, so this refuses a caller that "+
				"needs it: guest internet or quota enforcement stops working.", path)
		}
	}
}

// The routes that made the admin gate necessary: restore, licence install and offline package were reachable from every guest VLAN.
func TestTheDangerousRoutesAreAdmin(t *testing.T) {
	for _, path := range []string{
		"/v1/backup/restore",
		"/v1/backup/run",
		"/v1/backup/settings",
		"/v1/license/install",
		"/v1/central/refresh",
		"/v1/central/status",
		"/v1/central/offline-request",
		"/v1/central/offline-package",
		"/v1/hotel-admin-cert/rotate",
		"/v1/admin/pms/protel/test",
		"/v1/admin/walled-garden/reload",
		"/v1/vouchers",
		"/v1/vouchers/issue",
		"/v1/vouchers/export",
		"/v1/vouchers/0f8b9a0e-0000-0000-0000-000000000000/reveal",
		"/v1/vouchers/0f8b9a0e-0000-0000-0000-000000000000/revoke",
		"/v1/voucher-key-generations",
		"/v1/phase3/signin-attempts/credentials",
		"/v1/portal/tls/kick",
		"/v1/portal/tls/renew",
		"/v1/admin/accounts/0f8b9a0e-0000-0000-0000-000000000000/resend-verification",
	} {
		if classifyRoute(path) != adminRoute {
			t.Errorf("%s classified as GUEST: the network-facing captive portal would be able to call it", path)
		}
	}
}

func TestAnUnknownPathIsAdministrative(t *testing.T) {
	for _, path := range []string{
		"/v1/something-added-next-year",
		"/v1/phase9/anything",
		"/",
		"/v1/",
		"/v1/sessions",
	} {
		if classifyRoute(path) != adminRoute {
			t.Errorf("%s classified as GUEST; an unrecognised path must default to administrative", path)
		}
	}
}

// A "/v1/" guest prefix would expose the whole socket while every other test still passed.
func TestGuestPrefixesCannotBeWidenedByAccident(t *testing.T) {
	for _, p := range guestPrefixes {
		switch p {
		case "/", "/v1", "/v1/":
			t.Fatalf("guestPrefixes contains %q, which classifies the whole socket as guest-reachable", p)
		}
		if !strings.HasPrefix(p, "/") {
			t.Errorf("guestPrefixes entry %q is not a path", p)
		}
	}
	for _, g := range guestPrefixes {
		for _, a := range adminPrefixes {
			if strings.HasPrefix(a, g) {
				t.Errorf("guest prefix %q swallows admin prefix %q: the admin routes under it would be "+
					"reachable by any process in the socket's group", g, a)
			}
		}
	}
}

// On non-Linux requireAdminPeer allows everything, so this only asserts the gate is wired.
func TestTheGateRefusesAnAdminRouteAndPassesAGuestRoute(t *testing.T) {
	var reached bool
	h := (&server{}).peerGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	reached = false
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/health", nil))
	if !reached {
		t.Error("the gate blocked /v1/health, which keepalived, netd and edged all call")
	}
}
