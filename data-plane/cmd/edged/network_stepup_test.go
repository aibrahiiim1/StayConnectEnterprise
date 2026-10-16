package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// N-15: applying, keeping and rolling back a client-network configuration need the operator's password, and a
// request without one is refused before anything is staged or sent to netd (the server here has neither a
// database nor a netd: reaching either would panic).
func TestClientNetworkChangesRequireStepUp(t *testing.T) {
	s := &server{}
	cases := []struct {
		name string
		h    http.HandlerFunc
		path string
		body string
	}{
		{"apply", s.netApply, "/edge/v1/network/apply", `{"summary":"x"}`},
		{"apply with an empty password", s.netApply, "/edge/v1/network/apply", `{"summary":"x","password":""}`},
		{"confirm", s.confirmRevision, "/edge/v1/network/revisions/rev-1/confirm", `{}`},
		{"rollback", s.rollbackRevision, "/edge/v1/network/revisions/rev-1/rollback", ``},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", "rev-1")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		rec := httptest.NewRecorder()
		c.h(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "reauth_required") {
			t.Errorf("%s without a password: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
}
