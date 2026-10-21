package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type touchCounter struct {
	sess    *Session
	touched int
}

func (b *touchCounter) Get(context.Context, string) (*Session, error) { s := *b.sess; return &s, nil }
func (b *touchCounter) Destroy(context.Context, string) error         { return nil }
func (b *touchCounter) Touch(context.Context, *Session) error         { b.touched++; return nil }

// The idle limit is judged on what the person does. The console's own timer-driven requests (lists refreshing
// themselves, the sign-in-ending check) carry X-OneGate-Background: 1 and do not count as activity; whoami never
// counts; anything else does.
func TestBackgroundRequestsAreNotActivity(t *testing.T) {
	now := time.Now()
	b := &touchCounter{sess: &Session{OperatorID: "op", CreatedAt: now, LastSeenAt: now.Add(-5 * time.Minute)}}
	h := RequireAuth(b, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	send := func(path string, header map[string]string) int {
		b.touched = 0
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "tok"})
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d", path, rec.Code)
		}
		return b.touched
	}
	for _, c := range []struct {
		name   string
		path   string
		header map[string]string
		want   int
	}{
		{"a page the person opened", "/cloud/v1/appliances", nil, 1},
		{"a background refresh of the same list", "/cloud/v1/appliances", map[string]string{"X-OneGate-Background": "1"}, 0},
		{"the header name is case-insensitive", "/cloud/v1/overview", map[string]string{"x-onegate-background": "1"}, 0},
		{"any other value is activity", "/cloud/v1/overview", map[string]string{"X-OneGate-Background": "0"}, 1},
		{"whoami never counts", "/v1/auth/whoami", nil, 0},
		{"background whoami", "/v1/auth/whoami", map[string]string{"X-OneGate-Background": "1"}, 0},
	} {
		if got := send(c.path, c.header); got != c.want {
			t.Errorf("%s: touched %d times, want %d", c.name, got, c.want)
		}
	}
}
