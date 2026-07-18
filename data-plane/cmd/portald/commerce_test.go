package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// capture records what the bridge forwarded to scd.
type capture struct {
	called bool
	method string
	path   string
	query  url.Values
	body   map[string]any
}

// newBridgeHandler builds a portald handler whose scd client routes to a recording test server.
func newBridgeHandler(t *testing.T, portalOn bool, cap *capture) (*handler, func()) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.called = true
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.Query()
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			if len(b) > 0 {
				m := map[string]any{}
				_ = json.Unmarshal(b, &m)
				cap.body = m
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	addr := ts.Listener.Addr().String()
	cfg := iamv2.CommerceConfig{MasterEnabled: portalOn, PortalEnabled: portalOn}
	th, err := newHandler(cfgVal()) // parse the real templates (NotFound renders the landing page)
	if err != nil {
		t.Fatal(err)
	}
	th.scd = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
	}}
	th.commerceCfg = cfg
	th.commerceSessions = newCommerceSessionStore()
	return th, ts.Close
}

func cfgVal() cfg { return cfg{ScdSocket: "/run/stayconnect/scd.sock"} }

const (
	sessAC   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	sessDev  = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	sessGnet = "44444444-4444-4444-4444-444444444444"
)

func withSession(t *testing.T, h *handler, r *http.Request) {
	t.Helper()
	tok := "opaque-token-123"
	h.commerceSessions.put(tok, commerceSession{authContextID: sessAC, deviceID: sessDev, guestNetworkID: sessGnet, expiry: time.Now().Add(time.Hour)})
	r.AddCookie(&http.Cookie{Name: commerceCookie, Value: tok})
}

// Dark: commerce routes are not mounted, so a request falls through to the captive-portal landing and
// the bridge never runs / never calls scd (no commerce JSON, no scd contact).
func TestCommerceRoutesAbsentWhenDark(t *testing.T) {
	cap := &capture{}
	h, closeFn := newBridgeHandler(t, false, cap)
	defer closeFn()
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	for _, p := range []string{"/api/commerce/packages", "/api/commerce/quote", "/api/commerce/confirm"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// unmatched -> landing page (HTML), never the bridge's JSON contract
		if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("dark route %s must not serve the commerce JSON contract", p)
		}
		if strings.Contains(string(body), `"error":"unavailable"`) || strings.Contains(string(body), `"ok":true`) {
			t.Fatalf("dark route %s leaked a commerce response", p)
		}
	}
	if cap.called {
		t.Fatal("dark bridge must never call scd")
	}
}

// No trusted session -> generic unavailable, and scd is never called.
func TestCommerceNoSessionUnavailable(t *testing.T) {
	cap := &capture{}
	h, closeFn := newBridgeHandler(t, true, cap)
	defer closeFn()
	r := httptest.NewRequest("POST", "/api/commerce/quote", strings.NewReader(`{"package_id":"pkg-1"}`))
	w := httptest.NewRecorder()
	h.commerceQuote(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-session quote must be 503, got %d", w.Code)
	}
	if cap.called {
		t.Fatal("no-session quote must not call scd")
	}
}

// The browser cannot substitute Auth Context / Device / Network / Tenant / Site: even when it injects
// those fields, the bridge forwards ONLY the trusted session pins + the opaque package_id.
func TestCommerceBrowserCannotSubstitutePinsQuote(t *testing.T) {
	cap := &capture{}
	h, closeFn := newBridgeHandler(t, true, cap)
	defer closeFn()
	malicious := `{
		"package_id":"pkg-legit",
		"auth_context_id":"EVIL-AC",
		"device_id":"EVIL-DEV",
		"guest_network_id":"EVIL-NET",
		"tenant_id":"EVIL-TENANT",
		"site_id":"EVIL-SITE"
	}`
	r := httptest.NewRequest("POST", "/api/commerce/quote", strings.NewReader(malicious))
	withSession(t, h, r)
	w := httptest.NewRecorder()
	h.commerceQuote(w, r)
	if !cap.called {
		t.Fatal("bridge must call scd with a valid session")
	}
	if cap.body["auth_context_id"] != sessAC || cap.body["device_id"] != sessDev || cap.body["guest_network_id"] != sessGnet {
		t.Fatalf("bridge must forward SESSION pins, got %+v", cap.body)
	}
	if cap.body["package_id"] != "pkg-legit" {
		t.Fatalf("package_id must be the browser's opaque id, got %v", cap.body["package_id"])
	}
	// browser-injected tenant/site must NOT be forwarded at all (scd is authoritative for them)
	if _, ok := cap.body["tenant_id"]; ok {
		t.Fatal("bridge must never forward a browser tenant_id")
	}
	if _, ok := cap.body["site_id"]; ok {
		t.Fatal("bridge must never forward a browser site_id")
	}
}

// Confirm forwards only the opaque quote_id + session device/network, never browser-substituted pins.
func TestCommerceBrowserCannotSubstitutePinsConfirm(t *testing.T) {
	cap := &capture{}
	h, closeFn := newBridgeHandler(t, true, cap)
	defer closeFn()
	r := httptest.NewRequest("POST", "/api/commerce/confirm", strings.NewReader(`{"quote_id":"q-legit","device_id":"EVIL","auth_context_id":"EVIL"}`))
	withSession(t, h, r)
	w := httptest.NewRecorder()
	h.commerceConfirm(w, r)
	if !cap.called || cap.body["quote_id"] != "q-legit" || cap.body["device_id"] != sessDev {
		t.Fatalf("confirm must forward opaque quote_id + session device, got %+v", cap.body)
	}
	if cap.body["auth_context_id"] != nil {
		t.Fatal("confirm must not forward any auth_context_id from the browser")
	}
}

// Packages GET forwards session pins as query params (never browser-supplied ones).
func TestCommercePackagesUsesSessionPins(t *testing.T) {
	cap := &capture{}
	h, closeFn := newBridgeHandler(t, true, cap)
	defer closeFn()
	r := httptest.NewRequest("GET", "/api/commerce/packages?auth_context_id=EVIL&device_id=EVIL", nil)
	withSession(t, h, r)
	w := httptest.NewRecorder()
	h.commercePackages(w, r)
	if !cap.called || cap.query.Get("auth_context_id") != sessAC || cap.query.Get("device_id") != sessDev || cap.query.Get("guest_network_id") != sessGnet {
		t.Fatalf("packages must use SESSION pins, got %+v", cap.query)
	}
}
