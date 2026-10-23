package main

// Email accounts on the portal: the forms post here, every decision is scd's, portald only carries answers to the right page.
// scd's account token lives in an HttpOnly cookie for fifteen minutes; email links never act on GET, so a mail scanner spends nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// accountCookie holds scd's account_token for its own fifteen-minute lifetime.
	accountCookie     = "og_account"
	accountCookieLife = 15 * time.Minute
)

// Sentences handed to landing(); each is the English of its key in languages_account.go, pinned by a test.
const (
	msgAcctEmpty    = "Please enter your email address and password."
	msgAcctInvalid  = "The email address or password is incorrect. New here, or was your account removed? Tap “Create an account”. Forgot your password? Tap “Forgot password?”."
	msgAcctTaken    = "An account with this email address already exists. Sign in, or tap “Forgot password?” to choose a new password."
	msgAcctEmail    = "Please enter a valid email address."
	msgAcctWeak     = "Choose a password of 8 to 128 characters that is not your email address or a single repeated character."
	msgAcctMismatch = "The two passwords do not match."
	msgAcctLimit    = "This device cannot create another account today. Sign in to an existing account, or contact the site team for assistance."
)

type accountState struct {
	Verified             bool   `json:"verified"`
	VerificationRequired bool   `json:"verification_required"`
	Deadline             string `json:"deadline"`
	Suspended            bool   `json:"suspended"`
	GraceUntil           string `json:"grace_until"`
	GraceAvailable       bool   `json:"grace_available"`
	ResendAvailableIn    int    `json:"resend_available_in"`
}

type accountReply struct {
	AccountToken string        `json:"account_token"`
	Account      *accountState `json:"account"`
}

// reAccountToken bounds what is sent to scd: anything else could not be a token scd issued.
var (
	reAccountToken = regexp.MustCompile(`^[A-Za-z0-9._~=-]{16,1000}$`)
	reLinkToken    = regexp.MustCompile(`^[A-Za-z0-9._~=-]{16,256}$`)
	reSessionRef   = regexp.MustCompile(`^[0-9A-Za-z-]{8,64}$`)
)

func setAccountCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: accountCookie, Value: token, Path: "/",
		// HttpOnly: the page never needs it, and script that cannot read it cannot leak it.
		HttpOnly: true,
		// Secure whenever the page came over TLS; on the HTTP fallback a Secure cookie would never be sent back.
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(accountCookieLife / time.Second),
		Expires:  time.Now().Add(accountCookieLife),
	})
}

// accountCtx carries a token issued during this response to a page that may follow in it.
type accountCtxKey struct{}
type accountCtx struct{ token, email string }

func withAccount(r *http.Request, token, email string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), accountCtxKey{}, accountCtx{token: token, email: email}))
}

func accountTokenFrom(r *http.Request) string {
	if v, ok := r.Context().Value(accountCtxKey{}).(accountCtx); ok && v.token != "" {
		return v.token
	}
	c, err := r.Cookie(accountCookie)
	if err != nil || !reAccountToken.MatchString(c.Value) {
		return ""
	}
	return c.Value
}

// accountEmailFrom prefills the extra-hour form; never stored, never logged.
func accountEmailFrom(r *http.Request) string {
	if v, ok := r.Context().Value(accountCtxKey{}).(accountCtx); ok && v.email != "" {
		return capRunes(v.email, 254)
	}
	if r.Method == http.MethodPost {
		return capRunes(strings.TrimSpace(r.PostFormValue("email")), 254)
	}
	return ""
}

func (h *handler) accountDevice(r *http.Request) (net.IP, net.HardwareAddr, string) {
	ip := clientIP(r)
	if ip == nil {
		return nil, nil, "Unable to detect your device address."
	}
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		return nil, nil, msgDeviceNotOnNetwork
	}
	return ip, mac, ""
}

func (h *handler) accountPost(r *http.Request, path string, body map[string]string) (int, []byte, string, error) {
	raw, _ := json.Marshal(body)
	resp, err := h.scdDo(r.Context(), http.MethodPost, path, raw)
	if err != nil {
		slog.Error("scd account call", "path", path, "err", err)
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(payload, &e)
	return resp.StatusCode, payload, e.Error, nil
}

// register and grace only answer from IAM-v2, so asIAMv2 marks the reply shape.
func asIAMv2(payload []byte) []byte {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return payload
	}
	if a, _ := m["authority"].(string); a != "" {
		return payload
	}
	m["authority"] = "iam_v2"
	if b, err := json.Marshal(m); err == nil {
		return b
	}
	return payload
}

