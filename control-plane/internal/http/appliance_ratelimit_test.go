package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// X-10: the signed appliance routes are rate-limited per client address BEFORE authentication, so an
// unauthenticated caller cannot drive appliance lookups and audit writes without bound.
func TestApplianceRoutesAreRateLimitedBeforeAuthentication(t *testing.T) {
	c := &exhausted{}
	h := NewRouter(Deps{rateCounter: c})
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/appliance/hello"},
		{http.MethodPost, "/v1/appliance/offline-reconcile"},
	} {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`))
		req.RemoteAddr = "198.51.100.30:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s %s: want 429 from the limiter (before any database lookup), got %d", rt.method, rt.path, rec.Code)
		}
	}
	for _, k := range c.keys {
		if k != "rl:appliance:198.51.100.30" {
			t.Fatalf("limiter key %q, want rl:appliance:198.51.100.30", k)
		}
	}
	if len(c.keys) != 2 {
		t.Fatalf("want 2 limiter checks, got %v", c.keys)
	}
}
