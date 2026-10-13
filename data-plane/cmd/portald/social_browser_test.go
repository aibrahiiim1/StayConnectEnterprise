package main

import (
	"context"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	uaAndroidWebView = "Mozilla/5.0 (Linux; Android 12; Pixel 5 Build/SP1A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/120.0 Mobile Safari/537.36"
	uaAndroidChrome  = "Mozilla/5.0 (Linux; Android 12; Pixel 5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36"
)

// fakeSCD answers the routes the social start leg reaches, and records whether an attempt was started.
type fakeSCD struct {
	mu       sync.Mutex
	tls      string // body of /v1/portal/tls-state; "" = 404 (the endpoint not there yet)
	starts   []map[string]string
	startOut string
}

func socialHandler(t *testing.T, f *fakeSCD) *handler {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/portal/tls-state":
			if f.tls == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(f.tls))
		case "/v1/auth/social/start":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.starts = append(f.starts, body)
			_, _ = w.Write([]byte(f.startOut))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()
	// portald holds scd's portal HTTPS state itself (portal_https.go); seed it from the fake's answer.
	var holder *tlsStateHolder
	if f.tls != "" {
		var st portalTLSState
		if err := json.Unmarshal([]byte(f.tls), &st); err != nil {
			t.Fatal(err)
		}
		holder = &tlsStateHolder{st: st}
	}
	return &handler{
		portalTLS: holder,
		designs:   &designCache{},
		arpCache:  func(net.IP) (net.HardwareAddr, bool) { return net.HardwareAddr{0xaa, 0xbb, 0xcc, 1, 2, 3}, true },
		scd: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
		}},
	}
}

func socialStartReq(provider, ua, lang string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/auth/social/start?provider="+provider, nil)
	r.RemoteAddr = "10.77.0.42:51000"
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	r.Header.Set("Accept-Language", lang)
	return r
}

// GOOGLE AND FACEBOOK IN AN ANDROID WEBVIEW: the "open in your browser" page, with the canonical portal
// address and a copy button, a way back to the other methods -- and no sign-in attempt started.
func TestWebViewGetsTheOpenInBrowserPageForGoogleAndFacebook(t *testing.T) {
	for _, provider := range []string{"google", "facebook"} {
		for _, l := range portalLanguages {
			f := &fakeSCD{tls: `{"https_active":true,"canonical_hostname":"wifi.nilehotel.example","fallback_reason":null}`}
			h := socialHandler(t, f)
			w := httptest.NewRecorder()
			h.socialStart(w, socialStartReq(provider, uaAndroidWebView, l.Code))
			body := w.Body.String()
			if w.Code != http.StatusOK {
				t.Fatalf("%s/%s: status %d", provider, l.Code, w.Code)
			}
			words := builtinStrings[l.Code]
			for _, k := range []string{"social.browser.title", "social.browser.copy", "social.browser.other"} {
				if words[k] == "" || !strings.Contains(body, template.HTMLEscapeString(words[k])) {
					t.Errorf("%s/%s: %s is not on the page", provider, l.Code, k)
				}
			}
			name := providerDisplayName(provider)
			if !strings.Contains(body, template.HTMLEscapeString(strings.ReplaceAll(words["social.browser.lead"], "{p}", name))) {
				t.Errorf("%s/%s: the lead does not name %s", provider, l.Code, name)
			}
			if !strings.Contains(body, "https://wifi.nilehotel.example/") || !strings.Contains(body, `id="copy-address"`) {
				t.Errorf("%s/%s: the portal address and its copy button are missing", provider, l.Code)
			}
			if !strings.Contains(body, `href="/"`) {
				t.Errorf("%s/%s: no way back to the other sign-in methods", provider, l.Code)
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
				t.Errorf("%s/%s: the page has no policy", provider, l.Code)
			}
			if len(f.starts) != 0 {
				t.Fatalf("%s/%s: an OAuth attempt was started inside the WebView", provider, l.Code)
			}
		}
	}
}

