package main

// GUEST SIGN-IN PROTECTION — AUTHORIZATION, through the ACTUAL router and session middleware.
//
// Four keys now touch guest sign-in, and the Product Owner's requirement is that the release permission
// "must not implicitly grant access to credential values or permission to change the protection policy".
// That is a statement about the permission MODEL, so it is tested as one: every role, every one of the four
// routes, and the denied cases assert the handler is never REACHED.
//
// The shape that would fail here is the tempting one — a single "guest sign-in" permission covering the list,
// the credentials, the policy and the release. It would put the property's thresholds in the hands of every
// operator who can look at a dashboard, and thirty days of guest credentials in the hands of everyone who can
// end one device's wait.

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
			rr.Get("/{id}", reached)
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

// THE DESK RELEASES AND DOES NOT RE-TUNE.
//
// front_office_operator and guest_relations_operator are the rows this test exists for. They may end one
// device's wait — the guest is standing in front of them and they are the only people who can judge it — and
// they may not change the property's thresholds. If those ever collapse into one key, "make it twenty
// attempts for everybody" becomes the quickest way for a desk under pressure to help one person, and this is
// the test that fails when somebody does it.
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

// RELEASING CARRIES NO ACCESS TO CREDENTIAL VALUES. Stated as its own test because it is a requirement in the
// Product Owner's words, and because the two permissions are held by overlapping sets of roles — which is
// exactly the situation in which somebody later decides one implies the other.
func TestSignInProtection_TheReleasePermissionDoesNotCarryCredentials(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := protectionRBACRouter(s)

	// A role holding ONLY the release permission. It does not exist in the matrix, and that is the point:
	// the middleware decides per key, so a hypothetical role with one key gets exactly that key.
	cookie := loginAs(t, s, []string{"site_viewer"})
	if got := protectionDo(t, h, http.MethodGet, "/guest-signin-restrictions/", cookie); got != http.StatusTeapot {
		t.Fatalf("setup: site_viewer should read the restriction list, got %d", got)
	}
	if got := protectionDo(t, h, http.MethodGet, "/guest-signin-credentials/any", cookie); got != http.StatusForbidden {
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
