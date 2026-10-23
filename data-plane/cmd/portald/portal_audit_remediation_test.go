package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const auditPurchase = "0f0e0d0c-1111-2222-3333-444455556666"

type auditSCD struct {
	mu     sync.Mutex
	calls  map[string][]map[string]any
	answer map[string]func(in map[string]any) (int, string)
}

func (f *auditSCD) n(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls[path])
}

func (f *auditSCD) last(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls[path]
	if len(c) == 0 {
		return nil
	}
	return c[len(c)-1]
}

// auditHandler is the client-access fixture (MAC aa:bb:cc:dd:ee:ff, commerce on) over a fake scd; every call is
// recorded with its JSON body (or query, for a GET).
func auditHandler(t *testing.T, routes map[string]func(in map[string]any) (int, string)) (*handler, *auditSCD) {
	t.Helper()
	f := &auditSCD{calls: map[string][]map[string]any{}, answer: routes}
	mux := http.NewServeMux()
	for p := range routes {
		path := p
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			in := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&in)
			for k, v := range r.URL.Query() {
				in[k] = v[0]
			}
			f.mu.Lock()
			f.calls[path] = append(f.calls[path], in)
			fn := f.answer[path]
			f.mu.Unlock()
			code, body := fn(in)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		})
	}
	return clientAccessHandler(t, mux), f
}

func fixed(code int, body string) func(map[string]any) (int, string) {
	return func(map[string]any) (int, string) { return code, body }
}

func auditReq(method, path string, form url.Values) *http.Request {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr = "10.20.0.50:4321"
	return r
}

func withCommerce(h *handler, r *http.Request, sess commerceSession) *http.Request {
	if sess.expiry.IsZero() {
		sess.expiry = time.Now().Add(time.Minute)
	}
	h.commerceSessions.put("tok-audit", sess)
	r.AddCookie(&http.Cookie{Name: commerceCookie, Value: "tok-audit"})
	return r
}

func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestFormActionAllowsTheProvidersCheckoutAndThePortalsHTTPSNames(t *testing.T) {
	csp := portalCSP("n0nce")
	for _, want := range []string{"form-action 'self'", "https://checkout.stripe.com", "https://accept.paymob.com", "https://ksa.checkout.paymob.com"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "form-action *") || strings.Contains(csp, "https:;") && strings.Contains(csp, "form-action https:") {
		t.Fatal("form-action must name origins, never a wildcard")
	}
	setPortalFormOrigins(portalTLSState{Canonical: "portal.example.org", Hostnames: []string{"portal.example.org", "wifi.hotel.example"}})
	defer setPortalFormOrigins(portalTLSState{})
	csp = portalCSP("n0nce")
	for _, want := range []string{"https://portal.example.org", "https://wifi.hotel.example"} {
		if !strings.Contains(csp, want) {
			t.Errorf("a form re-posted (307) to the HTTPS portal name %s would be refused: %s", want, csp)
		}
	}
}

func cardRoutes() map[string]func(map[string]any) (int, string) {
	return map[string]func(map[string]any) (int, string){
		"/v1/commerce/quote": fixed(200, `{"quote_id":"q-1"}`),
		"/v1/commerce/confirm": fixed(200, `{"awaiting_settlement":true,"purchase_id":"`+auditPurchase+
			`","redirect_url":"https://checkout.stripe.com/c/pay/cs_test_1"}`),
	}
}