// Apple and Microsoft allow sign-in in a WebView, and every provider proceeds in a real browser.
func TestOtherProvidersAndRealBrowsersStartTheAttempt(t *testing.T) {
	for _, tc := range []struct{ provider, ua string }{
		{"apple", uaAndroidWebView}, {"microsoft", uaAndroidWebView},
		{"google", uaAndroidChrome}, {"facebook", uaAndroidChrome},
	} {
		f := &fakeSCD{startOut: `{"state":"s","authorize_url":"https://idp.example/authorize?state=s"}`}
		h := socialHandler(t, f)
		w := httptest.NewRecorder()
		h.socialStart(w, socialStartReq(tc.provider, tc.ua, "en"))
		if w.Code != http.StatusFound || w.Header().Get("Location") != "https://idp.example/authorize?state=s" {
			t.Errorf("%s (%s): want a redirect to the provider, got %d %q", tc.provider, tc.ua[:30], w.Code, w.Header().Get("Location"))
		}
		if len(f.starts) != 1 || f.starts[0]["provider"] != tc.provider {
			t.Errorf("%s: the attempt was not started: %v", tc.provider, f.starts)
		}
	}
}

// The address shown is scd's canonical hostname; under the HTTP fallback there is no HTTPS address to give;
// with scd silent, the TLS host the guest is already using; and an invented Host header is never echoed.
func TestTheInterstitialAddress(t *testing.T) {
	h := socialHandler(t, &fakeSCD{tls: `{"https_active":false,"canonical_hostname":null,"fallback_reason":"NO_CERTIFICATE"}`})
	if got := h.portalAddress(socialStartReq("google", uaAndroidWebView, "en")); got != "" {
		t.Errorf("fallback: %q", got)
	}
	h = socialHandler(t, &fakeSCD{}) // tls-state 404
	r := socialStartReq("google", uaAndroidWebView, "en")
	r.Host = "ab12cd34.gw.onegate.work:443"
	r.TLS = nil
	if got := h.portalAddress(r); got != "" {
		t.Errorf("plain HTTP must not invent an HTTPS address: %q", got)
	}
	r = httptest.NewRequest(http.MethodGet, "https://ab12cd34.gw.onegate.work/auth/social/start?provider=google", nil)
	if got := h.portalAddress(r); got != "https://ab12cd34.gw.onegate.work/" {
		t.Errorf("TLS host: %q", got)
	}
	r = httptest.NewRequest(http.MethodGet, "https://x/auth/social/start", nil)
	r.Host = `evil"><script>.example`
	if got := h.portalAddress(r); got != "" {
		t.Errorf("an invented Host header was echoed: %q", got)
	}
}

func TestWebViewDetection(t *testing.T) {
	if !isAndroidWebView(uaAndroidWebView) || isAndroidWebView(uaAndroidChrome) {
		t.Fatal("WebView detection is wrong")
	}
	if isAndroidWebView("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148") {
		t.Fatal("an iOS captive sheet is not an Android WebView")
	}
	for p, want := range map[string]bool{"google": true, "facebook": true, "apple": false, "microsoft": false} {
		if refusesEmbeddedBrowser(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}

// Sign in with Apple returns with a cross-site form POST and no cookie. The callback must carry state + code
// from the form to scd with the device read from the kernel -- and nothing in the request needs a cookie.
func TestAppleFormPostCallbackReachesSCDWithoutACookie(t *testing.T) {
	var got map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions/authorize-social" {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"state expired"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()
	h := &handler{designs: &designCache{},
		arpCache: func(net.IP) (net.HardwareAddr, bool) { return net.HardwareAddr{0xaa, 0xbb, 0xcc, 1, 2, 3}, true },
		scd: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}}}}
	r := httptest.NewRequest(http.MethodPost, "/auth/social/callback", strings.NewReader("state=st-1&code=c-1&user=%7B%7D"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.77.0.42:51000"
	w := httptest.NewRecorder()
	h.socialCallback(w, r)
	if got["state"] != "st-1" || got["code"] != "c-1" || got["mac"] != "aa:bb:cc:01:02:03" || got["ip"] != "10.77.0.42" {
		t.Fatalf("scd was not asked with the form's state/code and the kernel's device: %v", got)
	}
	if got["provider"] != "" {
		t.Errorf("a real provider sends no provider parameter; portald must not invent one: %q", got["provider"])
	}
	if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Errorf("status %d, cookies %v", w.Code, w.Result().Cookies())
	}
}