// accountSignedIn keeps the token and sends a suspended account to the verification page, since activation would refuse it.
func (h *handler) accountSignedIn(w http.ResponseWriter, r *http.Request, payload []byte, email string) (*http.Request, bool) {
	var rep accountReply
	_ = json.Unmarshal(payload, &rep)
	if rep.AccountToken != "" && reAccountToken.MatchString(rep.AccountToken) {
		setAccountCookie(w, r, rep.AccountToken)
		r = withAccount(r, rep.AccountToken, email)
	} else if email != "" {
		r = withAccount(r, "", email)
	}
	if rep.Account != nil && rep.Account.Suspended {
		h.renderAccountPage(w, r, rep.Account, true, accountNote{})
		return r, true
	}
	return r, false
}

func (h *handler) accountRegister(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	if email == "" || password == "" {
		h.landing(w, r, msgAcctEmpty)
		return
	}
	if password != r.PostFormValue("password_confirm") {
		h.landing(w, r, msgAcctMismatch)
		return
	}
	ip, mac, fail := h.accountDevice(r)
	if fail != "" {
		h.landing(w, r, fail)
		return
	}
	status, payload, code, err := h.accountPost(r, "/v1/auth/account/register", map[string]string{
		"email": email, "password": password, "ip": ip.String(), "mac": mac.String(),
		"lang": guestLanguage(r, nil).Code,
	})
	if err != nil {
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	if status == http.StatusOK {
		payload = asIAMv2(payload)
		r2, done := h.accountSignedIn(w, r, payload, email)
		if done {
			return
		}
		if !h.tryIAMv2Auth(w, r2, payload) {
			h.landing(w, r2, msgServiceError)
		}
		return
	}
	switch strings.ToLower(code) {
	case "email_taken":
		// The one answer that names an address as registered: throttled by scd like a sign-in.
		h.landing(w, r, msgAcctTaken)
	case "invalid_email":
		h.landing(w, r, msgAcctEmail)
	case "weak_password":
		h.landing(w, r, msgAcctWeak)
	case "registration_limit":
		h.landing(w, r, msgAcctLimit)
	case "too_many_attempts":
		h.landing(w, r, "Too many attempts. Please wait a minute and try again.")
	case "license_capacity_reached":
		h.landing(w, r, guestCapacityMessage)
	case "method_disabled":
		h.landing(w, r, guestLicenseRefusedMessage)
	default:
		if isLicenceRefusal(code) {
			h.landing(w, r, guestLicenseRefusedMessage)
			return
		}
		h.landing(w, r, msgServiceError)
	}
}

// POST /auth/account/signin: the address is the username (self-registered accounts are named by their lower-cased address).
func (h *handler) accountSignIn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	if email == "" || password == "" {
		h.landing(w, r, msgAcctEmpty)
		return
	}
	h.credentialsSignIn(w, r, email, password, true)
}

// POST /auth/account/forgot: the answer is the same for every address.
func (h *handler) accountForgot(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	if email == "" {
		h.landing(w, r, msgAcctEmail)
		return
	}
	ip, mac, fail := h.accountDevice(r)
	if fail != "" {
		h.landing(w, r, fail)
		return
	}
	if _, _, _, err := h.accountPost(r, "/v1/auth/account/forgot", map[string]string{
		"email": email, "ip": ip.String(), "mac": mac.String()}); err != nil {
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	h.renderAccountResult(w, r, http.StatusOK, true, "acct.forgot.title", "acct.forgot.sent")
}

// linkPageHeaders: a page opened from an email link carries its token in the query, so nothing may hand that URL on (no Referer, indexing or caching).
func linkPageHeaders(w http.ResponseWriter) string {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	return setPortalCSP(w)
}

type accountLinkView struct {
	guestPage
	Token  string
	Error  string
	Action string
}

// GET /account/verify?t= verifies nothing; the button's POST does.
func (h *handler) accountVerifyPage(w http.ResponseWriter, r *http.Request) {
	t := strings.TrimSpace(r.URL.Query().Get("t"))
	if !reLinkToken.MatchString(t) {
		h.renderAccountResult(w, r, http.StatusBadRequest, false, "acct.verify.title", "acct.verify.expired")
		return
	}
	nonce := linkPageHeaders(w)
	pg := h.newGuestPage(r, nonce)
	_ = accountVerifyTmpl.Execute(w, accountLinkView{guestPage: pg, Token: t})
}

func (h *handler) accountVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAccountResult(w, r, http.StatusBadRequest, false, "acct.verify.title", "acct.verify.expired")
		return
	}
	t := strings.TrimSpace(r.PostFormValue("t"))
	if !reLinkToken.MatchString(t) {
		h.renderAccountResult(w, r, http.StatusBadRequest, false, "acct.verify.title", "acct.verify.expired")
		return
	}
	status, _, _, err := h.accountPost(r, "/v1/auth/account/verify", map[string]string{"token": t})
	switch {
	case err != nil:
		h.renderAccountResult(w, r, http.StatusBadGateway, false, "acct.verify.title", "err.service")
	case status == http.StatusOK:
		h.renderAccountResult(w, r, http.StatusOK, true, "acct.verified.title", "acct.verified")
	case status == http.StatusGone || status == http.StatusNotFound || status == http.StatusBadRequest:
		h.renderAccountResult(w, r, http.StatusGone, false, "acct.verify.title", "acct.verify.expired")
	default:
		h.renderAccountResult(w, r, http.StatusBadGateway, false, "acct.verify.title", "err.service")
	}
}

