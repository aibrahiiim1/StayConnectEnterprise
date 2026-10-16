package main

// EMAIL ACCOUNTS ON THE PORTAL (account_handlers.go; docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md).
// The scd side is a fake speaking contract section 7 exactly; these pin what portald does with each answer.

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAccountSCD records every account call portald makes and answers with what the test set.
type fakeAccountSCD struct {
	mu      sync.Mutex
	calls   map[string][]map[string]any
	answers map[string]func(in map[string]any) (int, string)
}

func (f *fakeAccountSCD) got(path string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeAccountSCD) on(path string, fn func(in map[string]any) (int, string)) {
	f.answers[path] = fn
}

const acctToken = "acct.tok-0123456789abcdef"

func acctAuthReply(extra string) string {
	// register and grace replies carry no "authority" (contract section 7); portald knows they are IAM-v2.
	return `{"auth_context_id":"ac-9","device_id":"dev-9","guest_network_id":"gn-9","method":"ACCOUNT",` +
		`"live_entitlement_id":null,"account_token":"` + acctToken + `"` + extra + `}`
}

func accountHandler(t *testing.T) (*handler, *fakeAccountSCD) {
	t.Helper()
	f := &fakeAccountSCD{calls: map[string][]map[string]any{}, answers: map[string]func(map[string]any) (int, string){}}
	mux := http.NewServeMux()
	for _, p := range []string{"/v1/auth/account/register", "/v1/sessions/authorize-credentials", "/v1/auth/account/forgot",
		"/v1/auth/account/verify", "/v1/auth/account/reset", "/v1/auth/account/status", "/v1/auth/account/resend",
		"/v1/auth/account/grace", "/v1/sessions/activate", "/v1/sessions/status"} {
		path := p
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.calls[path] = append(f.calls[path], in)
			fn := f.answers[path]
			f.mu.Unlock()
			code, body := 404, `{"error":"not here"}`
			if fn != nil {
				code, body = fn(in)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		})
	}
	return clientAccessHandler(t, mux), f
}

func formReq(path string, v url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.20.0.50:4321"
	return r
}

func serveAcct(h *handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, r)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func reg(email, pw, confirm string) url.Values {
	return url.Values{"email": {email}, "password": {pw}, "password_confirm": {confirm}}
}

// ---- register ----------------------------------------------------------------------------------------------

