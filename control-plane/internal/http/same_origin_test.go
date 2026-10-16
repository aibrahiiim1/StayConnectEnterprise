package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// P4 same-site CSRF: a console write from a sibling subdomain (same SITE, so the Lax cookie is sent) is refused;
// the console's own origin, the request's own host and Origin-less clients pass; reads always pass.
func TestSameOriginWrites(t *testing.T) {
	h := sameOriginWrites([]string{"https://central.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, c := range []struct {
		method, path, origin, host, fwdHost string
		want                                int
	}{
		{"POST", "/cloud/v1/appliances/a/retire", "https://evil.example.com", "central.example.com", "", 403},
		{"DELETE", "/cloud/v1/sites/s", "https://evil.example.com", "127.0.0.1:8080", "", 403},
		{"POST", "/v1/auth/reauth", "https://evil.example.com", "central.example.com", "", 403},
		{"POST", "/cloud/v1/appliances/a/retire", "https://central.example.com", "127.0.0.1:8080", "", 204},
		{"POST", "/cloud/v1/x", "https://other-admin.test", "127.0.0.1:8080", "other-admin.test", 204},
		{"POST", "/cloud/v1/x", "https://central2.test", "central2.test", "", 204},
		{"POST", "/cloud/v1/x", "", "central.example.com", "", 204},
		{"GET", "/cloud/v1/overview", "https://evil.example.com", "central.example.com", "", 204},
		{"POST", "/v1/appliances/register", "https://evil.example.com", "central.example.com", "", 204},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.fwdHost != "" {
			req.Header.Set("X-Forwarded-Host", c.fwdHost)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s from %q (host %s): got %d want %d", c.method, c.path, c.origin, c.host, rec.Code, c.want)
		}
	}
}
