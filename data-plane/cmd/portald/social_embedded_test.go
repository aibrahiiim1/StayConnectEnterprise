package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	uaIOSCaptiveSheet = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
	uaIOSCNAClassic   = "CaptiveNetworkSupport-355.60.2 wispr"
	uaIOSSafari       = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	uaIOSChrome       = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/120.0 Mobile/15E148 Safari/604.1"
	uaIOSFirefox      = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/120.0 Mobile/15E148 Safari/605.1.15"
	uaIOSFacebookApp  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 [FBAN/FBIOS;FBAV/430.0]"
	uaMacSafari       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
	uaWindowsEdge     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36 Edg/120.0"
)

// Google refuses sign-in in a view an app controls. On iPhone that is the captive-network sheet iOS opens by itself.
func TestEmbeddedBrowserDetectionCoversTheIOSCaptiveSheet(t *testing.T) {
	for name, ua := range map[string]string{
		"Android WebView": uaAndroidWebView, "iOS captive sheet": uaIOSCaptiveSheet, "iOS captive assistant": uaIOSCNAClassic,
		"an app's in-app browser on iPhone": uaIOSFacebookApp,
	} {
		if !isEmbeddedBrowser(ua) {
			t.Errorf("%s must be treated as an embedded browser", name)
		}
	}
	for name, ua := range map[string]string{
		"Android Chrome": uaAndroidChrome, "Safari on iPhone": uaIOSSafari, "Chrome on iPhone": uaIOSChrome,
		"Firefox on iPhone": uaIOSFirefox, "Safari on a Mac": uaMacSafari, "Edge on Windows": uaWindowsEdge, "no user agent": "",
	} {
		if isEmbeddedBrowser(ua) {
			t.Errorf("%s is a real browser and must be allowed to sign in with Google", name)
		}
	}
}

// Inside the iOS captive sheet Google and Facebook get the "open in your browser" page and no attempt starts; a real
// browser on the same phone, and the providers that accept embedded views, start normally.
func TestIOSCaptiveSheetGetsTheOpenInBrowserPageForGoogleAndFacebook(t *testing.T) {
	for _, provider := range []string{"google", "facebook"} {
		f := &fakeSCD{tls: `{"https_active":true,"canonical_hostname":"portal.onegate.getsemantics.com","fallback_reason":null}`}
		h := socialHandler(t, f)
		w := httptest.NewRecorder()
		h.socialStart(w, socialStartReq(provider, uaIOSCaptiveSheet, "en"))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `id="copy-address"`) || !strings.Contains(body, "https://portal.onegate.getsemantics.com/") {
			t.Errorf("%s: want the open-in-your-browser page with the portal address, got %d", provider, w.Code)
		}
		if len(f.starts) != 0 {
			t.Errorf("%s: no sign-in attempt may start inside the captive sheet: %v", provider, f.starts)
		}
	}
	for _, tc := range []struct{ provider, ua string }{
		{"google", uaIOSSafari}, {"google", uaIOSChrome}, {"apple", uaIOSCaptiveSheet}, {"microsoft", uaIOSCaptiveSheet},
	} {
		f := &fakeSCD{startOut: `{"state":"s","authorize_url":"https://idp.example/authorize?state=s"}`}
		h := socialHandler(t, f)
		w := httptest.NewRecorder()
		h.socialStart(w, socialStartReq(tc.provider, tc.ua, "en"))
		if w.Code != http.StatusFound || len(f.starts) != 1 {
			t.Errorf("%s in %.40q: want a redirect to the provider, got %d (%d attempts)", tc.provider, tc.ua, w.Code, len(f.starts))
		}
	}
}
