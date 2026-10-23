//go:build devlicense

package main

// The development OAuth stub (devlicense builds only) is not an open redirect (audit P-2).

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTheDevelopmentStubReturnsOnlyToThisPortalsCallback(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){})
	get := func(redirect string) *httptest.ResponseRecorder {
		q := url.Values{"provider": {"google"}, "state": {"s-1"}, "redirect_uri": {redirect}, "email": {"a@example.com"}, "auto": {"1"}}
		r := httptest.NewRequest(http.MethodGet, "http://portal.test:8380/api/oauth/stub/authorize?"+q.Encode(), nil)
		r.RemoteAddr = "10.20.0.50:4321"
		return serveAcct(h, r)
	}
	for _, bad := range []string{"https://evil.example/auth/social/callback", "http://portal.test:8380/elsewhere",
		"http://portal.test:8380/auth/social/callback?x=1", "javascript:alert(1)", "//evil.example/auth/social/callback",
		"http://user@portal.test:8380/auth/social/callback"} {
		if rec := get(bad); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("redirect_uri %q: %d %q", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := get("http://portal.test:8380/auth/social/callback")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "http://portal.test:8380/auth/social/callback?provider=google&state=s-1&code=") {
		t.Fatalf("the portal's own callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	q := url.Values{"provider": {"google"}, "state": {"s-1"}, "redirect_uri": {"http://portal.test:8380/auth/social/callback"}}
	r := httptest.NewRequest(http.MethodGet, "http://portal.test:8380/api/oauth/stub/authorize?"+q.Encode(), nil)
	r.RemoteAddr = "10.20.0.50:4321"
	rec = serveAcct(h, r)
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("the stub page has no CSP: %q", csp)
	}
	r = httptest.NewRequest(http.MethodPost, "http://portal.test:8380/api/oauth/stub/authorize-confirm",
		strings.NewReader(url.Values{"provider": {"google"}, "state": {"s"}, "redirect_uri": {"https://evil.example/cb"}, "email": {"a@example.com"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.20.0.50:4321"
	if rec = serveAcct(h, r); rec.Code != http.StatusBadRequest {
		t.Fatalf("confirm to a foreign redirect_uri: %d", rec.Code)
	}
}