func TestCardChoiceSubmittedByScriptIsToldWhereToGo(t *testing.T) {
	h, _ := auditHandler(t, cardRoutes())
	form := url.Values{"package_id": {"p-1"}, "method": {"ONLINE_PAYMENT"}}
	r := withCommerce(h, auditReq(http.MethodPost, "/packages/acquire", form), commerceSession{authContextID: "ac", deviceID: "dv", guestNetworkID: "gn"})
	r.Header.Set("X-OneGate-Nav", "1")
	rec := serveAcct(h, r)
	var out map[string]string
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out["next"] != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Fatalf("the page's script must be told the provider's URL, got %d %s", rec.Code, rec.Body.String())
	}
	r = withCommerce(h, auditReq(http.MethodPost, "/packages/acquire", form), commerceSession{authContextID: "ac", deviceID: "dv", guestNetworkID: "gn"})
	rec = serveAcct(h, r)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Fatalf("a plain form must be redirected to the provider, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestThePackagePageSubmitsByScriptAndNavigatesWithWindowLocation(t *testing.T) {
	for _, want := range []string{"'X-OneGate-Nav': '1'", "window.location.href", "e.preventDefault()"} {
		if !strings.Contains(packagesHTML, want) {
			t.Errorf("the package page's script lacks %q", want)
		}
	}
}

// A refusal is shown once, from the session, never from a URL.
func TestARefusalTheScriptWasToldAboutIsShownOnThePackagePage(t *testing.T) {
	routes := cardRoutes()
	routes["/v1/commerce/quote"] = fixed(409, `{"error":"unavailable"}`)
	routes["/v1/commerce/packages"] = fixed(200, `{"packages":[{"package_id":"p-1","display":{"name":"Day"}}]}`)
	h, _ := auditHandler(t, routes)
	r := withCommerce(h, auditReq(http.MethodPost, "/packages/acquire", url.Values{"package_id": {"p-1"}}), commerceSession{authContextID: "ac", deviceID: "dv"})
	r.Header.Set("X-OneGate-Nav", "1")
	rec := serveAcct(h, r)
	if !strings.Contains(rec.Body.String(), `"next":"/packages"`) {
		t.Fatalf("got %s", rec.Body.String())
	}
	page := serveAcct(h, withCommerce(h, auditReq(http.MethodGet, "/packages", nil), mustSession(h))).Body.String()
	if !strings.Contains(page, esc(builtinStrings["en"]["err.package.gone"])) {
		t.Fatal("the refusal is not shown on the package page")
	}
	page = serveAcct(h, withCommerce(h, auditReq(http.MethodGet, "/packages", nil), mustSession(h))).Body.String()
	if strings.Contains(page, esc(builtinStrings["en"]["err.package.gone"])) {
		t.Fatal("the refusal must be shown once")
	}
}

func mustSession(h *handler) commerceSession {
	s, _ := h.commerceSessions.get("tok-audit")
	return s
}

func TestTheProviderReturnsToTheCanonicalPortalNotTheHostHeader(t *testing.T) {
	h, f := auditHandler(t, cardRoutes())
	h.portalTLS = &tlsStateHolder{st: portalTLSState{HTTPSActive: true, Canonical: "portal.example.org", Hostnames: []string{"portal.example.org"}}}
	r := withCommerce(h, auditReq(http.MethodPost, "/packages/acquire", url.Values{"package_id": {"p-1"}, "method": {"ONLINE_PAYMENT"}}),
		commerceSession{authContextID: "ac", deviceID: "dv"})
	r.Host = "evil.example"
	r.TLS = &tls.ConnectionState{}
	_ = serveAcct(h, r)
	if got := f.last("/v1/commerce/confirm")["return_base"]; got != "https://portal.example.org" {
		t.Fatalf("return_base = %v, want the canonical HTTPS origin", got)
	}
	rec := serveAcct(h, withCommerce(h, auditReq(http.MethodGet, "/", nil), mustSession(h)))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/pay/return?p="+auditPurchase {
		t.Fatalf("/ after paying: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTheStubOAuthIsAbsentFromAProductionBuild(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){})
	q := url.Values{"provider": {"google"}, "state": {"s"}, "redirect_uri": {"https://evil.example/cb"},
		"email": {"a@example.com"}, "auto": {"1"}}
	rec := serveAcct(h, auditReq(http.MethodGet, "/api/oauth/stub/authorize?"+q.Encode(), nil))
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "evil.example") || rec.Code == http.StatusFound {
		t.Fatalf("the stub redirected in a production build: %d %q", rec.Code, loc)
	}
	rec = serveAcct(h, auditReq(http.MethodPost, "/api/oauth/stub/authorize-confirm", url.Values{"provider": {"google"}, "state": {"s"},
		"redirect_uri": {"https://evil.example/cb"}, "email": {"a@example.com"}}))
	if strings.Contains(rec.Header().Get("Location"), "evil.example") {
		t.Fatal("the stub confirm redirected in a production build")
	}
}

