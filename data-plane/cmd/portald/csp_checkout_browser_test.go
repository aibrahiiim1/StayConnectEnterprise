package main

// A REAL BROWSER, THE REAL CSP, A REAL CROSS-ORIGIN CHECKOUT (audit P-1).
//
// The defect was invisible to every handler test: /packages/acquire answered 303 to the provider, which is
// correct, and Chromium then refused the redirect because the page's form-action is 'self'. Only a browser
// enforcing the policy shows it. This drives a headless Chrome (or Chromium / Edge) against the real portald
// handler -- the real package page, its real script and its real Content-Security-Policy -- with a local HTTPS
// server standing in for the provider's hosted checkout on another origin, and asserts where the browser ends up:
//
//	A. the page's own script (fetch + window.location), the provider NOT in form-action  -> reaches the provider
//	B. a plain form submission, the provider NOT in form-action                          -> blocked (the control:
//	   proves the browser enforces form-action on the redirect, so A and C mean something)
//	C. a plain form submission, the provider in form-action (the no-script fallback)      -> reaches the provider
//
// The browser is found at $ONEGATE_TEST_CHROME or the usual install paths; without one the test is skipped (and
// says so). The harness adds one nonce-carrying script that presses the first package button, because
// --dump-dom cannot click; for B and C it submits the form with HTMLFormElement.submit(), which bypasses the
// page's submit handler exactly as a browser without script would.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func findChrome() string {
	if p := os.Getenv("ONEGATE_TEST_CHROME"); p != "" {
		return p
	}
	var cands []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			cands = append(cands, base+`\Google\Chrome\Application\chrome.exe`, base+`\Microsoft\Edge\Application\msedge.exe`)
		}
	case "darwin":
		cands = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	default:
		cands = []string{"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium"}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

var cspNonce = regexp.MustCompile(`'nonce-([A-Za-z0-9_-]+)'`)

func TestCardCheckoutReachesTheProviderInARealBrowser(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium/Edge found (set ONEGATE_TEST_CHROME): the browser-level CSP check did not run")
	}
	// The provider's hosted checkout: another origin, over HTTPS.
	// A navigation that reaches it is counted HERE, on the provider's side: that is the fact under test, and it
	// does not depend on when the browser happens to dump its DOM.
	var hits atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/c/pay/cs_test_1" {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1 id="reached">PROVIDER-CHECKOUT-REACHED</h1></body></html>`))
	}))
	defer provider.Close()
	providerOrigin := strings.Replace(provider.URL, "127.0.0.1", "localhost", 1) // a different host: cross-origin

	saved := hostedCheckoutOrigins
	defer func() { hostedCheckoutOrigins = saved }()

	for _, tc := range []struct {
		name        string
		plainSubmit bool
		allowed     bool
		wantReached bool
	}{
		{"A: the page's script, provider not in form-action", false, false, true},
		{"B: a plain form, provider not in form-action (control)", true, false, false},
		{"C: a plain form, provider in form-action", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			hostedCheckoutOrigins = nil
			if tc.allowed {
				hostedCheckoutOrigins = []string{providerOrigin}
			}
			h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
				"/v1/commerce/packages": fixed(200, `{"packages":[{"package_id":"p-card","display":{"name":"Day pass"},`+
					`"methods":["ONLINE_PAYMENT"],"price_minor":2500,"currency":"EGP","currency_exponent":2}]}`),
				"/v1/commerce/quote": fixed(200, `{"quote_id":"q-1"}`),
				"/v1/commerce/confirm": fixed(200, `{"awaiting_settlement":true,"purchase_id":"`+auditPurchase+
					`","redirect_url":"`+providerOrigin+`/c/pay/cs_test_1"}`),
			})
			h.commerceSessions.put("tok-browser", commerceSession{authContextID: "ac", deviceID: "dv", guestNetworkID: "gn",
				expiry: time.Now().Add(5 * time.Minute)})
			routes := h.routes()
			press := `document.querySelector('.choice-list button').click();`
			if tc.plainSubmit {
				press = `HTMLFormElement.prototype.submit.call(document.querySelector('.choice-list form'));`
			}
			portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The browser's connection is loopback; the portal identifies the device from the address it
				// came from, so the harness presents the fixture's guest address.
				r.RemoteAddr = "10.20.0.50:4321"
				// Plain bytes, so the harness can add its one script.
				r.Header.Del("Accept-Encoding")
				if r.URL.Path == "/start" {
					http.SetCookie(w, &http.Cookie{Name: commerceCookie, Value: "tok-browser", Path: "/"})
					http.Redirect(w, r, "/packages", http.StatusFound)
					return
				}
				rec := httptest.NewRecorder()
				routes.ServeHTTP(rec, r)
				body := rec.Body.Bytes()
				if r.URL.Path == "/packages" {
					m := cspNonce.FindStringSubmatch(rec.Header().Get("Content-Security-Policy"))
					if m == nil {
						t.Errorf("the package page has no nonce CSP")
					} else {
						inject := fmt.Sprintf(`<script nonce="%s">window.addEventListener('load',function(){setTimeout(function(){%s},50);});</script></body>`, m[1], press)
						body = bytes.Replace(body, []byte("</body>"), []byte(inject), 1)
					}
				}
				for k, v := range rec.Header() {
					if k == "Content-Length" || k == "Content-Encoding" {
						continue
					}
					w.Header()[k] = v
				}
				w.WriteHeader(rec.Code)
				_, _ = w.Write(body)
			}))
			defer portal.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			profile := t.TempDir()
			cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-first-run",
				"--no-default-browser-check", "--ignore-certificate-errors", "--user-data-dir="+profile,
				"--virtual-time-budget=8000", "--dump-dom", portal.URL+"/start")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil && len(out) == 0 {
				t.Skipf("the browser could not be run (%v): the browser-level CSP check did not run", err)
			}
			reached := hits.Load() > 0 || strings.Contains(string(out), "PROVIDER-CHECKOUT-REACHED")
			if reached != tc.wantReached {
				t.Fatalf("reached the provider = %v, want %v (browser: %v %.600s). Final DOM:\n%.1500s",
					reached, tc.wantReached, err, stderr.String(), out)
			}
		})
	}
}
