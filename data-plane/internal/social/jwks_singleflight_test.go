package social

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// P4 L5: a slow key-set fetch (an unknown kid) does not stall a sign-in whose key is cached, and concurrent fetches
// for unknown kids are one request.
func TestJWKSFetchDoesNotHoldTheCache(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer srv.Close()
	defer close(release)
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	c := newJWKSCache(srv.URL, func() *http.Client { return srv.Client() })
	c.keys = map[string]*rsa.PublicKey{"known": &k.PublicKey}
	c.fetchedAt = time.Now().Add(-2 * jwksMinRefetch) // fresh for "known", but an unknown kid may refetch

	for i := 0; i < 3; i++ {
		go func() { _, _ = c.key(context.Background(), "unknown") }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan error, 1)
	go func() { _, err := c.key(context.Background(), "known"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cached key: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cached key waited for another caller's key-set fetch")
	}
	time.Sleep(50 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d concurrent key-set fetches, want 1", n)
	}
}
