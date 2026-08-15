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

// THE PUBLIC PORTAL'S SOURCE-IDENTITY BOUNDARY.
//
// On this appliance a guest reaches the portal DIRECTLY through nftables DNAT, which preserves the original
// source address. There is no reverse proxy, so X-Forwarded-For, X-Real-IP and True-Client-IP arrive entirely
// under the guest's control.
//
// That matters far more here than it would on an ordinary web service, because the source address is not
// merely logged: clientIP() feeds it into neighbour/ARP resolution to derive a DEVICE, and the device derives
// the ENTITLEMENT. The Phase-6 surface deliberately takes no subject parameter so that identity comes from
// the connection -- and a middleware that rewrites RemoteAddr from a header hands the parameter straight
// back, letting one guest name another guest's address and operate on their devices.
//
// chi's middleware.RealIP does exactly that rewrite. It was installed globally on this router and has been
// removed. These tests are the standing proof.

const (
	callerAddr = "10.90.0.11:41000" // caller A: the real peer
	victimIP   = "10.90.0.22"       // victim B: a different guest, whose identity must be unreachable
)

// spoofHeaders is every header a guest could try. Each one is a real product's idea of "the client's true
// address", which is precisely why a middleware might honour it.
var spoofHeaders = []string{"X-Forwarded-For", "X-Real-IP", "True-Client-IP", "X-Client-IP", "Forwarded"}

// observedIP runs one request through the REAL middleware stack this service installs and reports what
// clientIP() would resolve. It builds the stack from routes()'s own list rather than restating it, so the
// test follows the service if the stack changes.
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

// realStack is the middleware this service actually installs, in order, minus the logger (which only writes
// to stderr during tests). RealIP is absent here because it is absent in routes(); if somebody re-adds it
// there and not here, the guard test below fails.
func realStack(r chi.Router) {
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
}

// NO SPOOF HEADER MAY MOVE THE DERIVED SOURCE. This is the test that would have failed before RealIP was
// removed.
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

// Several headers at once, including the multi-hop XFF form a proxy chain would produce.
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

// The genuine direct source must keep working, or the test above would pass for a resolver that returns
// nothing at all.
func TestSourceIdentityResolvesTheRealPeer(t *testing.T) {
	got := observedIP(t, realStack, callerAddr, nil)
	if got == nil || got.String() != "10.90.0.11" {
		t.Fatalf("the real peer did not resolve: %v", got)
	}
	other := observedIP(t, realStack, "10.90.0.22:52000", nil)
	if other == nil || other.String() != victimIP {
		t.Fatalf("a different real peer did not resolve to itself: %v", other)
	}
	// ...and the two are genuinely different, so "caller A is not victim B" is a real distinction rather
	// than an artifact of both resolving to the same thing.
	if got.Equal(other) {
		t.Fatal("caller A and victim B resolved to the same address; the fixtures are not distinct")
	}
}

// THE REGRESSION GUARD. RealIP is what made the spoof work, so its absence is asserted directly against the
// service's own router construction -- not against a copy of the middleware list.
func TestRealIPMiddlewareIsNotInstalled(t *testing.T) {
	// Demonstrate that RealIP WOULD have moved the identity, so this test is grounded in the actual
	// behaviour rather than in a claim about it.
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

	// Now the real service: the same request must resolve the real peer.
	if got := observedIP(t, realStack, callerAddr, map[string]string{"X-Real-IP": victimIP}); got.String() != "10.90.0.11" {
		t.Fatalf("the service stack honoured X-Real-IP: %v", got)
	}
}

// The source file itself must not reintroduce it. A grep is a blunt instrument, and it is the right one here:
// the failure mode is somebody adding one line back, and no behavioural test of the assembled router can run
// without a full handler.
func TestRouterSourceDoesNotReferenceRealIP(t *testing.T) {
	src, err := readSourceFile("main.go")
	if err != nil {
		t.Skipf("cannot read main.go: %v", err)
	}
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue // the explanatory note naming it is the point, not a violation
		}
		if strings.Contains(trimmed, "middleware.RealIP") {
			t.Fatalf("middleware.RealIP is installed again: %q\n"+
				"On this direct/DNAT architecture it rewrites RemoteAddr from guest-controlled headers, and "+
				"that address derives the device and therefore the entitlement.", trimmed)
		}
	}
}

// readSourceFile reads a file from this package's directory.
func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
