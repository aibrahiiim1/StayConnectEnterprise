package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// PHASE-6 OPERATOR SURFACE — AUTHORIZATION, through the ACTUAL router and session middleware.
//
// The decoder tests next door prove what the wire type accepts. They are not authorization proof and must
// not be read as one: a request can be perfectly well-formed and still come from somebody with no business
// changing a guest-facing appliance capability.
//
// The defect these close is precise. The routes were first registered directly inside requireAuth, so every
// authenticated operator -- including read-only desk roles -- could flip the setting. AUTHENTICATION IS NOT
// AUTHORIZATION, and the difference is invisible until somebody with a read-only login changes something.
//
// They are mounted through mountResource now, which is the same path every other management surface takes,
// so the role matrix in auth.go decides. These tests drive that real stack: chi router, requireAuth,
// resourcePermission, and the handler.

// phase6RBACRouter builds the same shape main.go mounts: requireAuth in front, mountResource around the
// resource. The handler bodies are not exercised here -- a request that reaches them has already passed the
// boundary this test is about, and reaching them is exactly what must not happen for the denied cases.
func phase6RBACRouter(s *server) http.Handler {
	reached := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		mountResource(r, s, "guest-device-self-service", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Put("/", reached)
			return rr
		})
	})
	return r
}

// loginAs creates a real session for a set of roles and returns its cookie value.
func loginAs(t *testing.T, s *server, roles []string) string {
	t.Helper()
	return s.sessions.create(&session{
		OperatorID: "55555555-5555-5555-5555-555555555555",
		Email:      "op@example.invalid", DisplayName: "Test Operator", Roles: roles,
	})
}

func phase6Do(t *testing.T, h http.Handler, method, cookie, body string) int {
	t.Helper()
	req := httptest.NewRequest(method, "/guest-device-self-service/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func newPhase6RBACServer() *server {
	return &server{sessions: newSessionStore(30 * time.Minute)}
}

// Unauthenticated requests never reach the resource.
func TestPhase6SettingRequiresAuthentication(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if code := phase6Do(t, h, m, "", `{"enabled":true}`); code != http.StatusUnauthorized {
			t.Fatalf("%s without a session returned %d, expected 401", m, code)
		}
	}
}

// THE DEFECT, ASSERTED. An authenticated operator whose role holds no permission on this resource must be
// refused -- not merely unable to do harm by luck of what the handler checks afterwards.
func TestPhase6SettingRefusesARoleWithNoPermission(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	// A role that exists in the matrix but holds nothing on this resource.
	cookie := loginAs(t, s, []string{"read_only_auditor"})
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		code := phase6Do(t, h, m, cookie, `{"enabled":true}`)
		if code == http.StatusTeapot {
			t.Fatalf("%s reached the handler for a role with no permission on this resource", m)
		}
		if code != http.StatusForbidden {
			t.Fatalf("%s returned %d, expected 403", m, code)
		}
	}
}

// A READ role may read and must not write. This is the case the original wiring got wrong: the desk roles
// are authenticated, and without resourcePermission they could have changed the setting.
func TestPhase6SettingReadRoleCannotWrite(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	for _, role := range []string{"front_office_operator", "guest_relations_operator"} {
		cookie := loginAs(t, s, []string{role})
		if code := phase6Do(t, h, http.MethodGet, cookie, ""); code != http.StatusTeapot {
			t.Fatalf("%s could not READ the setting: %d", role, code)
		}
		code := phase6Do(t, h, http.MethodPut, cookie, `{"enabled":true}`)
		if code == http.StatusTeapot {
			t.Fatalf("%s REACHED the write handler; a read-only role changed a guest-facing capability", role)
		}
		if code != http.StatusForbidden {
			t.Fatalf("%s write returned %d, expected 403", role, code)
		}
	}
}

// The intended management roles can perform the operation, or the refusals above would prove only that the
// resource is unreachable by everyone.
func TestPhase6SettingWriteRolesCanWrite(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	for _, role := range []string{"hotel_it_manager", "site_admin", "tenant_admin"} {
		cookie := loginAs(t, s, []string{role})
		if code := phase6Do(t, h, http.MethodGet, cookie, ""); code != http.StatusTeapot {
			t.Fatalf("%s could not READ the setting: %d", role, code)
		}
		if code := phase6Do(t, h, http.MethodPut, cookie, `{"enabled":true}`); code != http.StatusTeapot {
			t.Fatalf("%s could not WRITE the setting: %d", role, code)
		}
	}
}

// A role list that contains a permitted role alongside an unrelated one still works: the matrix is a union
// over the operator's roles, and asserting it here stops a later "simplification" from making it an
// intersection.
func TestPhase6SettingRolesAreAUnion(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	cookie := loginAs(t, s, []string{"front_office_operator", "hotel_it_manager"})
	if code := phase6Do(t, h, http.MethodPut, cookie, `{"enabled":true}`); code != http.StatusTeapot {
		t.Fatalf("a manager who is also a desk operator could not write: %d", code)
	}
}
