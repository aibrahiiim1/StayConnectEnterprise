package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A redirect is the probed listener's answer, not a destination to follow.
func TestTCPGet_DoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://portal.invalid/", http.StatusFound)
	}))
	defer srv.Close()
	code, err := tcpGet(context.Background(), srv.URL+"/")
	if err != nil || code != http.StatusFound {
		t.Fatalf("tcpGet = %d, %v; want the listener's own 302", code, err)
	}
}

// A certificate no system root vouches for is a failure.
func TestTLSListenerGet_VerifiesTheCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	if _, err := tlsListenerGet(context.Background(), addr, "example.com"); err == nil {
		t.Fatal("an unverifiable certificate passed the HTTPS portal check")
	}
}