func openRoutes() map[string]func(map[string]any) (int, string) {
	return map[string]func(map[string]any) (int, string){
		"/v1/sessions/authorize-open": fixed(200, `{"auth_context_id":"ac-o","device_id":"dv-o","guest_network_id":"gn-o",`+
			`"method":"OPEN","authority":"iam_v2","resume_token":"anon-tok","recovery_code":"ABCDE-FGHJK"}`),
	}
}

func TestOpenSelectionKeepsTheWelcomeBackCredential(t *testing.T) {
	h, _ := auditHandler(t, openRoutes())
	r := auditReq(http.MethodPost, "/auth/open", url.Values{})
	r.AddCookie(&http.Cookie{Name: clientResumeCookie, Value: "client-tok"})
	rec := serveAcct(h, r)
	if c := setCookie(rec, clientResumeCookie); c != nil {
		t.Fatalf("open selection rewrote og_client: %+v", c)
	}
	if c := setCookie(rec, resumeCookie); c == nil || c.Value != "anon-tok" || !c.HttpOnly {
		t.Fatalf("the anonymous token must go to og_resume, got %+v", c)
	}
}

func TestATypedReturnCodeIsNotOverriddenByTheCookie(t *testing.T) {
	h, f := auditHandler(t, openRoutes())
	r := auditReq(http.MethodPost, "/auth/open", url.Values{"return_code": {"ABCDE-FGHJK"}})
	r.AddCookie(&http.Cookie{Name: resumeCookie, Value: "someone-elses"})
	_ = serveAcct(h, r)
	in := f.last("/v1/sessions/authorize-open")
	if in["recovery_code"] != "ABCDE-FGHJK" || in["resume_token"] != nil {
		t.Fatalf("a typed code must go alone, got %v", in)
	}
	r = auditReq(http.MethodPost, "/auth/open", url.Values{})
	r.AddCookie(&http.Cookie{Name: resumeCookie, Value: "mine"})
	_ = serveAcct(h, r)
	if in := f.last("/v1/sessions/authorize-open"); in["resume_token"] != "mine" {
		t.Fatalf("without a typed code the cookie is used, got %v", in)
	}
}

func TestTheReturnCodeIsShownOnThePaymentAndOnlinePages(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/sessions/status": fixed(200, `{"session_id":"sess-1","active":true}`),
	})
	sess := commerceSession{authContextID: "ac", deviceID: "dv", returnCode: "ABCDE-FGHJK"}
	for _, path := range []string{"/pay/return?p=" + auditPurchase, "/success"} {
		page := serveAcct(h, withCommerce(h, auditReq(http.MethodGet, path, nil), sess)).Body.String()
		if !strings.Contains(page, "ABCDE-FGHJK") {
			t.Errorf("%s does not show the return code", path)
		}
	}
}

func TestSignOutForgetsTheOpenSelectionCookie(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){"/v1/sessions/revoke": fixed(200, `{}`)})
	r := auditReq(http.MethodPost, "/logout", url.Values{"leave": {leaveToken(net.ParseIP("10.20.0.50"))}})
	r.AddCookie(&http.Cookie{Name: resumeCookie, Value: "anon-tok"})
	rec := serveAcct(h, r)
	if c := setCookie(rec, resumeCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("sign-out must clear og_resume, got %+v", c)
	}
}

