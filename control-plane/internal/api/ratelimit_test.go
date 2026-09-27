package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// memCounter is an in-memory RateCounter.
type memCounter struct {
	mu sync.Mutex
	n  map[string]int64
}

func newMemCounter() *memCounter { return &memCounter{n: map[string]int64{}} }

func (m *memCounter) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n[key]++
	return m.n[key], nil
}

func hit(h http.Handler, remote, xRealIP, xff string) int {
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	req.RemoteAddr = remote
	if xRealIP != "" {
		req.Header.Set("X-Real-IP", xRealIP)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A client talking to ctrlapi directly cannot escape the limit by rotating forwarding headers: its bucket is
// its TCP peer address.
func TestRateLimitIgnoresSpoofedHeadersFromDirectClients(t *testing.T) {
	c := newMemCounter()
	h := RateLimitWith(c, "login", 3, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	spoofs := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4", "5.5.5.5"}
	var codes []int
	for i, s := range spoofs {
		codes = append(codes, hit(h, "203.0.113.7:4000", s, spoofs[len(spoofs)-1-i]))
	}
	if codes[2] != 200 || codes[3] != http.StatusTooManyRequests || codes[4] != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Real-IP/X-Forwarded-For escaped the limit: %v", codes)
	}
	if len(c.n) != 1 || c.n["rl:login:203.0.113.7"] != 5 {
		t.Fatalf("expected one bucket keyed on the TCP peer, got %v", c.n)
	}
}

// Behind the loopback proxy, the proxy's X-Real-IP is the client: two clients get two buckets, and a client's
// own first X-Forwarded-For hop is not believed.
func TestRateLimitTrustsTheLoopbackProxy(t *testing.T) {
	c := newMemCounter()
	h := RateLimitWith(c, "reauth", 1, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if hit(h, "127.0.0.1:5000", "198.51.100.1", "") != 200 || hit(h, "127.0.0.1:5000", "198.51.100.2", "") != 200 {
		t.Fatal("two clients behind the proxy must not share a bucket")
	}
	if hit(h, "127.0.0.1:5000", "198.51.100.1", "") != http.StatusTooManyRequests {
		t.Fatal("the same client behind the proxy must be limited")
	}
	// No X-Real-IP: the LAST hop (appended by the proxy) counts, not the client-chosen first one.
	if hit(h, "127.0.0.1:5000", "", "6.6.6.6, 198.51.100.2") != http.StatusTooManyRequests {
		t.Fatal("a spoofed first X-Forwarded-For hop escaped the limit")
	}
	if c.n["rl:reauth:6.6.6.6"] != 0 {
		t.Fatalf("a client-chosen hop became a bucket: %v", c.n)
	}
}

func TestRateLimitWithoutCounterIsOpen(t *testing.T) {
	h := RateLimitWith(nil, "login", 0, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if hit(h, "203.0.113.7:1", "", "") != 200 {
		t.Fatal("no counter must not block")
	}
}
