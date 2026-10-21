package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The "not yourself" checks compare the operator id in the URL with the session's. An upper-case spelling of
// one's own id is the same row to Postgres, so it must be refused as yourself; a braced or otherwise
// non-canonical spelling is refused as a bad id. Neither may reach the database.
func TestOperatorSelfChecksCanonicaliseTheID(t *testing.T) {
	const me = "3f2b8c1e-5d4a-4e6f-9a0b-1c2d3e4f5a6b"
	s := &server{}
	router := chi.NewRouter()
	router.Delete("/operators/{id}", s.disableEdgeOperator)
	router.Post("/operators/{id}/set-password", s.setEdgeOperatorPassword)
	router.Post("/operators/{id}/reset-mfa", s.resetEdgeOperatorMFA)
	router.Post("/operators/{id}/roles", s.addEdgeOperatorRole)
	router.Delete("/operators/{id}/roles/{role}", s.removeEdgeOperatorRole)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), sessKey, &session{OperatorID: me}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	upper := strings.ToUpper(me)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodDelete, "/operators/" + upper, `{"your_password":"x"}`},
		{http.MethodPost, "/operators/" + upper + "/set-password", `{"password":"x","your_password":"x"}`},
		{http.MethodPost, "/operators/" + upper + "/reset-mfa", `{"password":"x","reason":"lost phone"}`},
		{http.MethodDelete, "/operators/" + upper + "/roles/site_admin", `{"your_password":"x"}`},
	} {
		if rec := call(c.method, c.path, c.body); rec.Code != http.StatusConflict {
			t.Fatalf("%s %s = %d %s, want 409 (yourself)", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	for _, bad := range []string{"%7B" + me + "%7D", strings.ReplaceAll(me, "-", ""), "not-a-uuid"} {
		for _, c := range []struct{ method, path string }{
			{http.MethodDelete, "/operators/" + bad},
			{http.MethodPost, "/operators/" + bad + "/set-password"},
			{http.MethodPost, "/operators/" + bad + "/reset-mfa"},
			{http.MethodPost, "/operators/" + bad + "/roles"},
			{http.MethodDelete, "/operators/" + bad + "/roles/site_admin"},
		} {
			if rec := call(c.method, c.path, `{}`); rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d, want 400", c.method, c.path, rec.Code)
			}
		}
	}
	if id, ok := canonicalUUID(upper); !ok || id != me {
		t.Fatalf("canonicalUUID(%q) = %q %v", upper, id, ok)
	}
}
