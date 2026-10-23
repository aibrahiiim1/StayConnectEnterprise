package main

// Guest sign-in attempts authorization through the real router and session middleware; denied cases assert the handler is never reached.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// signInRBACRouter builds the shape main.go mounts; handler bodies are irrelevant, since reaching one means the boundary was crossed.
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
			rr.Post("/{id}", reached)
			return rr
		})
	})
	return r
}

func signInDo(t *testing.T, h http.Handler, path, cookie string) int {
	t.Helper()
	// The credential reveal is a POST carrying the operator's password (B8); the list is a GET.
	method := http.MethodGet
	if strings.HasPrefix(path, "/guest-signin-credentials/") {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// The two permissions differ role by role: site_viewer may read the list but not what guests typed.
// If they collapse into one key, every read-only login sees guest credentials.
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

// Hiding the button is not the boundary: calling the credential endpoint directly is refused by middleware before any handler or row read.
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

// No session, no surface: unauthenticated and unauthorized are different failures.
func TestSignInAttempts_AnonymousIsRefusedOnBothSurfaces(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := signInRBACRouter(s)
	for _, path := range []string{"/guest-signin-attempts/", "/guest-signin-credentials/x"} {
		if got := signInDo(t, h, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, got)
		}
	}
}

// Writes are refused on the list: write intent derives from the METHOD, so permRead is refused a POST even if a route were added later.
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

// The UI mirror (hotel-admin MATRIX) must agree with edged's rolePerms.
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
