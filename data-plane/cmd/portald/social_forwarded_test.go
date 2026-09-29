package main

import (
	"net/http/httptest"
	"testing"
)

// The portal's HTTPS is terminated by the appliance's own proxy; its scheme is believed only from loopback.
func TestForwardedHTTPSOnlyFromTheLoopbackProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/auth/social/start", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.RemoteAddr = "127.0.0.1:40000"
	if !forwardedHTTPS(r) {
		t.Fatal("loopback proxy's https not honoured")
	}
	r.RemoteAddr = "10.77.0.42:51000"
	if forwardedHTTPS(r) {
		t.Fatal("a guest set X-Forwarded-Proto and was believed")
	}
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-Proto", "http")
	if forwardedHTTPS(r) {
		t.Fatal("http treated as https")
	}
}
