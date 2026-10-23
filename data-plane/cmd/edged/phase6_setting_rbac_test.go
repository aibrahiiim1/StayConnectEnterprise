package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// Authorization for the Phase-6 setting through the real router and session middleware. The routes are
// mounted via mountResource so the auth.go role matrix decides; authentication alone must not allow a write.

// phase6RBACRouter mirrors main.go: requireAuth in front, mountResource around the resource.
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

func TestPhase6SettingRequiresAuthentication(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if code := phase6Do(t, h, m, "", `{"enabled":true}`); code != http.StatusUnauthorized {
			t.Fatalf("%s without a session returned %d, expected 401", m, code)
		}
	}
}

// A role with no permission on this resource must be refused before the handler.
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

// A read role may read and must not write.
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

// The management roles can perform the operation (else the refusals above prove nothing).
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

// Permissions are a union over the operator's roles, not an intersection.
func TestPhase6SettingRolesAreAUnion(t *testing.T) {
	s := newPhase6RBACServer()
	h := phase6RBACRouter(s)
	cookie := loginAs(t, s, []string{"front_office_operator", "hotel_it_manager"})
	if code := phase6Do(t, h, http.MethodPut, cookie, `{"enabled":true}`); code != http.StatusTeapot {
		t.Fatalf("a manager who is also a desk operator could not write: %d", code)
	}
}
