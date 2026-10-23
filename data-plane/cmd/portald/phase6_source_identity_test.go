package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Guests reach the portal directly through nftables DNAT with no reverse proxy, so forwarding headers are
// entirely guest-controlled, and clientIP() derives the device and therefore the entitlement. chi's
// middleware.RealIP would rewrite RemoteAddr from them, so it must not be installed.

const (
	callerAddr = "10.90.0.11:41000"
	victimIP   = "10.90.0.22"
)

var spoofHeaders = []string{"X-Forwarded-For", "X-Real-IP", "True-Client-IP", "X-Client-IP", "Forwarded"}

// observedIP reports what clientIP() resolves through the given middleware stack.
func observedIP(t *testing.T, install func(chi.Router), remote string, headers map[string]string) net.IP {
	t.Helper()
	var got net.IP
	r := chi.NewRouter()
	install(r)
	r.Get("/probe", func(w http.ResponseWriter, req *http.Request) { got = clientIP(req) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// realStack mirrors routes() minus the logger; RealIP is absent in both.
func realStack(r chi.Router) {
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
}

func TestSourceIdentityIgnoresEverySpoofHeader(t *testing.T) {
	want := net.ParseIP("10.90.0.11")
	for _, h := range spoofHeaders {
		t.Run(h, func(t *testing.T) {
			got := observedIP(t, realStack, callerAddr, map[string]string{h: victimIP})
			if got == nil {
				t.Fatal("no source address was resolved at all")
			}
			if !got.Equal(want) {
				t.Fatalf("%s redirected the source identity to %s; the connection said %s", h, got, want)
			}
			if got.String() == victimIP {
				t.Fatalf("%s let caller A be resolved as victim B", h)
			}
		})
	}
}

func TestSourceIdentityIgnoresCombinedAndChainedSpoofs(t *testing.T) {
	got := observedIP(t, realStack, callerAddr, map[string]string{
		"X-Forwarded-For": victimIP + ", 203.0.113.9, 198.51.100.4",
		"X-Real-IP":       victimIP,
		"True-Client-IP":  victimIP,
		"Forwarded":       "for=" + victimIP,
	})
	if got == nil || got.String() != "10.90.0.11" {
		t.Fatalf("a combined spoof moved the source identity to %v", got)
	}
}

// The real source must keep working, or the tests above would pass for a resolver returning nothing.
func TestSourceIdentityResolvesTheRealPeer(t *testing.T) {
	got := observedIP(t, realStack, callerAddr, nil)
	if got == nil || got.String() != "10.90.0.11" {
		t.Fatalf("the real peer did not resolve: %v", got)
	}
	other := observedIP(t, realStack, "10.90.0.22:52000", nil)
	if other == nil || other.String() != victimIP {
		t.Fatalf("a different real peer did not resolve to itself: %v", other)
	}
	if got.Equal(other) {
		t.Fatal("caller A and victim B resolved to the same address; the fixtures are not distinct")
	}
}

func TestRealIPMiddlewareIsNotInstalled(t *testing.T) {
	// Control case: RealIP would have moved the identity.
	withRealIP := func(r chi.Router) {
		r.Use(middleware.RequestID)
		r.Use(middleware.RealIP)
		r.Use(middleware.Recoverer)
	}
	spoofed := observedIP(t, withRealIP, callerAddr, map[string]string{"X-Real-IP": victimIP})
	if spoofed == nil || spoofed.String() != victimIP {
		t.Fatalf("control case: RealIP did not rewrite the address as expected (got %v); "+
			"if chi changed, this test needs revisiting rather than deleting", spoofed)
	}

	if got := observedIP(t, realStack, callerAddr, map[string]string{"X-Real-IP": victimIP}); got.String() != "10.90.0.11" {
		t.Fatalf("the service stack honoured X-Real-IP: %v", got)
	}
}

// A source scan is the right tool: the failure is one line added back, and the assembled router cannot be
// tested without a full handler.
func TestRouterSourceDoesNotReferenceRealIP(t *testing.T) {
	src, err := readSourceFile("main.go")
	if err != nil {
		t.Skipf("cannot read main.go: %v", err)
	}
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue // a comment naming it is not a violation
		}
		if strings.Contains(trimmed, "middleware.RealIP") {
			t.Fatalf("middleware.RealIP is installed again: %q\n"+
				"On this direct/DNAT architecture it rewrites RemoteAddr from guest-controlled headers, and "+
				"that address derives the device and therefore the entitlement.", trimmed)
		}
	}
}

func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