// GET /account/reset?t= shows the new-password form; GET changes nothing.
func (h *handler) accountResetPage(w http.ResponseWriter, r *http.Request) {
	t := strings.TrimSpace(r.URL.Query().Get("t"))
	if !reLinkToken.MatchString(t) {
		h.renderAccountResult(w, r, http.StatusBadRequest, false, "acct.reset.title", "acct.reset.expired")
		return
	}
	h.renderResetForm(w, r, http.StatusOK, t, "")
}

func (h *handler) renderResetForm(w http.ResponseWriter, r *http.Request, status int, token, errKey string) {
	nonce := linkPageHeaders(w)
	pg := h.newGuestPage(r, nonce)
	v := accountLinkView{guestPage: pg, Token: token}
	if errKey != "" {
		v.Error = pg.T[errKey]
	}
	w.WriteHeader(status)
	_ = accountResetTmpl.Execute(w, v)
}

// POST /account/reset: scd verifies the address, revokes other tokens and ends live sessions.
func (h *handler) accountReset(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAccountResult(w, r, http.StatusBadRequest, false, "acct.reset.title", "acct.reset.expired")
		return
	}
	t := strings.TrimSpace(r.PostFormValue("t"))
	if !reLinkToken.MatchString(t) {
		h.renderAccountResult(w, r, http.StatusBadRequest, false, "acct.reset.title", "acct.reset.expired")
		return
	}
	password := r.PostFormValue("password")
	if password == "" {
		h.renderResetForm(w, r, http.StatusUnprocessableEntity, t, "err.acct.weak")
		return
	}
	if password != r.PostFormValue("password_confirm") {
		h.renderResetForm(w, r, http.StatusUnprocessableEntity, t, "err.acct.mismatch")
		return
	}
	status, _, code, err := h.accountPost(r, "/v1/auth/account/reset", map[string]string{"token": t, "password": password})
	switch {
	case err != nil:
		h.renderAccountResult(w, r, http.StatusBadGateway, false, "acct.reset.title", "err.service")
	case status == http.StatusOK:
		h.renderAccountResult(w, r, http.StatusOK, true, "acct.reset.done.title", "acct.reset.done")
	case status == http.StatusUnprocessableEntity || strings.EqualFold(code, "weak_password"):
		h.renderResetForm(w, r, http.StatusUnprocessableEntity, t, "err.acct.weak")
	case status == http.StatusGone || status == http.StatusNotFound || status == http.StatusBadRequest:
		h.renderAccountResult(w, r, http.StatusGone, false, "acct.reset.title", "acct.reset.expired")
	default:
		h.renderAccountResult(w, r, http.StatusBadGateway, false, "acct.reset.title", "err.service")
	}
}

type accountNote struct {
	key   string
	n     int
	alert bool
}

func (h *handler) accountStatus(r *http.Request, token string) *accountState {
	if token == "" {
		return nil
	}
	ip, mac, fail := h.accountDevice(r)
	if fail != "" {
		return nil
	}
	status, payload, _, err := h.accountPost(r, "/v1/auth/account/status", map[string]string{
		"account_token": token, "ip": ip.String(), "mac": mac.String()})
	if err != nil || status != http.StatusOK {
		return nil
	}
	var out struct {
		Account *accountState `json:"account"`
	}
	if json.Unmarshal(payload, &out) != nil {
		return nil
	}
	return out.Account
}