func TestAThrottledVoucherIsToldToWaitAndScdCodesAreNeverShown(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/sessions/authorize": fixed(429, `{"error":"TOO_MANY_ATTEMPTS"}`),
	})
	page := serveAcct(h, auditReq(http.MethodPost, "/auth/voucher", url.Values{"code": {"ABC123"}})).Body.String()
	if !strings.Contains(page, esc(builtinStrings["en"]["err.attempts"])) || strings.Contains(page, esc(builtinStrings["en"]["err.voucher.invalid"])) {
		t.Fatal("a throttled voucher must be told to wait, not that the code is invalid")
	}
	h.scd = nil
	h2, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/sessions/authorize": fixed(403, `{"error":"SOME_INTERNAL_CODE"}`),
	})
	page = serveAcct(h2, auditReq(http.MethodPost, "/auth/voucher", url.Values{"code": {"ABC123"}})).Body.String()
	if strings.Contains(page, "SOME_INTERNAL_CODE") {
		t.Fatal("scd's code reached the guest")
	}
}

func TestDeviceLimitSentencesNoLongerSayDisconnect(t *testing.T) {
	for _, l := range portalLanguages {
		for _, k := range []string{"err.voucher.devices", "err.account.devices", "err.room.devices"} {
			if s := builtinStrings[l.Code][k]; s == "" || strings.Contains(strings.ToLower(s), "disconnect another device") {
				t.Errorf("%s/%s: %q", l.Code, k, s)
			}
		}
	}
}

func TestYoureOnlineSaysTheRealTimeLeftAndTrustsNothingInItsURL(t *testing.T) {
	ends := time.Now().Add(2*time.Hour + 30*time.Second).UTC().Format(time.RFC3339)
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/sessions/status": fixed(200, `{"session_id":"sess-9","active":true,"access_ends_at":"`+ends+`"}`),
	})
	page := serveAcct(h, auditReq(http.MethodGet, "/success?s=forged&t=99999", nil)).Body.String()
	en := builtinStrings["en"]
	if !strings.Contains(page, ">"+strings.ReplaceAll(en["unit.h"], "{n}", "2")+"<") {
		t.Errorf("the online page does not show the 2 h left")
	}
	if strings.Contains(page, en["online.unlimited"]) || strings.Contains(page, "forged") {
		t.Error("the online page printed a claim from its URL")
	}
	if !strings.Contains(page, `href="/status?s=sess-9"`) {
		t.Error("the Status link must carry scd's session")
	}
	if strings.Contains(page, "/api/commerce/") {
		t.Error("the online page must offer nothing to buy")
	}
	h2, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/sessions/status": fixed(200, `{"session_id":"","active":false}`),
	})
	rec := serveAcct(h2, auditReq(http.MethodGet, "/success?s=forged&t=60", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("a device that is not online must not be told it is: %d", rec.Code)
	}
}

func TestTheCaptiveAPIReportsTheEarlierEnd(t *testing.T) {
	hard := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/sessions/status": fixed(200, `{"session_id":"s","active":true,"time_mode":"AGGREGATE_ONLINE_TIME","remaining_online_seconds":7200,"hard_expiry":"`+hard+`"}`),
	})
	online, left := h.deviceOnline(context.Background(), net.ParseIP("10.20.0.50"))
	if !online || left == nil || *left > 3600 || *left < 3500 {
		t.Fatalf("seconds-remaining must be the hard expiry (~3600), got %v %v", online, left)
	}
}

func TestThePaymentPageOffersAnotherWayAtOnceAfterCancelling(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){})
	page := serveAcct(h, auditReq(http.MethodGet, "/pay/return?p="+auditPurchase+"&cancelled=1", nil)).Body.String()
	if strings.Contains(page, `id="pay-actions" hidden`) || !strings.Contains(page, `href="/packages"`) {
		t.Fatal("a client who left the payment page must be offered another way at once")
	}
	page = serveAcct(h, auditReq(http.MethodGet, "/pay/return?p="+auditPurchase, nil)).Body.String()
	if !strings.Contains(page, `id="pay-actions" hidden`) {
		t.Fatal("while paying, the actions stay hidden")
	}
	for _, want := range []string{"checkout_expiry_seconds", "sessionStorage", "room.failed", "room.review"} {
		if !strings.Contains(page, want) {
			t.Errorf("the payment page lacks %q", want)
		}
	}
}

