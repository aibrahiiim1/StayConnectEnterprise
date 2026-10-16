package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// N-10: the Caddy check asks whether TLS is served, not Caddy's (now owner-only) admin API.
func TestCaddyCheckProbesTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if ok, detail, _ := caddyServesTLS(context.Background(), srv.Listener.Addr().String()); !ok {
		t.Fatalf("a TLS listener was reported down: %s", detail)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if ok, _, _ := caddyServesTLS(context.Background(), addr); ok {
		t.Fatal("a closed port was reported as serving TLS")
	}
}