func TestRegistrationSignsTheClientInAndGoesStraightToThePackages(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/auth/account/register", func(map[string]any) (int, string) {
		return 200, acctAuthReply(`,"account":{"verified":false,"verification_required":true,"deadline":"2099-01-01T00:00:00Z","suspended":false}`)
	})
	req := formReq("/auth/account/register", reg("  Ann@Example.com ", "correct horse", "correct horse"))
	req.AddCookie(&http.Cookie{Name: "sc-lang", Value: "de"})
	rec := serveAcct(h, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/packages" {
		t.Fatalf("registration must continue to package selection: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	in := f.got("/v1/auth/account/register")
	if len(in) != 1 || in[0]["email"] != "Ann@Example.com" || in[0]["password"] != "correct horse" ||
		in[0]["mac"] != "aa:bb:cc:dd:ee:ff" || in[0]["ip"] != "10.20.0.50" || in[0]["lang"] != "de" {
		t.Fatalf("register must carry the form, the kernel's device and the reading language: %v", in)
	}
	if c := cookieNamed(rec, commerceCookie); c == nil || c.Value == "" {
		t.Fatal("the commerce session is issued exactly as for any sign-in")
	}
	c := cookieNamed(rec, accountCookie)
	if c == nil || c.Value != acctToken || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.MaxAge != 900 || c.Secure {
		t.Fatalf("the account token is an HttpOnly, Lax, 15-minute cookie (not Secure over plain HTTP): %+v", c)
	}
	if strings.Contains(rec.Body.String(), "correct horse") {
		t.Fatal("a password must never come back in a page")
	}
}

func TestTheAccountCookieIsSecureOverTLS(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/auth/account/register", func(map[string]any) (int, string) { return 200, acctAuthReply("") })
	req := formReq("/auth/account/register", reg("ann@example.com", "correct horse", "correct horse"))
	req.TLS = &tls.ConnectionState{}
	rec := serveAcct(h, req)
	if c := cookieNamed(rec, accountCookie); c == nil || !c.Secure || !c.HttpOnly {
		t.Fatalf("over TLS the account cookie is Secure: %+v", c)
	}
}

func TestAnAddressAlreadyRegisteredOffersSignInOrAReset(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/auth/account/register", func(map[string]any) (int, string) { return 409, `{"error":"email_taken"}` })
	rec := serveAcct(h, formReq("/auth/account/register", reg("ann@example.com", "correct horse", "correct horse")))
	body := rec.Body.String()
	if !strings.Contains(body, esc(builtinStrings["en"]["err.acct.taken"])) {
		t.Fatal("the client must be told the address already has an account")
	}
	// The page opens on Sign in with the address filled in; Forgot password is one tap away; Create is closed.
	if !strings.Contains(body, `id="form-acct-signin" data-acct="signin">`) {
		t.Error("the Sign in state must be the one showing")
	}
	if !strings.Contains(body, `id="form-acct-create" data-acct="create" style="display:none"`) {
		t.Error("the Create state must be closed")
	}
	if !strings.Contains(body, `id="acct-email" name="email"`) || !strings.Contains(body, `value="ann@example.com"`) {
		t.Error("the address must be given back on the Sign in form")
	}
	if !strings.Contains(body, `data-acct-go="forgot"`) {
		t.Error("the reset path must be offered")
	}
	if c := cookieNamed(rec, commerceCookie); c != nil {
		t.Fatal("a refused registration signs nobody in")
	}
}

func TestAWeakPasswordAndAMismatchAreSaidInWords(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/auth/account/register", func(map[string]any) (int, string) { return 422, `{"error":"weak_password"}` })
	rec := serveAcct(h, formReq("/auth/account/register", reg("ann@example.com", "aaaaaaaa", "aaaaaaaa")))
	body := rec.Body.String()
	if !strings.Contains(body, esc(builtinStrings["en"]["err.acct.weak"])) {
		t.Fatal("a weak password must be explained")
	}
	if !strings.Contains(body, `id="form-acct-create" data-acct="create">`) || !strings.Contains(body, `value="ann@example.com"`) {
		t.Error("the Create state stays open, with the address kept")
	}
	if strings.Contains(body, "aaaaaaaa") {
		t.Fatal("the password must not be given back")
	}
	// Two different passwords never reach scd.
	rec = serveAcct(h, formReq("/auth/account/register", reg("ann@example.com", "correct horse", "correct hors")))
	if !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.acct.mismatch"])) {
		t.Fatal("a mismatch must be said")
	}
	if n := len(f.got("/v1/auth/account/register")); n != 1 {
		t.Fatalf("a mismatch must not be sent to scd (%d calls)", n)
	}
	// The registration limit and the throttle each have their sentence.
	f.on("/v1/auth/account/register", func(map[string]any) (int, string) { return 429, `{"error":"registration_limit"}` })
	if body := serveAcct(h, formReq("/auth/account/register", reg("b@example.com", "correct horse", "correct horse"))).Body.String(); !strings.Contains(body, esc(builtinStrings["en"]["err.acct.limit"])) {
		t.Error("the per-device registration limit must be explained")
	}
	f.on("/v1/auth/account/register", func(map[string]any) (int, string) { return 429, `{"error":"TOO_MANY_ATTEMPTS"}` })
	if body := serveAcct(h, formReq("/auth/account/register", reg("b@example.com", "correct horse", "correct horse"))).Body.String(); !strings.Contains(body, esc(builtinStrings["en"]["err.attempts"])) {
		t.Error("the throttle must be explained")
	}
}

// ---- sign in -----------------------------------------------------------------------------------------------