func TestSignOutNeedsThisDevicesTokenAndIsRecordedAsLogout(t *testing.T) {
	h, f := auditHandler(t, map[string]func(map[string]any) (int, string){"/v1/sessions/revoke": fixed(200, `{}`)})
	rec := serveAcct(h, auditReq(http.MethodPost, "/logout", url.Values{}))
	if rec.Code != http.StatusForbidden || f.n("/v1/sessions/revoke") != 0 {
		t.Fatalf("a sign-out without the device's token must end nothing: %d, %d calls", rec.Code, f.n("/v1/sessions/revoke"))
	}
	if !strings.Contains(rec.Body.String(), `name="leave" value="`+leaveToken(net.ParseIP("10.20.0.50"))+`"`) {
		t.Fatal("the confirmation must carry a fresh token so a real client can still sign out")
	}
	_ = serveAcct(h, auditReq(http.MethodPost, "/logout", url.Values{"leave": {leaveToken(net.ParseIP("10.20.0.51"))}}))
	if f.n("/v1/sessions/revoke") != 0 {
		t.Fatal("another device's token signed this one out")
	}
	rec = serveAcct(h, auditReq(http.MethodPost, "/logout", url.Values{"leave": {leaveToken(net.ParseIP("10.20.0.50"))}}))
	if rec.Code != http.StatusSeeOther || f.n("/v1/sessions/revoke") != 1 || f.last("/v1/sessions/revoke")["reason"] != "logout" {
		t.Fatalf("sign-out: %d, %v", rec.Code, f.last("/v1/sessions/revoke"))
	}
}

func TestEveryDisconnectFormCarriesTheToken(t *testing.T) {
	for name, src := range map[string]string{"packages": packagesHTML, "success": successHTML, "status": statusHTML,
		"replace": replaceHTML, "account": accountHTML, "leave": leaveHTML} {
		forms := strings.Count(src, `action="/logout"`)
		if forms == 0 || strings.Count(src, `name="leave"`) != forms {
			t.Errorf("%s: %d sign-out forms, %d tokens", name, forms, strings.Count(src, `name="leave"`))
		}
	}
}

func TestWelcomeBackKeepsTheCredentialWhenTheRefusalIsAboutTheMoment(t *testing.T) {
	for _, tc := range []struct {
		code  int
		clear bool
	}{{429, false}, {503, false}, {403, true}} {
		h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
			"/v1/sessions/authorize-resume": fixed(tc.code, `{"error":"x"}`),
		})
		r := auditReq(http.MethodPost, "/auth/resume", url.Values{})
		r.TLS = &tls.ConnectionState{} // the remembered device is honoured over HTTPS only
		r.AddCookie(&http.Cookie{Name: clientResumeCookie, Value: "client-tok"})
		rec := serveAcct(h, r)
		c := setCookie(rec, clientResumeCookie)
		if cleared := c != nil && c.MaxAge < 0; cleared != tc.clear {
			t.Errorf("status %d: cleared=%v, want %v", tc.code, cleared, tc.clear)
		}
	}
}

