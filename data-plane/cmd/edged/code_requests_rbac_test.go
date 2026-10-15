package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// STAFF HELP FOR CODE REQUESTS (resources_code_requests.go): who may list, and who may read a live code.
// Revealing a code is the stronger power, and it never follows from the list. The list is READ-only for every
// role below site_admin: its only write, clearing a device's temporary-internet windows, went with the email code.
func TestCodeRequests_RevealIsItsOwnPermission(t *testing.T) {
	cases := []struct {
		role                     string
		list, write, revealAllow bool
	}{
		{"site_admin", true, true, true},
		{"hotel_it_manager", true, false, true},
		{"front_office_operator", true, false, true},
		{"guest_relations_operator", true, false, false},
		{"site_viewer", true, false, false},
		{"voucher_operator", false, false, false},
		{"payments_operator", false, false, false},
	}
	for _, c := range cases {
		roles := []string{c.role}
		if got := permFor(roles, "code-requests", permRead); got != c.list {
			t.Errorf("%s list: %v, want %v", c.role, got, c.list)
		}
		if got := permFor(roles, "code-requests", permWrite); got != c.write {
			t.Errorf("%s write: %v, want %v", c.role, got, c.write)
		}
		if got := permFor(roles, "code-reveal", permWrite); got != c.revealAllow {
			t.Errorf("%s reveal: %v, want %v", c.role, got, c.revealAllow)
		}
	}
}

// The "let them try again" route is gone with the temporary internet it cleared.
func TestCodeRequests_ClearRouteIsRemoved(t *testing.T) {
	s := &server{}
	rec := httptest.NewRecorder()
	s.codeRequestsRoutes().ServeHTTP(rec, withSession(httptest.NewRequest(http.MethodPost, "/clear",
		strings.NewReader(`{"device_mac":"02:00:00:00:00:01","reason":"x","password":"y"}`)), "site_admin"))
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /code-requests/clear still answers %d", rec.Code)
	}
}

// The list asks scd under the new name and falls back to the former one only when scd does not know the new.
func TestCodeRequests_ListFallsBackToTheFormerSCDRoute(t *testing.T) {
	var paths []string
	sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == scdCodeRequestsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"requests":[]}`))
	})
	s := &server{scd: newSCDClient(sock)}
	rec := httptest.NewRecorder()
	s.codeRequestsRoutes().ServeHTTP(rec, withSession(httptest.NewRequest(http.MethodGet, "/?hours=24", nil), "site_admin"))
	if rec.Code != http.StatusOK || len(paths) != 2 || paths[1] != scdCodeRequestsLegacyPath {
		t.Fatalf("got %d, scd asked %v", rec.Code, paths)
	}
}

// A role that cannot show a code sees the address and the device masked; a body that cannot be parsed is never
// relayed unmasked.
func TestCodeRequests_ListIsMaskedForRolesThatCannotReveal(t *testing.T) {
	in := []byte(`{"requests":[{"channel":"email","destination":"guest@example.com","device_mac":"d2:77:1c:e6:f7:3a","state":"PENDING"},` +
		`{"channel":"sms","destination":"+201001234567","device_mac":"02:00:00:00:00:01"}],"limits":{"per_device_daily":3}}`)
	out := string(maskCodeRequests(in))
	for _, leak := range []string{"guest@example.com", "d2:77:1c", "+201001234567"} {
		if strings.Contains(out, leak) {
			t.Fatalf("masked list leaks %q: %s", leak, out)
		}
	}
	for _, want := range []string{"g•••@example.com", "••:••:••:••:f7:3a", "+20•••4567", `"state":"PENDING"`, `"per_device_daily":3`} {
		if !strings.Contains(out, want) {
			t.Fatalf("masked list misses %q: %s", want, out)
		}
	}
	if got := string(maskCodeRequests([]byte("not json"))); got != `{"requests":[]}` {
		t.Fatalf("an unparseable body must fail closed, got %s", got)
	}
}
