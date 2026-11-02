package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The template preview in the Admin Console reads the portal page on loopback. Whatever state the portal's HTTPS is in
// (or flaps between), that read must be answered, never redirected to a name the appliance cannot reach itself.
func TestTheAdminConsolesLoopbackReadIsNeverRedirected(t *testing.T) {
	states := map[string]portalTLSState{
		"HTTPS active, shared name canonical": sharedWithSiteDomain,
		"HTTPS active, own domain canonical":  activeState,
		"HTTP fallback":                       {FallbackReason: "NO_CERTIFICATE", OneGateHostname: tOG, PortalIP: "10.255.255.1", PortalIPActive: true},
		"no state at all":                     {},
	}
	for name, st := range states {
		h := httpsHandler(t, st, nil)
		served := 0
		front := h.httpFront(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }))
		for _, path := range []string{"/", "/api/languages", "/api/branding", "/?profile=7912870f-5890-4933-818b-d1c3f1c43b5b"} {
			served = 0
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8380"+path, nil)
			r.RemoteAddr = "127.0.0.1:41000"
			front.ServeHTTP(w, r)
			if served != 1 || w.Code >= 300 {
				t.Errorf("%s %s: the loopback read was not served (%d, handler ran %d times, Location %q)", name, path, w.Code, served, w.Header().Get("Location"))
			}
		}
	}
}

// A client, or anything a proxy forwards for a client, still goes to the canonical name.
func TestOnlyARealLoopbackReadWithoutForwardingHeadersIsExempt(t *testing.T) {
	h := httpsHandler(t, sharedWithSiteDomain, nil)
	front := h.httpFront(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	cases := []struct {
		name, remote, header, value string
		redirected                  bool
	}{
		{"a client on the guest network", "10.20.0.50:50000", "", "", true},
		{"loopback with X-Forwarded-For (a proxy in front)", "127.0.0.1:41000", "X-Forwarded-For", "10.20.0.50", true},
		{"loopback with X-Real-Ip", "127.0.0.1:41000", "X-Real-Ip", "10.20.0.50", true},
		{"loopback with Forwarded", "127.0.0.1:41000", "Forwarded", "for=10.20.0.50", true},
		{"loopback, nothing forwarded", "127.0.0.1:41000", "", "", false},
		{"IPv6 loopback", "[::1]:41000", "", "", false},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://10.20.0.1:8380/packages", nil)
		r.RemoteAddr = tc.remote
		if tc.header != "" {
			r.Header.Set(tc.header, tc.value)
		}
		front.ServeHTTP(w, r)
		if got := w.Code == http.StatusFound; got != tc.redirected {
			t.Errorf("%s: redirected=%v, want %v (%d)", tc.name, got, tc.redirected, w.Code)
		}
	}
}