func TestReplacementSpendsOneChangeHoweverManyTaps(t *testing.T) {
	var mu sync.Mutex
	activations := 0
	h, f := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/devices/list":    fixed(200, `{"outcome":"LISTED","limit":1,"in_use":1,"changes_left":3,"devices":[{"id":"d-old","online":false}]}`),
		"/v1/devices/replace": fixed(200, `{"outcome":"REPLACED","entitlement_id":"ent-1","device_id":"dv"}`),
		"/v1/sessions/activate": func(map[string]any) (int, string) {
			mu.Lock()
			defer mu.Unlock()
			activations++
			if activations == 1 {
				return 503, `{"error":"busy"}`
			}
			return 200, `{"enforced":true,"session_id":"sess-new"}`
		},
	})
	sess := commerceSession{authContextID: "ac", deviceID: "dv", guestNetworkID: "gn"}
	_ = serveAcct(h, withCommerce(h, auditReq(http.MethodPost, "/devices/replace", url.Values{"device_id": {"d-old"}}), sess))
	rec := serveAcct(h, withCommerce(h, auditReq(http.MethodPost, "/devices/replace", url.Values{"device_id": {"d-old"}}), mustSession(h)))
	if f.n("/v1/devices/replace") != 1 {
		t.Fatalf("the second tap replaced another device: %d replacements", f.n("/v1/devices/replace"))
	}
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/success") {
		t.Fatalf("the retry must activate on the place already moved: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestReplacementNumbersDevicesLikeTheOnlinePanelWithUniqueNames(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/devices/list": fixed(200, `{"outcome":"LISTED","limit":3,"in_use":3,"changes_left":3,"devices":[`+
			`{"id":"d1","this_device":true},{"id":"d2"},{"id":"d3"}]}`),
	})
	page := serveAcct(h, withCommerce(h, auditReq(http.MethodGet, "/devices/replace", nil), commerceSession{authContextID: "ac", deviceID: "dv"})).Body.String()
	name := func(n string) string { return strings.ReplaceAll(builtinStrings["en"]["dev.name"], "{n}", n) }
	if strings.Contains(page, ">"+name("1")+"<") || !strings.Contains(page, ">"+name("2")+"<") || !strings.Contains(page, ">"+name("3")+"<") {
		t.Fatal("the replacement page must number devices by their place in the package's list")
	}
	if strings.Count(page, `aria-label="`+esc(name("2"))) != 1 || strings.Count(page, `aria-label="`+esc(name("3"))) != 1 {
		t.Fatal("each Use-this-device button needs its own accessible name")
	}
	if !strings.Contains(page, "document.documentElement.lang") {
		t.Fatal("last-used dates must follow the page's language")
	}
}

func TestAnArrivingAlertTakesFocus(t *testing.T) {
	if !strings.Contains(guestScripts, ".notice[role=alert]") || !strings.Contains(guestScripts, "focus(") {
		t.Fatal("pages after sign-in must move focus to a refusal they arrive with")
	}
}

func TestAHeldAccountIsOfferedSignOutAndIsNotSentRoundAgain(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){})
	r := withCommerce(h, auditReq(http.MethodGet, "/account/verification", nil), commerceSession{authContextID: "ac", deviceID: "dv"})
	rec := httptest.NewRecorder()
	h.renderAccountPage(rec, r, nil, true, accountNote{})
	if !strings.Contains(rec.Body.String(), `action="/logout"`) {
		t.Fatal("the hold page must offer Sign out")
	}
	rec = serveAcct(h, withCommerce(h, auditReq(http.MethodGet, "/", nil), mustSession(h)))
	if rec.Code == http.StatusSeeOther && rec.Header().Get("Location") == "/packages" {
		t.Fatal("a held account is sent round to the package list again")
	}
}

func TestCookiesAreSecureOverTLSAndTheRememberedDeviceLivesAsLongAsItsCredential(t *testing.T) {
	r := auditReq(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	setPortalCookie(rec, r, "x", "v", time.Minute)
	if c := setCookie(rec, "x"); c.Secure {
		t.Fatal("a cookie set over plain HTTP cannot be Secure (it would never come back)")
	}
	r.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	setPortalCookie(rec, r, "x", "v", time.Minute)
	setClientResumeCookie(rec, r, "tok", 7)
	if c := setCookie(rec, "x"); !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("over TLS: %+v", c)
	}
	if c := setCookie(rec, clientResumeCookie); !c.Secure || c.MaxAge != 7*24*3600 {
		t.Fatalf("og_client must live the credential's 7 days, got %+v", c)
	}
	rec = httptest.NewRecorder()
	setClientResumeCookie(rec, r, "tok", 0)
	if c := setCookie(rec, clientResumeCookie); c.MaxAge != 30*24*3600 {
		t.Fatalf("og_client defaults to the contract's 30 days, got %d", c.MaxAge)
	}
}

