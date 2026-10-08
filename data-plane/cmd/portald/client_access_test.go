package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE CLIENT-ACCESS JOURNEY ON THE PORTAL (docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §6).
//
// Three seams that used to be broken or missing:
//   * a verified one-time code and an identity-provider callback now reach package selection through the
//     same commerce session as a voucher or an account (they used to land on /success?s=&t=0);
//   * a verified factor earns the device a resume credential, kept in an HttpOnly cookie;
//   * the package page says who is signed in and why a free package is not on it.

func otpPayload(extra string) []byte {
	return []byte(`{"auth_context_id":"ac-1","device_id":"dev-1","guest_network_id":"gn-1",` +
		`"method":"OTP","authority":"iam_v2","identity_label":"a•••@example.com"` + extra + `}`)
}

func TestAVerifiedCodeStartsTheJourneyAsJSONAndKeepsTheDeviceCredential(t *testing.T) {
	h := portalFixture()
	rec := httptest.NewRecorder()
	if !h.tryIAMv2AuthMode(rec, httptest.NewRequest(http.MethodPost, "/auth/otp/verify", nil), otpPayload(`,"resume_token":"tok-1"`), true) {
		t.Fatal("an IAM-v2 reply must be handled")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Next string `json:"next"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Next != "/packages" {
		t.Fatalf("the page must be told where to go next, got %s", rec.Body.String())
	}
	var commerce, resume *http.Cookie
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case commerceCookie:
			commerce = c
		case clientResumeCookie:
			resume = c
		}
	}
	if commerce == nil || commerce.Value == "" {
		t.Fatal("the commerce session cookie must be issued for a code sign-in exactly as for a voucher")
	}
	if resume == nil || resume.Value != "tok-1" || !resume.HttpOnly {
		t.Fatalf("the resume credential must be kept in an HttpOnly cookie: %+v", resume)
	}
	sess, ok := h.commerceSessions.get(commerce.Value)
	if !ok || sess.identityLabel != "a•••@example.com" {
		t.Fatalf("the session must remember the masked identity, got %+v %v", sess, ok)
	}
}

func TestARefusalAfterTheCodeCarriesItsTranslationKey(t *testing.T) {
	h := portalFixtureCommerceOff()
	rec := httptest.NewRecorder()
	h.tryIAMv2AuthMode(rec, httptest.NewRequest(http.MethodPost, "/auth/otp/verify", nil), otpPayload(""), true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Error, Key string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Key != "err.packages.off" || out.Error == "" {
		t.Fatalf("a refusal must name its translated sentence, got %s", rec.Body.String())
	}
}

func TestNoResumeCookieWithoutACredential(t *testing.T) {
	h := portalFixture()
	rec := httptest.NewRecorder()
	h.tryIAMv2Auth(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), iamv2Payload())
	for _, c := range rec.Result().Cookies() {
		if c.Name == clientResumeCookie {
			t.Fatal("a voucher sign-in earns no device credential; only a verified factor does")
		}
	}
}

// clientAccessHandler is a full portal handler whose scd is the given mux (branding answers empty), with a
// warm neighbour cache for aa:bb:cc:dd:ee:ff.
func clientAccessHandler(t *testing.T, mux *http.ServeMux) *handler {
	t.Helper()
	mux.HandleFunc("/v1/tenant/branding", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"design":{}}`)) })
	mux.HandleFunc("/v1/sessions/resume-revoke", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()
	return &handler{
		commerceCfg:      portalFixture().commerceCfg,
		commerceSessions: newCommerceSessionStore(),
		tmplLand:         mustParse(t, "land", landingHTML),
		tmplSucc:         mustParse(t, "succ", successHTML),
		designs:          &designCache{},
		arpCache:         func(net.IP) (net.HardwareAddr, bool) { return mustMAC("aa:bb:cc:dd:ee:ff"), true },
		scd: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
		}},
	}
}

