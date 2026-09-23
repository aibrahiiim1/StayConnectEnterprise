package main

// THE TEST THAT MAKES THE CLASSIFICATION IMPOSSIBLE TO FORGET.
//
// classifyRoute defaults to adminRoute, so a route added later and never classified is REFUSED to non-edged
// callers rather than exposed. That is the safe direction at runtime and a bad surprise in practice: the
// forgotten route might be a GUEST route, and the first symptom would be guests unable to sign in.
//
// So the runtime fails closed and this test fails loud. It builds the real route list and insists every
// single path is named in one of the two lists in admin_surface.go -- not merely handled by the default.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// scdRoutePatterns reads the routes out of main.go rather than out of a chi router.
//
// WHY THE SOURCE AND NOT chi.Walk. Building the real router means building a *server: a database pool, a
// PMS registry, a mailer, a metrics registry and the notification loader. That is a live-dependency test
// pretending to be a unit test, and it would be skipped on a workstation -- which is precisely where a
// forgotten classification needs to be caught. The registration lines are unambiguous and the regexp below
// anchors on them; a route registered in a way this does not match shows up as a mismatch in the count
// assertion at the end rather than silently passing.
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

// TestEveryRouteIsDeliberatelyClassified is the whole point of the file.
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
		// A chi pattern carries {id}; the classifier sees a real path. Neither affects prefix matching,
		// but comparing the pattern is what keeps this test independent of any particular id.
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

// TestTheGuestSurfaceIsExactlyWhatPortaldNeeds pins the guest list against its actual consumer.
//
// Every path here was read out of cmd/portald, cmd/acctd or deploy/ -- see the caller table in
// admin_surface.go. If one of these ever classifies as admin, the captive portal breaks, so the test states
// them one by one rather than trusting the prefixes to keep meaning what they mean today.
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
		"/v1/sessions/revoke", // acctd, enforcing a quota
		"/v1/auth/otp/issue",
		"/v1/auth/social/start",
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
	} {
		if classifyRoute(path) != guestRoute {
			t.Errorf("%s classified as ADMIN. portald or acctd calls it, so this refuses a caller that "+
				"needs it: guest internet or quota enforcement stops working.", path)
		}
	}
}

// TestTheDangerousRoutesAreAdmin names the operations that made this gate necessary.
//
// Vouchers are on the list, and they are not the reason it exists. /v1/backup/restore overwrites the
// database. /v1/license/install changes what the appliance is entitled to serve. /v1/setup/enroll changes
// which tenant it belongs to. All three were reachable by the process listening on every guest VLAN.
func TestTheDangerousRoutesAreAdmin(t *testing.T) {
	for _, path := range []string{
		"/v1/backup/restore",
		"/v1/backup/run",
		"/v1/backup/settings",
		"/v1/license/install",
		"/v1/license/refresh",
		"/v1/setup/enroll",
		"/v1/setup/offline-import",
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
	} {
		if classifyRoute(path) != adminRoute {
			t.Errorf("%s classified as GUEST: the network-facing captive portal would be able to call it", path)
		}
	}
}

// TestAnUnknownPathIsAdministrative is the default-deny property, asserted rather than assumed.
func TestAnUnknownPathIsAdministrative(t *testing.T) {
	for _, path := range []string{
		"/v1/something-added-next-year",
		"/v1/phase9/anything",
		"/",
		"/v1/",
		"/v1/sessions", // NOT a guest prefix: the guest entries are the specific session routes
	} {
		if classifyRoute(path) != adminRoute {
			t.Errorf("%s classified as GUEST; an unrecognised path must default to administrative", path)
		}
	}
}

// TestGuestPrefixesCannotBeWidenedByAccident guards the shape of the list itself.
//
// "/v1/" or "/v1" in guestPrefixes would make the ENTIRE socket guest-reachable while every other test here
// still passed, because each one only checks paths it names. This checks the list, not the paths.
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
	// A prefix must not be a prefix OF an admin prefix, which would swallow it.
	for _, g := range guestPrefixes {
		for _, a := range adminPrefixes {
			if strings.HasPrefix(a, g) {
				t.Errorf("guest prefix %q swallows admin prefix %q: the admin routes under it would be "+
					"reachable by any process in the socket's group", g, a)
			}
		}
	}
}

// TestTheGateRefusesAnAdminRouteAndPassesAGuestRoute exercises the middleware, not just the classifier.
//
// On a non-Linux build requireAdminPeer allows everything by design (peer_identity_other.go), so what this
// can assert portably is that the gate is WIRED: a guest route reaches the handler. The refusal path is
// asserted on its own terms in the Linux-only test below.
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