func TestTheCodeRequestComesOnlyFromThePortalsOwnPageAndIsBoundToTheDevice(t *testing.T) {
	h, f := auditHandler(t, map[string]func(map[string]any) (int, string){
		"/v1/auth/otp/issue":      fixed(200, `{"challenge_id":"c-0123456789abcdef0123","ttl_seconds":300}`),
		"/v1/tenant/auth-methods": fixed(200, `{"voucher":{"enabled":true}}`),
	})
	h.pendingOTP = newPendingOTPStore()
	body := `{"channel":"sms","destination":"+201000000000"}`
	send := func(ct, origin string) int {
		r := httptest.NewRequest(http.MethodPost, "/auth/otp/request", strings.NewReader(body))
		r.RemoteAddr = "10.20.0.50:4321"
		r.Header.Set("Content-Type", ct)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return serveAcct(h, r).Code
	}
	if send("text/plain", "") != http.StatusForbidden || send("application/json", "https://evil.example") != http.StatusForbidden {
		t.Fatal("a cross-origin or non-JSON request must not send a message")
	}
	if f.n("/v1/auth/otp/issue") != 0 {
		t.Fatal("scd was asked to send a message for a refused request")
	}
	if code := send("application/json", "http://example.com"); code != 200 || f.n("/v1/auth/otp/issue") != 1 {
		t.Fatalf("the portal's own page must be able to ask: %d", code)
	}
	methods := func() string {
		r := auditReq(http.MethodGet, "/api/auth-methods", nil)
		return serveAcct(h, r).Body.String()
	}
	if !strings.Contains(methods(), "pending_otp") {
		t.Fatal("the pending code must reopen for the device that asked")
	}
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return mustMAC("02:00:00:00:00:99"), true }
	if strings.Contains(methods(), "pending_otp") {
		t.Fatal("another device on the same address was offered the pending code")
	}
}

func TestTheSignInDamper(t *testing.T) {
	d := newSignInDamper()
	now := time.Now()
	d.now = func() time.Time { return now }
	for i := 0; i < signInBurst; i++ {
		if !d.allow("10.0.0.1") {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	if d.allow("10.0.0.1") {
		t.Fatal("the burst must be bounded")
	}
	if !d.allow("10.0.0.2") {
		t.Fatal("one device's limit must not touch another's")
	}
	now = now.Add(signInWindow)
	if !d.allow("10.0.0.1") {
		t.Fatal("the window must pass")
	}
	h, f := auditHandler(t, map[string]func(map[string]any) (int, string){"/v1/sessions/authorize": fixed(403, `{"error":"x"}`)})
	h.signIns = newSignInDamper()
	for i := 0; i < signInBurst+1; i++ {
		_ = serveAcct(h, auditReq(http.MethodPost, "/auth/voucher", url.Values{"code": {"A"}}))
	}
	if f.n("/v1/sessions/authorize") != signInBurst {
		t.Fatalf("scd was asked %d times, want %d", f.n("/v1/sessions/authorize"), signInBurst)
	}
}

func TestARequestThatRunsOutGetsAPageInWords(t *testing.T) {
	h, _ := auditHandler(t, map[string]func(map[string]any) (int, string){})
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	mw := h.portalTimeout(50 * time.Millisecond)(slow)
	r := auditReq(http.MethodPost, "/packages/acquire", url.Values{"package_id": {"p"}})
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, r)
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.timeout"])) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if portalRequestTimeout < 22*time.Second {
		t.Fatal("the deadline must cover quote + confirm + activation")
	}
}