func TestTheLandingPageGreetsARememberedDeviceAndHidesTheForms(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sessions/resume-peek", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["mac"] != "aa:bb:cc:dd:ee:ff" || in["resume_token"] != "tok-1" || in["ip"] == "" {
			t.Errorf("peek must carry the server-derived device and the cookie, got %v", in)
		}
		w.Write([]byte(`{"ok":true,"identity_label":"a•••@example.com","method":"OTP"}`))
	})
	h := clientAccessHandler(t, mux)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.20.0.50:4321"
	req.AddCookie(&http.Cookie{Name: clientResumeCookie, Value: "tok-1"})
	rec := httptest.NewRecorder()
	h.index(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`id="welcome"`, "a•••@example.com", `action="/auth/resume"`, `action="/auth/resume/forget"`, "Welcome back"} {
		if !strings.Contains(body, want) {
			t.Fatalf("landing page lacks %q", want)
		}
	}
	// The ordinary sign-in forms stay in the page (noscript, and "Not you?") -- the script hides them.
	if !strings.Contains(body, `id="form-voucher"`) {
		t.Fatal("the sign-in forms must remain for Not-you and noscript")
	}
	// Without the cookie: no greeting.
	rec = httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.20.0.50:4321"
	h.index(rec, req2)
	if strings.Contains(rec.Body.String(), `id="welcome"`) {
		t.Fatal("no credential, no greeting")
	}
}

func TestConnectGoesThroughTheResolvingDeviceLookupAndTheSameJourney(t *testing.T) {
	mux := http.NewServeMux()
	refuse := false
	mux.HandleFunc("/v1/sessions/authorize-resume", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["mac"] != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("Connect must send the resolved MAC, got %v", in)
		}
		if refuse {
			w.Write([]byte(`{"ok":false,"reason":"other_device"}`))
			return
		}
		w.Write(otpPayload(""))
	})
	h := clientAccessHandler(t, mux)
	req := httptest.NewRequest(http.MethodPost, "/auth/resume", nil)
	req.RemoteAddr = "10.20.0.50:4321"
	req.AddCookie(&http.Cookie{Name: clientResumeCookie, Value: "tok-1"})
	rec := httptest.NewRecorder()
	h.authResume(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/packages" {
		t.Fatalf("Connect must lead to the same package choice as any sign-in, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// A refused credential clears the cookie and shows the ordinary page.
	refuse = true
	rec = httptest.NewRecorder()
	h.authResume(rec, req)
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == clientResumeCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("a refused credential must be forgotten by the browser too")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Please sign in") {
		t.Fatalf("the ordinary page with a plain sentence, got %d", rec.Code)
	}
}

func TestThePackagePageSaysWhoAndWhyFreeIsGone(t *testing.T) {
	h := clientAccessHandler(t, http.NewServeMux())
	w := httptest.NewRecorder()
	h.renderPackages(w, httptest.NewRequest(http.MethodGet, "/packages", nil),
		[]guestPackage{{PackageID: "p1", Display: map[string]any{"name": "Day pass"}, PriceMinor: 500, Currency: "USD", CurrencyExponent: 2, Methods: []string{"ONLINE_PAYMENT"}}},
		"", "a•••@example.com", true)
	body := w.Body.String()
	for _, want := range []string{"Signed in as", "a•••@example.com", "used your free access"} {
		if !strings.Contains(body, want) {
			t.Fatalf("package page lacks %q", want)
		}
	}
	if strings.Contains(body, "alice@") {
		t.Fatal("the page must never carry the full identity")
	}
}

func TestTheLandingScriptLeadsWithThePrimaryMethodAndQuickSignIn(t *testing.T) {
	// The script is served inside the page; its contract is textual: the primary method is reordered to the
	// front, the identity providers are moved above the tabs, and a remembered device hides the forms.
	for _, want := range []string{"PRIMARY_MAP", "cfg.portal && cfg.portal.primary_method", "insertBefore(quick, tabsEl)",
		"getElementById('welcome')", "delivery_failed", "j.next || '/packages'"} {
		if !strings.Contains(landingHTML, want) {
			t.Fatalf("landing script lacks %q", want)
		}
	}
	// The one-time-code block specifically: it must no longer read the legacy session_id / duration_seconds
	// reply shape (the Phase-3 room and post-stay paths legitimately return a session; they are not this seam).
	start := strings.Index(landingHTML, "codeForm.addEventListener('submit'")
	end := strings.Index(landingHTML, "attach('email')")
	if start < 0 || end < start {
		t.Fatal("the one-time-code block was not found")
	}
	if block := landingHTML[start:end]; strings.Contains(block, "session_id") || strings.Contains(block, "duration_seconds") {
		t.Fatal("the one-time-code verify must not read the legacy session reply")
	}
}

func mustMAC(s string) net.HardwareAddr {
	m, err := net.ParseMAC(s)
	if err != nil {
		panic(err)
	}
	return m
}