func TestEmailSignInIsTheAccountSignInWithTheAddress(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/sessions/authorize-credentials", func(in map[string]any) (int, string) {
		if in["password"] != "correct horse" {
			return 401, `{"error":"AUTH_DENIED"}`
		}
		return 200, `{"auth_context_id":"ac-9","device_id":"dev-9","guest_network_id":"gn-9","method":"ACCOUNT","authority":"iam_v2","account_token":"` + acctToken + `","account":{"verified":true}}`
	})
	rec := serveAcct(h, formReq("/auth/account/signin", url.Values{"email": {"ann@example.com"}, "password": {"correct horse"}}))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/packages" {
		t.Fatalf("sign-in continues to packages: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if in := f.got("/v1/sessions/authorize-credentials"); len(in) != 1 || in[0]["username"] != "ann@example.com" || in[0]["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("the address is the username: %v", in)
	}
	if c := cookieNamed(rec, accountCookie); c == nil || c.Value != acctToken {
		t.Fatal("sign-in keeps the account token too")
	}
	// A wrong password reads as the email form's sentence, on the Email panel, address kept.
	rec = serveAcct(h, formReq("/auth/account/signin", url.Values{"email": {"ann@example.com"}, "password": {"nope nope"}}))
	body := rec.Body.String()
	if !strings.Contains(body, esc(builtinStrings["en"]["err.acct.invalid"])) || !strings.Contains(body, `value="ann@example.com"`) {
		t.Fatal("a refused email sign-in says so and keeps the address")
	}
	if !strings.Contains(body, `const SERVER_FORM = "email"`) {
		t.Error("the page must reopen on the Email panel")
	}
}

// ---- forgot ------------------------------------------------------------------------------------------------

func TestForgotPasswordAlwaysAnswersTheSame(t *testing.T) {
	h, f := accountHandler(t)
	want := esc(builtinStrings["en"]["acct.forgot.sent"])
	for _, ans := range []struct {
		code int
		body string
	}{{202, `{}`}, {404, `{"error":"unknown"}`}, {429, `{"error":"TOO_MANY_ATTEMPTS"}`}, {500, `{"error":"boom"}`}} {
		a := ans
		f.on("/v1/auth/account/forgot", func(map[string]any) (int, string) { return a.code, a.body })
		rec := serveAcct(h, formReq("/auth/account/forgot", url.Values{"email": {"who@example.com"}}))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("scd %d: the answer must be the generic one, got %d", a.code, rec.Code)
		}
	}
	if in := f.got("/v1/auth/account/forgot"); len(in) != 4 || in[0]["email"] != "who@example.com" || in[0]["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("forgot carries the address and the device: %v", in)
	}
}

// ---- the links in the emails -------------------------------------------------------------------------------

func TestTheVerifyLinkShowsAButtonAndOnlyThePostVerifies(t *testing.T) {
	h, f := accountHandler(t)
	verified := 200
	f.on("/v1/auth/account/verify", func(map[string]any) (int, string) { return verified, `{"verified":true}` })
	tok := "tok_0123456789abcdefABCDEF"
	get := httptest.NewRequest(http.MethodGet, "/account/verify?t="+tok, nil)
	get.RemoteAddr = "10.20.0.50:4321"
	rec := serveAcct(h, get)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `action="/account/verify"`) || !strings.Contains(body, `name="t" value="`+tok+`"`) {
		t.Fatalf("the link page must carry one Verify form: %d", rec.Code)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("a page with a token in its address must not hand it on: %v", rec.Header())
	}
	if len(f.got("/v1/auth/account/verify")) != 0 {
		t.Fatal("GET must verify nothing: a mail scanner following the link spends nothing")
	}
	rec = serveAcct(h, formReq("/account/verify", url.Values{"t": {tok}}))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.verified"])) {
		t.Fatalf("the button verifies: %d", rec.Code)
	}
	if in := f.got("/v1/auth/account/verify"); len(in) != 1 || in[0]["token"] != tok {
		t.Fatalf("verify carries the token: %v", in)
	}
	verified = 410
	rec = serveAcct(h, formReq("/account/verify", url.Values{"t": {tok}}))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.verify.expired"])) {
		t.Fatalf("a spent link says so: %d", rec.Code)
	}
	// A malformed token is refused without asking scd.
	bad := httptest.NewRequest(http.MethodGet, "/account/verify?t=%3Cscript%3E", nil)
	if rec = serveAcct(h, bad); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("malformed link: %d", rec.Code)
	}
	if n := len(f.got("/v1/auth/account/verify")); n != 2 {
		t.Fatalf("only the two button presses reached scd, got %d", n)
	}
}