func TestFallbackProbesGoToAnAddressTheDeviceCanReach(t *testing.T) {
	h := &handler{cfg: cfg{PortalFQDN: "portal.stayconnect.local"}}
	r := auditReq(http.MethodGet, "/generate_204", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("10.9.0.1"), Port: 8380}))
	rec := httptest.NewRecorder()
	h.googleProbe(rec, r)
	if rec.Header().Get("Location") != "http://10.9.0.1/" {
		t.Fatalf("probe redirected to %q", rec.Header().Get("Location"))
	}
	h.portalTLS = &tlsStateHolder{st: portalTLSState{PortalIP: "10.255.0.1", PortalIPActive: true}}
	rec = httptest.NewRecorder()
	h.windowsProbe(rec, r)
	if rec.Header().Get("Location") != "http://10.255.0.1/" {
		t.Fatalf("with the portal address up, probes go there: %q", rec.Header().Get("Location"))
	}
}

func TestTheHTTPFallbackKeepsTheJourneyOnThePortalAddress(t *testing.T) {
	h := &handler{portalTLS: &tlsStateHolder{st: portalTLSState{PortalIP: "10.255.0.1", PortalIPActive: true}}}
	passed := false
	front := h.httpFront(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))
	try := func(method, host, path string) *httptest.ResponseRecorder {
		passed = false
		r := httptest.NewRequest(method, "http://"+host+path, nil)
		rec := httptest.NewRecorder()
		front.ServeHTTP(rec, r)
		return rec
	}
	if rec := try(http.MethodGet, "captive.apple.com", "/hotspot-detect.html"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "http://10.255.0.1/hotspot-detect.html" {
		t.Fatalf("a page load on another name must move to the portal address: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range []struct{ method, host, path string }{
		{http.MethodGet, "10.255.0.1", "/"}, {http.MethodPost, "captive.apple.com", "/auth/voucher"},
		{http.MethodGet, "captive.apple.com", "/api/branding"},
	} {
		if try(c.method, c.host, c.path); !passed {
			t.Errorf("%s %s%s must be served where it arrived", c.method, c.host, c.path)
		}
	}
	h.portalTLS.st.PortalIPActive = false
	if try(http.MethodGet, "captive.apple.com", "/"); !passed {
		t.Error("without a working portal address the fallback is unchanged")
	}
}

func TestARoomGuestAtTheDeviceLimitIsOfferedTheReplacementPage(t *testing.T) {
	routes := map[string]func(map[string]any) (int, string){
		"/v1/phase3/auth/pms/grant": fixed(200, `{"outcome":"NOT_VERIFIED","failure_class":"DEVICE_LIMIT","device_id":"dv-room","guest_network_id":"gn-room"}`),
	}
	h, _ := auditHandler(t, routes)
	r := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", strings.NewReader(`{"auth_context_id":"ac-room","package_revision_id":"pr-1"}`))
	r.RemoteAddr = "10.20.0.50:4321"
	r.Header.Set("Content-Type", "application/json")
	rec := serveAcct(h, r)
	var out phase3Out
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || !out.OK || out.RedirectTo != "/devices/replace" {
		t.Fatalf("a room guest at the limit must be sent to the replacement page, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "dv-room") || strings.Contains(rec.Body.String(), "gn-room") {
		t.Fatal("the device and network ids must never reach the guest")
	}
	c := setCookie(rec, commerceCookie)
	if c == nil {
		t.Fatal("no session holds the verified sign-in")
	}
	sess, ok := h.commerceSessions.get(c.Value)
	if !ok || sess.authContextID != "ac-room" || sess.deviceID != "dv-room" || sess.guestNetworkID != "gn-room" {
		t.Fatalf("session pins: %+v", sess)
	}
	// Without the Phase-2 surface there is nowhere to hold the proof, so the sentence is answered.
	h2, _ := auditHandler(t, routes)
	h2.commerceSessions = nil
	r = httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", strings.NewReader(`{"auth_context_id":"ac-room","package_revision_id":"pr-1"}`))
	r.RemoteAddr = "10.20.0.50:4321"
	rec = serveAcct(h2, r)
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.OK || out.Message != guestRoomDeviceLimitMessage {
		t.Fatalf("got %s", rec.Body.String())
	}
}
