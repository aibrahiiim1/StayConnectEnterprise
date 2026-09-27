package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// exhausted is a counter every bucket of which is already over any limit; it records the keys it was asked for.
type exhausted struct {
	mu   sync.Mutex
	keys []string
}

func (e *exhausted) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keys = append(e.keys, key)
	return 1 << 30, nil
}

// Login and step-up re-authentication are rate-limited on the server-derived client address. Re-auth is
// limited before the session check, so the limit holds without a session too.
func TestLoginAndReauthAreRateLimitedOnTrustedClientAddress(t *testing.T) {
	c := &exhausted{}
	h := NewRouter(Deps{rateCounter: c})
	for _, path := range []string{"/v1/auth/login", "/v1/auth/reauth"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"x","password":"y"}`))
		req.RemoteAddr = "203.0.113.9:40000"
		req.Header.Set("X-Real-IP", "10.9.9.9") // a direct client's claim: ignored
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: want 429 from the limiter, got %d", path, rec.Code)
		}
	}
	want := []string{"rl:login:203.0.113.9", "rl:reauth:203.0.113.9"}
	if len(c.keys) != 2 || c.keys[0] != want[0] || c.keys[1] != want[1] {
		t.Fatalf("limiter keys = %v, want %v", c.keys, want)
	}
}
