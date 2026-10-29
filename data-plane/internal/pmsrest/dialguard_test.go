package pmsrest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Audit AD-40: the production client (no transport of its own) cannot connect to a loopback address even when the URL
// looks harmless; a caller that supplies a transport (a test server) is left alone.
func TestDefaultClientRefusesLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()

	o := Options{HTTP: &http.Client{Timeout: 3 * time.Second}, Retries: 1}
	o.defaults()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if resp, err := o.HTTP.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the default client connected to a loopback address")
	}

	own := Options{HTTP: srv.Client(), Retries: 1}
	own.defaults()
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := own.HTTP.Do(req2)
	if err != nil {
		t.Fatalf("a caller-supplied transport must be left alone: %v", err)
	}
	_ = resp.Body.Close()
}
