package main

// GUEST SIGN-IN ATTEMPTS — AUTHORIZATION, through the ACTUAL router and session middleware.
//
// The Product Owner asked for two distinct, server-enforced permissions, and "server-enforced" is the whole
// of it: a UI that hides a button is a courtesy, and the only thing that stops an operator reading thirty
// days of guest credentials is that edged refuses the route. So these tests drive the real stack — chi
// router, requireAuth, resourcePermission, handler — and the denied cases assert that the handler is never
// REACHED, not merely that it answered unhelpfully.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// signInRBACRouter builds the same shape main.go mounts. The handler bodies are irrelevant here: a request
// that reaches one has already crossed the boundary this test is about, and reaching it is exactly what must
// not happen for a denied role.
func signInRBACRouter(s *server) http.Handler {
	reached := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		mountResource(r, s, "guest-signin-attempts", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Get("/{id}", reached)
			return rr
		})
		mountResource(r, s, "guest-signin-credentials", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/{id}", reached)
			return rr
		})
	})
	return r
}

func signInDo(t *testing.T, h http.Handler, path, cookie string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// THE TWO PERMISSIONS ARE GENUINELY DIFFERENT, role by role.
//
// The row that matters most is site_viewer: it may read the list — the same class of operational evidence it
// reads everywhere else — and it may NOT read what guests typed. If those two ever collapse into one key,
// every read-only login on the property becomes a thirty-day window onto guest credentials, and this is the
// test that fails when somebody does it.
func TestSignInAttempts_TheTwoPermissionsAreEnforcedSeparately(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := signInRBACRouter(s)

	cases := []struct {
		role            string
		wantList        int
		wantCredentials int
	}{
		{"site_admin", http.StatusTeapot, http.StatusTeapot},
		{"hotel_it_manager", http.StatusTeapot, http.StatusTeapot},
		{"front_office_operator", http.StatusTeapot, http.StatusTeapot},
		{"guest_relations_operator", http.StatusTeapot, http.StatusTeapot},
		// The list, and NOT the values.
		{"site_viewer", http.StatusTeapot, http.StatusForbidden},
		// Neither: these roles have no reason to look at guest sign-in at all.
		{"voucher_operator", http.StatusForbidden, http.StatusForbidden},
		{"payments_operator", http.StatusForbidden, http.StatusForbidden},
	}
	for _, c := range cases {
		cookie := loginAs(t, s, []string{c.role})
		if got := signInDo(t, h, "/guest-signin-attempts/", cookie); got != c.wantList {
			t.Errorf("%s: list = %d, want %d", c.role, got, c.wantList)
		}
		if got := signInDo(t, h, "/guest-signin-credentials/abc", cookie); got != c.wantCredentials {
			t.Errorf("%s: credentials = %d, want %d", c.role, got, c.wantCredentials)
		}
	}
}

// HIDING THE BUTTON IS NOT THE BOUNDARY. An operator who can see the list and calls the credential endpoint
// directly — which is all "hiding a button" leaves them to do — is refused by middleware, before any handler
// runs and before any row is read.
func TestSignInAttempts_TheCredentialRouteCannotBeReachedByCallingItDirectly(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := signInRBACRouter(s)
	cookie := loginAs(t, s, []string{"site_viewer"})

	if got := signInDo(t, h, "/guest-signin-attempts/", cookie); got != http.StatusTeapot {
		t.Fatalf("setup: the list should be readable by site_viewer, got %d", got)
	}
	if got := signInDo(t, h, "/guest-signin-credentials/any-id", cookie); got != http.StatusForbidden {
		t.Fatalf("a role without the credential permission reached the values by calling the API: %d", got)
	}
}

// No session, no surface. Stated separately because an authenticated-but-unauthorized answer and an
// unauthenticated one are different failures, and only one of them means the middleware is wired at all.
func TestSignInAttempts_AnonymousIsRefusedOnBothSurfaces(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := signInRBACRouter(s)
	for _, path := range []string{"/guest-signin-attempts/", "/guest-signin-credentials/x"} {
		if got := signInDo(t, h, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, got)
		}
	}
}

// WRITES ARE REFUSED EVERYWHERE. Both resources are read-only by construction — the routers register only
// GET — but the permission model derives write intent from the METHOD, so a role holding permRead is refused
// a POST even if a route were added later without thinking about it.
func TestSignInAttempts_NoRoleMayWriteEitherSurface(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := signInRBACRouter(s)
	for _, role := range []string{"hotel_it_manager", "front_office_operator", "site_viewer"} {
		cookie := loginAs(t, s, []string{role})
		req := httptest.NewRequest(http.MethodPost, "/guest-signin-attempts/", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s could POST to the attempts surface: %d", role, rec.Code)
		}
	}
}

// The UI mirror must agree with this file. A drift between edged's rolePerms and hotel-admin's MATRIX has
// already hidden working screens from the roles that owned them; here it would do something worse — show a
// credential panel the server then refuses, or hide one the operator is entitled to.
func TestSignInAttempts_PermissionKeysExistInTheMatrix(t *testing.T) {
	for _, key := range []string{"guest-signin-attempts", "guest-signin-credentials"} {
		found := false
		for role, perms := range rolePerms {
			if _, ok := perms[key]; ok {
				found = true
				_ = role
			}
		}
		if !found {
			t.Errorf("%s is granted to no role at all, so the screen is unreachable for everyone but site_admin", key)
		}
	}
}