type accountView struct {
	guestPage
	Title                              string
	Hold                               bool
	Verified                           bool
	Reminder, ReminderAt, ReminderTmpl string
	Note                               string
	NoteAlert                          bool
	CanResend, Grace                   bool
	Email                              string
	SessionRef                         string
	BackHref, BackLabel                string
}

func (h *handler) renderAccountPage(w http.ResponseWriter, r *http.Request, st *accountState, hold bool, note accountNote) {
	token := accountTokenFrom(r)
	if st == nil {
		st = h.accountStatus(r, token)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	v := accountView{guestPage: p, Hold: hold, Email: accountEmailFrom(r)}
	if st != nil {
		v.Hold = v.Hold || st.Suspended
		v.Verified = st.Verified && !st.Suspended
		v.Grace = v.Hold && st.GraceAvailable
		if !v.Hold && !st.Verified && st.VerificationRequired {
			v.Reminder, v.ReminderAt = reminderFor(p.T, st)
			v.ReminderTmpl = p.T["acct.reminder"]
		}
	}
	v.CanResend = token != "" && !v.Verified
	if v.Hold {
		// The landing page must not continue this sign-in at the package list again.
		h.updateCommerceSession(r, func(s *commerceSession) { s.held = true })
	}
	switch {
	case v.Hold:
		v.Title = p.T["acct.hold.title"]
	case v.Verified:
		v.Title = p.T["acct.verified.title"]
	default:
		v.Title = p.T["acct.verify.title"]
	}
	if note.key != "" {
		v.Note = fill(p.T[note.key], "n", fmt.Sprint(note.n))
		v.NoteAlert = note.alert
	} else if token == "" && !v.Verified {
		v.Note = p.T["err.signin.again"]
	}
	s := ""
	if r.Method == http.MethodPost {
		s = strings.TrimSpace(r.PostFormValue("s"))
	} else {
		s = strings.TrimSpace(r.URL.Query().Get("s"))
	}
	if reSessionRef.MatchString(s) && !v.Hold {
		v.SessionRef = s
		v.BackHref, v.BackLabel = "/success?s="+s, p.T["online.back"]
	} else {
		v.BackHref, v.BackLabel = "/", p.T["errpage.back"]
	}
	_ = accountTmpl.Execute(w, v)
}

// reminderFor names the end of a live extra hour, else the deadline; a moment already past gives the plain how-to.
func reminderFor(words map[string]string, st *accountState) (string, string) {
	at := st.Deadline
	if g, err := time.Parse(time.RFC3339, st.GraceUntil); err == nil && g.After(time.Now()) {
		at = st.GraceUntil
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil || !t.After(time.Now()) {
		return words["acct.verify.how"], ""
	}
	return fill(words["acct.reminder"], "date", t.UTC().Format("2006-01-02 15:04")+" UTC"), t.UTC().Format(time.RFC3339)
}

func (h *handler) accountVerificationPage(w http.ResponseWriter, r *http.Request) {
	h.renderAccountPage(w, r, nil, false, accountNote{})
}

func (h *handler) accountResend(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	token := accountTokenFrom(r)
	if token == "" {
		h.renderAccountPage(w, r, nil, false, accountNote{key: "err.signin.again", alert: true})
		return
	}
	ip, mac, fail := h.accountDevice(r)
	if fail != "" {
		h.renderAccountPage(w, r, nil, false, accountNote{key: serverMessageKey(fail), alert: true})
		return
	}
	status, payload, code, err := h.accountPost(r, "/v1/auth/account/resend", map[string]string{
		"account_token": token, "ip": ip.String(), "mac": mac.String()})
	note := accountNote{key: "err.service", alert: true}
	switch {
	case err != nil:
	case status == http.StatusAccepted || status == http.StatusOK:
		note = accountNote{key: "acct.resent"}
	case strings.EqualFold(code, "resend_wait"):
		var e struct {
			RetryAfter int `json:"retry_after_seconds"`
		}
		_ = json.Unmarshal(payload, &e)
		if e.RetryAfter < 1 {
			e.RetryAfter = 1
		}
		note = accountNote{key: "acct.resend.wait", n: e.RetryAfter, alert: true}
	case strings.EqualFold(code, "resend_limit"):
		note = accountNote{key: "acct.resend.limit", alert: true}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		note = accountNote{key: "err.signin.again", alert: true}
	case status == http.StatusTooManyRequests:
		note = accountNote{key: "err.attempts", alert: true}
	}
	h.renderAccountPage(w, r, nil, false, note)
}

// POST /auth/account/grace requires fresh authentication (address and password, never the cookie).
func (h *handler) accountGrace(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	if email == "" || password == "" {
		h.renderAccountPage(w, r, nil, true, accountNote{key: "err.acct.empty", alert: true})
		return
	}
	ip, mac, fail := h.accountDevice(r)
	if fail != "" {
		h.renderAccountPage(w, r, nil, true, accountNote{key: serverMessageKey(fail), alert: true})
		return
	}
	status, payload, code, err := h.accountPost(r, "/v1/auth/account/grace", map[string]string{
		"email": email, "password": password, "ip": ip.String(), "mac": mac.String()})
	if err != nil {
		h.renderAccountPage(w, r, nil, true, accountNote{key: "err.service", alert: true})
		return
	}
	if status == http.StatusOK {
		payload = asIAMv2(payload)
		r2, done := h.accountSignedIn(w, r, payload, email)
		if done {
			return
		}
		if !h.tryIAMv2Auth(w, r2, payload) {
			h.landing(w, r2, msgServiceError)
		}
		return
	}
	switch strings.ToLower(code) {
	case "grace_unavailable":
		h.renderAccountPage(w, r, nil, true, accountNote{key: "acct.grace.used", alert: true})
	case "not_suspended":
		h.landing(w, r, "Please sign in again.")
	case "invalid_credentials":
		h.renderAccountPage(w, r, nil, true, accountNote{key: "err.acct.invalid", alert: true})
	case "too_many_attempts":
		h.renderAccountPage(w, r, nil, true, accountNote{key: "err.attempts", alert: true})
	default:
		if status == http.StatusUnauthorized {
			h.renderAccountPage(w, r, nil, true, accountNote{key: "err.acct.invalid", alert: true})
			return
		}
		h.renderAccountPage(w, r, nil, true, accountNote{key: "err.service", alert: true})
	}
}

type onlineReminder struct{ Text, At, Tmpl string }

func (h *handler) onlineReminderFor(r *http.Request, words map[string]string) *onlineReminder {
	token := accountTokenFrom(r)
	if token == "" {
		return nil
	}
	st := h.accountStatus(r, token)
	if st == nil || st.Verified || st.Suspended || !st.VerificationRequired {
		return nil
	}
	text, at := reminderFor(words, st)
	return &onlineReminder{Text: text, At: at, Tmpl: words["acct.reminder"]}
}

type accountResultView struct {
	guestPage
	Title, Message string
	OK             bool
}

func (h *handler) renderAccountResult(w http.ResponseWriter, r *http.Request, status int, ok bool, titleKey, key string) {
	nonce := linkPageHeaders(w)
	p := h.newGuestPage(r, nonce)
	w.WriteHeader(status)
	_ = accountResultTmpl.Execute(w, accountResultView{guestPage: p, Title: p.T[titleKey], Message: p.T[key], OK: ok})
}

const acctMomentScript = `
<script nonce="{{.Nonce}}">
  (function () {
    var el = document.querySelector('[data-acct-at]');
    if (!el) return;
    var ts = Date.parse(el.getAttribute('data-acct-at'));
    if (isNaN(ts)) return;
    var d = new Date(ts), when;
    try { when = d.toLocaleString(document.documentElement.lang || undefined); } catch (e) { when = d.toLocaleString(); }
    el.textContent = String(el.getAttribute('data-acct-tmpl') || '').split('{date}').join(when);
  })();
</script>`

const accountHTML = guestHead + `
<title>{{.Title}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <div class="status-icon{{if not .Verified}} status-icon--err{{end}}" aria-hidden="true">{{if .Verified}}` + iconCheck + `{{else}}` + iconAlert + `{{end}}</div>
  <h1 class="page-title">{{.Title}}</h1>
  {{if .Hold}}<p class="page-lead">{{index .T "acct.hold.lead"}}</p>{{end}}
  {{if .Verified}}<p class="page-lead">{{index .T "acct.verified"}}</p>{{end}}
  {{with .Reminder}}<p class="page-lead"{{with $.ReminderAt}} data-acct-at="{{.}}" data-acct-tmpl="{{$.ReminderTmpl}}"{{end}}>{{.}}</p>{{end}}
  {{with .Note}}<div class="notice show{{if $.NoteAlert}} notice--error{{end}}" role="{{if $.NoteAlert}}alert{{else}}status{{end}}">{{.}}</div>{{end}}
  {{if not .Verified}}<p class="page-lead">{{index .T "acct.verify.how"}}</p>{{end}}
  {{if .CanResend}}<div class="actions">
    <form method="post" action="/auth/account/resend" id="form-acct-resend">{{with .SessionRef}}<input type="hidden" name="s" value="{{.}}">{{end}}<button class="btn" type="submit">{{index .T "acct.resend"}}</button></form>
  </div>{{end}}
  {{if .Grace}}<div class="section">
    <h2>{{index .T "acct.grace"}}</h2>
    <p class="lead">{{index .T "acct.grace.lead"}}</p>
    <form method="post" action="/auth/account/grace" class="acct-form" id="form-acct-grace">
      <div class="field">
        <label for="grace-email">{{index .T "email.dest"}}</label>
        <input id="grace-email" name="email" type="email" required autocomplete="email" autocapitalize="none" spellcheck="false" maxlength="254" dir="ltr"{{with .Email}} value="{{.}}"{{end}}>
      </div>
      <div class="field">
        <label for="grace-password">{{index .T "account.pass"}}</label>
        <input id="grace-password" name="password" type="password" required autocomplete="current-password" maxlength="128">
      </div>
      <button class="primary" type="submit">{{index .T "acct.grace"}}</button>
    </form>
  </div>{{end}}
  <!-- A HELD ACCOUNT IS OFFERED SIGN OUT (audit P-13). "Back" led to the landing page, which continued the
       sign-in at the package list, which led back here: a loop with no way to use another account. -->
  <div class="actions">{{if .Hold}}<form method="post" action="/logout"><input type="hidden" name="leave" value="{{.Leave}}"><button class="btn btn--outline" type="submit">{{index .T "pkg.signout"}}</button></form>{{else}}<a class="btn btn--outline" href="{{.BackHref}}">{{.BackLabel}}</a>{{end}}</div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + acctMomentScript + `
</body></html>`

const accountVerifyHTML = guestHead + `
<title>{{index .T "acct.verify.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <h1 class="page-title">{{index .T "acct.verify.title"}}</h1>
  <p class="page-lead">{{index .T "acct.verify.lead"}}</p>
  <form method="post" action="/account/verify" class="acct-form" id="form-acct-verify">
    <input type="hidden" name="t" value="{{.Token}}">
    <button class="primary" type="submit">{{index .T "acct.verify.btn"}}</button>
  </form>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`

const accountResetHTML = guestHead + `
<title>{{index .T "acct.reset.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <h1 class="page-title">{{index .T "acct.reset.title"}}</h1>
  {{with .Error}}<div class="notice show notice--error" role="alert" id="reset-err">{{.}}</div>{{end}}
  <form method="post" action="/account/reset" class="acct-form" id="form-acct-reset" autocomplete="off">
    <input type="hidden" name="t" value="{{.Token}}">
    <div class="field">
      <label for="reset-password">{{index .T "acct.reset.new"}}</label>
      <input id="reset-password" name="password" type="password" required autocomplete="new-password" minlength="8" maxlength="128" aria-describedby="reset-rule{{if .Error}} reset-err{{end}}">
      <p class="hint" id="reset-rule">{{index .T "acct.rule"}}</p>
    </div>
    <div class="field">
      <label for="reset-password2">{{index .T "acct.confirm"}}</label>
      <input id="reset-password2" name="password_confirm" type="password" required autocomplete="new-password" minlength="8" maxlength="128">
    </div>
    <button class="primary" type="submit">{{index .T "acct.reset.btn"}}</button>
  </form>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`

const accountResultHTML = guestHead + `
<title>{{.Title}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <div class="status-icon{{if not .OK}} status-icon--err{{end}}" aria-hidden="true">{{if .OK}}` + iconCheck + `{{else}}` + iconAlert + `{{end}}</div>
  <h1 class="page-title">{{.Title}}</h1>
  <p class="page-lead"{{if not .OK}} role="alert"{{end}}>{{.Message}}</p>
  <div class="actions"><a class="btn" href="/">{{index .T "acct.continue"}}</a></div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`

var (
	accountTmpl       = template.Must(template.New("account").Parse(compactMarkup(accountHTML)))
	accountVerifyTmpl = template.Must(template.New("account-verify").Parse(compactMarkup(accountVerifyHTML)))
	accountResetTmpl  = template.Must(template.New("account-reset").Parse(compactMarkup(accountResetHTML)))
	accountResultTmpl = template.Must(template.New("account-result").Parse(compactMarkup(accountResultHTML)))
)
