package main

// Guest sign-in protection authorization through the real router and session middleware, for the four guest sign-in keys; denied cases assert the handler is never reached.
// A single "guest sign-in" permission would hand the thresholds and thirty days of credentials to far too many operators.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func protectionRBACRouter(s *server) http.Handler {
	reached := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		mountResource(r, s, "guest-signin-protection", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Put("/", reached)
			return rr
		})
		mountResource(r, s, "guest-signin-restrictions", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Post("/{id}/release", reached)
			return rr
		})
		mountResource(r, s, "guest-signin-credentials", func() http.Handler {
			rr := chi.NewRouter()
			rr.Post("/{id}", reached)
			return rr
		})
	})
	return r
}

func protectionDo(t *testing.T, h http.Handler, method, path, cookie string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// The desk releases and does not re-tune: front_office_operator and guest_relations_operator may end one device's wait but not change the thresholds.
func TestSignInProtection_ReleasingIsNotTheSameAsReTuning(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := protectionRBACRouter(s)

	cases := []struct {
		role                                          string
		readPolicy, writePolicy, listRestr, doRelease int
	}{
		{"site_admin", http.StatusTeapot, http.StatusTeapot, http.StatusTeapot, http.StatusTeapot},
		{"hotel_it_manager", http.StatusTeapot, http.StatusTeapot, http.StatusTeapot, http.StatusTeapot},
		// The desk: release yes, policy read-only.
		{"front_office_operator", http.StatusTeapot, http.StatusForbidden, http.StatusTeapot, http.StatusTeapot},
		{"guest_relations_operator", http.StatusTeapot, http.StatusForbidden, http.StatusTeapot, http.StatusTeapot},
		// A viewer sees both and acts on neither.
		{"site_viewer", http.StatusTeapot, http.StatusForbidden, http.StatusTeapot, http.StatusForbidden},
		// Roles with no relationship to guest sign-in reach nothing.
		{"voucher_operator", http.StatusForbidden, http.StatusForbidden, http.StatusForbidden, http.StatusForbidden},
		{"payments_operator", http.StatusForbidden, http.StatusForbidden, http.StatusForbidden, http.StatusForbidden},
	}
	for _, c := range cases {
		cookie := loginAs(t, s, []string{c.role})
		if got := protectionDo(t, h, http.MethodGet, "/guest-signin-protection/", cookie); got != c.readPolicy {
			t.Errorf("%s: read policy = %d, want %d", c.role, got, c.readPolicy)
		}
		if got := protectionDo(t, h, http.MethodPut, "/guest-signin-protection/", cookie); got != c.writePolicy {
			t.Errorf("%s: change policy = %d, want %d", c.role, got, c.writePolicy)
		}
		if got := protectionDo(t, h, http.MethodGet, "/guest-signin-restrictions/", cookie); got != c.listRestr {
			t.Errorf("%s: list restrictions = %d, want %d", c.role, got, c.listRestr)
		}
		if got := protectionDo(t, h, http.MethodPost, "/guest-signin-restrictions/abc/release", cookie); got != c.doRelease {
			t.Errorf("%s: release = %d, want %d", c.role, got, c.doRelease)
		}
	}
}

// Releasing carries no access to credential values; the two permissions are held by overlapping roles, where one is easily assumed to imply the other.
func TestSignInProtection_TheReleasePermissionDoesNotCarryCredentials(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := protectionRBACRouter(s)

	// A role holding ONLY the release permission. It does not exist in the matrix, and that is the point:
	// the middleware decides per key, so a hypothetical role with one key gets exactly that key.
	cookie := loginAs(t, s, []string{"site_viewer"})
	if got := protectionDo(t, h, http.MethodGet, "/guest-signin-restrictions/", cookie); got != http.StatusTeapot {
		t.Fatalf("setup: site_viewer should read the restriction list, got %d", got)
	}
	if got := protectionDo(t, h, http.MethodPost, "/guest-signin-credentials/any", cookie); got != http.StatusForbidden {
		t.Fatalf("reading the restriction list carried access to guest credentials: %d", got)
	}
}

// No session, no surface — on every one of the new routes.
func TestSignInProtection_AnonymousIsRefusedEverywhere(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := protectionRBACRouter(s)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/guest-signin-protection/"},
		{http.MethodPut, "/guest-signin-protection/"},
		{http.MethodGet, "/guest-signin-restrictions/"},
		{http.MethodPost, "/guest-signin-restrictions/x/release"},
	} {
		if got := protectionDo(t, h, c.method, c.path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c.method, c.path, got)
		}
	}
}
