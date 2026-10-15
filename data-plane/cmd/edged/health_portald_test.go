package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A loopback probe measures the listener it probes: a redirect elsewhere is that listener's answer, not a
// destination to follow (the HTTPS portal's 302 to its canonical hostname was followed off-box and reported the
// portal degraded).
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

// The HTTPS listener check verifies the served certificate the way a client does: a certificate no system root
// vouches for is a failure, not a pass.
func TestTLSListenerGet_VerifiesTheCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	if _, err := tlsListenerGet(context.Background(), addr, "example.com"); err == nil {
		t.Fatal("an unverifiable certificate passed the HTTPS portal check")
	}
}