func TestTheResetLinkShowsAFormAndThePostResets(t *testing.T) {
	h, f := accountHandler(t)
	reset := 200
	f.on("/v1/auth/account/reset", func(map[string]any) (int, string) {
		if reset == 422 {
			return 422, `{"error":"weak_password"}`
		}
		return reset, `{}`
	})
	tok := "tok_0123456789abcdefABCDEF"
	rec := serveAcct(h, httptest.NewRequest(http.MethodGet, "/account/reset?t="+tok, nil))
	body := rec.Body.String()
	for _, want := range []string{`action="/account/reset"`, `name="t" value="` + tok + `"`, `name="password"`, `name="password_confirm"`, `autocomplete="new-password"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the reset form lacks %q", want)
		}
	}
	if len(f.got("/v1/auth/account/reset")) != 0 {
		t.Fatal("GET must change nothing")
	}
	// A mismatch is refused here; scd is not asked.
	rec = serveAcct(h, formReq("/account/reset", url.Values{"t": {tok}, "password": {"new password 1"}, "password_confirm": {"new password 2"}}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.acct.mismatch"])) ||
		!strings.Contains(rec.Body.String(), `name="t" value="`+tok+`"`) {
		t.Fatalf("mismatch: %d", rec.Code)
	}
	if len(f.got("/v1/auth/account/reset")) != 0 {
		t.Fatal("a mismatch must not reach scd")
	}
	rec = serveAcct(h, formReq("/account/reset", url.Values{"t": {tok}, "password": {"new password 1"}, "password_confirm": {"new password 1"}}))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.reset.done"])) {
		t.Fatalf("reset: %d", rec.Code)
	}
	if in := f.got("/v1/auth/account/reset"); len(in) != 1 || in[0]["token"] != tok || in[0]["password"] != "new password 1" {
		t.Fatalf("reset carries token and password: %v", in)
	}
	reset = 422
	rec = serveAcct(h, formReq("/account/reset", url.Values{"t": {tok}, "password": {"aaaaaaaa"}, "password_confirm": {"aaaaaaaa"}}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.acct.weak"])) {
		t.Fatalf("weak: %d", rec.Code)
	}
	reset = 410
	rec = serveAcct(h, formReq("/account/reset", url.Values{"t": {tok}, "password": {"new password 1"}, "password_confirm": {"new password 1"}}))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.reset.expired"])) {
		t.Fatalf("expired: %d", rec.Code)
	}
}

// ---- verification required ---------------------------------------------------------------------------------

func suspendedReply(grace bool) string {
	g := "false"
	if grace {
		g = "true"
	}
	return `{"auth_context_id":"ac-9","device_id":"dev-9","guest_network_id":"gn-9","method":"ACCOUNT","authority":"iam_v2",` +
		`"account_token":"` + acctToken + `","account":{"verified":false,"verification_required":true,` +
		`"deadline":"2020-01-01T00:00:00Z","suspended":true,"grace_available":` + g + `}}`
}

func TestASuspendedAccountSeesResendAndTheExtraHourOnlyWhenAvailable(t *testing.T) {
	for _, grace := range []bool{true, false} {
		h, f := accountHandler(t)
		g := grace
		f.on("/v1/sessions/authorize-credentials", func(map[string]any) (int, string) { return 200, suspendedReply(g) })
		rec := serveAcct(h, formReq("/auth/account/signin", url.Values{"email": {"ann@example.com"}, "password": {"correct horse"}}))
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, esc(builtinStrings["en"]["acct.hold.title"])) ||
			!strings.Contains(body, esc(builtinStrings["en"]["acct.hold.lead"])) || !strings.Contains(body, esc(builtinStrings["en"]["acct.verify.how"])) {
			t.Fatalf("grace=%v: the verification page must explain: %d", grace, rec.Code)
		}
		if !strings.Contains(body, `action="/auth/account/resend"`) {
			t.Errorf("grace=%v: Resend must be offered", grace)
		}
		hasGrace := strings.Contains(body, `action="/auth/account/grace"`)
		if hasGrace != grace {
			t.Errorf("grace=%v: the extra hour is offered only while scd says it is available (shown=%v)", grace, hasGrace)
		}
		if grace && (!strings.Contains(body, `id="grace-password" name="password" type="password" required`) || !strings.Contains(body, `value="ann@example.com"`)) {
			t.Error("the extra hour asks for the password again, with the address filled in")
		}
		if cookieNamed(rec, commerceCookie) != nil {
			t.Fatal("a suspended account is not offered packages")
		}
		if c := cookieNamed(rec, accountCookie); c == nil || c.Value != acctToken {
			t.Fatal("the token is kept, so Resend works")
		}
		if len(f.got("/v1/sessions/activate")) != 0 {
			t.Fatal("a suspended account is not activated")
		}
	}
}

func TestAnActivationRefusedForVerificationShowsThePage(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/sessions/authorize-credentials", func(map[string]any) (int, string) {
		return 200, `{"auth_context_id":"ac-9","device_id":"dev-9","guest_network_id":"gn-9","method":"ACCOUNT","authority":"iam_v2",` +
			`"live_entitlement_id":"ent-1","account_token":"` + acctToken + `","account":{"verified":false,"verification_required":true,"suspended":false}}`
	})
	f.on("/v1/sessions/activate", func(map[string]any) (int, string) { return 403, `{"error":"VERIFICATION_REQUIRED"}` })
	f.on("/v1/auth/account/status", func(in map[string]any) (int, string) {
		if in["account_token"] != acctToken {
			return 401, `{"error":"invalid_token"}`
		}
		return 200, `{"account":{"verified":false,"verification_required":true,"suspended":true,"grace_available":true}}`
	})
	rec := serveAcct(h, formReq("/auth/account/signin", url.Values{"email": {"ann@example.com"}, "password": {"correct horse"}}))
	body := rec.Body.String()
	if !strings.Contains(body, esc(builtinStrings["en"]["acct.hold.title"])) || !strings.Contains(body, `action="/auth/account/grace"`) {
		t.Fatalf("VERIFICATION_REQUIRED at activation must show the verification page: %d", rec.Code)
	}
	if in := f.got("/v1/auth/account/status"); len(in) != 1 || in[0]["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("the page asks scd for the state with the token issued in this response: %v", in)
	}
}

func TestTheExtraHourRequiresThePasswordAgain(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/auth/account/grace", func(in map[string]any) (int, string) {
		if in["password"] != "correct horse" {
			return 401, `{"error":"invalid_credentials"}`
		}
		return 200, acctAuthReply(`,"account":{"verified":false,"verification_required":true,"suspended":false,"grace_until":"2099-01-01T00:00:00Z"}`)
	})
	// No password: nothing is sent.
	rec := serveAcct(h, formReq("/auth/account/grace", url.Values{"email": {"ann@example.com"}}))
	if !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.acct.empty"])) || len(f.got("/v1/auth/account/grace")) != 0 {
		t.Fatal("the extra hour without a password must not reach scd")
	}
	// A cookie is not a password.
	req := formReq("/auth/account/grace", url.Values{"email": {"ann@example.com"}})
	req.AddCookie(&http.Cookie{Name: accountCookie, Value: acctToken})
	serveAcct(h, req)
	if len(f.got("/v1/auth/account/grace")) != 0 {
		t.Fatal("the account cookie must never stand in for the password")
	}
	// A wrong password says so on the same page.
	rec = serveAcct(h, formReq("/auth/account/grace", url.Values{"email": {"ann@example.com"}, "password": {"wrong one"}}))
	if !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.acct.invalid"])) {
		t.Fatal("a wrong password must be said")
	}
	// The right one continues like a sign-in.
	rec = serveAcct(h, formReq("/auth/account/grace", url.Values{"email": {"ann@example.com"}, "password": {"correct horse"}}))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/packages" {
		t.Fatalf("the extra hour continues to activation like a sign-in: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	in := f.got("/v1/auth/account/grace")
	if len(in) != 2 || in[1]["email"] != "ann@example.com" || in[1]["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("grace carries the address, password and device: %v", in)
	}
	// Used up: said in words.
	f.on("/v1/auth/account/grace", func(map[string]any) (int, string) { return 409, `{"error":"grace_unavailable"}` })
	rec = serveAcct(h, formReq("/auth/account/grace", url.Values{"email": {"ann@example.com"}, "password": {"correct horse"}}))
	if !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.grace.used"])) {
		t.Fatal("an exhausted extra hour must be explained")
	}
}

func TestResendUsesTheCookieAndSaysWhenToWait(t *testing.T) {
	h, f := accountHandler(t)
	f.on("/v1/auth/account/status", func(map[string]any) (int, string) {
		return 200, `{"account":{"verified":false,"verification_required":true,"suspended":true}}`
	})
	code, body := 202, `{}`
	f.on("/v1/auth/account/resend", func(map[string]any) (int, string) { return code, body })
	withCookie := func() *http.Request {
		r := formReq("/auth/account/resend", url.Values{})
		r.AddCookie(&http.Cookie{Name: accountCookie, Value: acctToken})
		return r
	}
	rec := serveAcct(h, withCookie())
	if !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.resent"])) {
		t.Fatal("a resend must be confirmed")
	}
	if in := f.got("/v1/auth/account/resend"); len(in) != 1 || in[0]["account_token"] != acctToken || in[0]["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("resend carries the cookie's token and the device: %v", in)
	}
	code, body = 429, `{"error":"resend_wait","retry_after_seconds":42}`
	if rec = serveAcct(h, withCookie()); !strings.Contains(rec.Body.String(), esc(fill(builtinStrings["en"]["acct.resend.wait"], "n", "42"))) {
		t.Fatal("the wait must be said, in seconds")
	}
	code, body = 429, `{"error":"resend_limit"}`
	if rec = serveAcct(h, withCookie()); !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["acct.resend.limit"])) {
		t.Fatal("the daily limit must be said")
	}
	// Without the cookie nothing is sent, and the client is told to sign in again.
	rec = serveAcct(h, formReq("/auth/account/resend", url.Values{}))
	if len(f.got("/v1/auth/account/resend")) != 3 || !strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.signin.again"])) {
		t.Fatal("resend without a recent sign-in must not reach scd")
	}
	if strings.Contains(rec.Body.String(), `action="/auth/account/resend"`) {
		t.Error("without a token there is no Resend button to press")
	}
}

func TestYoureOnlineRemindsAnUnverifiedAccount(t *testing.T) {
	h, f := accountHandler(t)
	deadline := time.Now().Add(20 * time.Hour).UTC().Format(time.RFC3339)
	state := `{"account":{"verified":false,"verification_required":true,"deadline":"` + deadline + `","suspended":false}}`
	f.on("/v1/auth/account/status", func(map[string]any) (int, string) { return 200, state })
	// The online page looks up this device's session; the reminder's Resend carries the session scd named.
	f.on("/v1/sessions/status", func(map[string]any) (int, string) {
		return 200, `{"session_id":"0f0e0d0c-1111-2222-3333-444455556666","active":true}`
	})
	req := httptest.NewRequest(http.MethodGet, "/success?s=forged&t=3600", nil)
	req.RemoteAddr = "10.20.0.50:4321"
	req.AddCookie(&http.Cookie{Name: accountCookie, Value: acctToken})
	body := serveAcct(h, req).Body.String()
	if !strings.Contains(body, `id="acct-reminder"`) || !strings.Contains(body, `data-acct-at="`+deadline+`"`) ||
		!strings.Contains(body, `action="/auth/account/resend"`) || !strings.Contains(body, `name="s" value="0f0e0d0c-1111-2222-3333-444455556666"`) {
		t.Fatal("the online page must remind an unverified account, with the deadline and Resend")
	}
	// Verified: nothing to say. No cookie: scd is not asked.
	state = `{"account":{"verified":true,"verification_required":true}}`
	if body = serveAcct(h, req).Body.String(); strings.Contains(body, `id="acct-reminder"`) {
		t.Fatal("a verified account is not reminded")
	}
	n := len(f.got("/v1/auth/account/status"))
	plain := httptest.NewRequest(http.MethodGet, "/success?s=x&t=0", nil)
	plain.RemoteAddr = "10.20.0.50:4321"
	serveAcct(h, plain)
	if len(f.got("/v1/auth/account/status")) != n {
		t.Fatal("without the account cookie the online page asks scd nothing")
	}
}

// ---- the page and its words --------------------------------------------------------------------------------

func TestTheEmailPanelIsTheAccountAndTheCodeIsGone(t *testing.T) {
	for _, want := range []string{"cfg.email_account && cfg.email_account.enabled", "email_account:'email'",
		`action="/auth/account/signin"`, `action="/auth/account/register"`, `action="/auth/account/forgot"`,
		`data-acct-go="forgot"`, `data-acct-go="create"`, `data-acct-go="signin"`} {
		if !strings.Contains(landingHTML, want) {
			t.Errorf("the landing page lacks %q", want)
		}
	}
	for _, gone := range []string{`data-otp="email"`, "attach('email')", "showTempAccess", "temp-access", "temporary_access",
		"cfg.email.enabled", "cfg.email &&", "otp.temp.", "otp.sent.email", "otp.retry.email", `data-i18n="help.email"`} {
		if strings.Contains(landingHTML, gone) {
			t.Errorf("the landing page still carries the email code: %q", gone)
		}
	}
	// SMS and WhatsApp keep their code boxes and the shared code route.
	for _, keep := range []string{`data-otp="sms"`, `data-otp="whatsapp"`, "attach('sms')", "attach('whatsapp')", "'/auth/otp/request'"} {
		if !strings.Contains(landingHTML, keep) {
			t.Errorf("the phone codes must stay: %q", keep)
		}
	}
}

func TestTheAccountSentencesAreTheirKeysEnglish(t *testing.T) {
	for msg, key := range map[string]string{
		msgAcctEmpty: "err.acct.empty", msgAcctInvalid: "err.acct.invalid", msgAcctTaken: "err.acct.taken",
		msgAcctEmail: "err.acct.email", msgAcctWeak: "err.acct.weak", msgAcctMismatch: "err.acct.mismatch",
		msgAcctLimit: "err.acct.limit",
	} {
		if builtinStrings["en"][key] != msg {
			t.Errorf("%s reads %q in the dictionary but the handler sends %q", key, builtinStrings["en"][key], msg)
		}
		if serverMessageKey(msg) != key {
			t.Errorf("%q is not localised as %s", msg, key)
		}
	}
	// Every account word ships in every language (the general completeness test covers the rest).
	for _, l := range portalLanguages {
		if n, want := len(accountStrings[l.Code]), len(accountStrings["en"]); n != want {
			t.Errorf("%s has %d account strings, English has %d", l.Code, n, want)
		}
	}
}

func TestATokenInALinkIsNeverLogged(t *testing.T) {
	var logged string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { logged = r.RequestURI })
	for _, u := range []string{"/account/verify?t=tok_0123456789abcdef", "/account/reset?t=tok_0123456789abcdef"} {
		r := httptest.NewRequest(http.MethodGet, u, nil)
		redactLoggedQuery(next).ServeHTTP(httptest.NewRecorder(), r)
		if strings.Contains(logged, "tok_") {
			t.Fatalf("the request log would carry the token: %q", logged)
		}
	}
}
