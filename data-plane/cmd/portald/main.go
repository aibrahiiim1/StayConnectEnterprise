// portald is the captive portal: unauthenticated guest HTTP/HTTPS lands here via nftables DNAT and sign-in is authorized through scd.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/reqlog"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
)

// Portal HTTPS certificates come from scd per hostname (portal_https.go) and are served by SNI; there is no static certificate setting.
type cfg struct {
	HTTPAddr   string
	HTTPSAddr  string
	ScdSocket  string
	PortalFQDN string
}

func loadCfg() cfg {
	return cfg{
		HTTPAddr:   envOr("PORTALD_HTTP_ADDR", ":8380"),
		HTTPSAddr:  envOr("PORTALD_HTTPS_ADDR", ":8343"),
		ScdSocket:  envOr("PORTALD_SCD_SOCKET", "/run/stayconnect/scd.sock"),
		PortalFQDN: envOr("PORTALD_FQDN", "portal.stayconnect.local"),
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type handler struct {
	cfg      cfg
	scd      *http.Client
	tmplLand *template.Template
	tmplSucc *template.Template
	arpCache arpLookup
	// pendingOTP holds each device's in-flight code so the landing page can reopen on the code box (otp_pending.go).
	pendingOTP *pendingOTPStore
	// arpNudge is a seam so tests can drive resolution without a network.
	arpNudge arpNudge
	// arpSleep is the resolver's own wait, off the budget's clock so the budget keeps exactly one pad.
	arpSleep arpSleep

	// commerceSessions is nil unless commerceCfg mounts the commerce routes; nil means the surface does not exist here, and every reader must check it.
	commerceCfg      iamv2.CommerceConfig
	commerceSessions *commerceSessionStore

	// designs caches the published portal design briefly; nil disables it.
	designs *designCache

	// clock drives the Phase-3 response-time budget; nil is the real clock and only tests set it.
	clock phase3Clock

	// portalTLS is portald's copy of scd's portal HTTPS state, refreshed every 15 s; nil means the HTTP fallback.
	portalTLS *tlsStateHolder

	// signIns bounds how fast one device may post to the sign-in routes; nil disables it.
	signIns *signInDamper
}

func newHandler(c cfg) (*handler, error) {
	tland, err := template.New("land").Parse(compactMarkup(landingHTML))
	if err != nil {
		return nil, err
	}
	tsucc, err := template.New("succ").Parse(compactMarkup(successHTML))
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.ScdSocket)
		},
	}
	// Phase 2 DARK commerce config (env-only, all flags default OFF; fail closed on a malformed value).
	commCfg, err := iamv2.LoadCommerceConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	h := &handler{
		cfg:         c,
		scd:         &http.Client{Transport: tr, Timeout: 5 * time.Second},
		tmplLand:    tland,
		tmplSucc:    tsucc,
		arpCache:    defaultArp,
		pendingOTP:  newPendingOTPStore(),
		arpNudge:    defaultArpNudge,
		commerceCfg: commCfg,
		designs:     &designCache{},
		portalTLS:   &tlsStateHolder{},
		signIns:     newSignInDamper(),
	}
	// The commerce session store exists only behind the gate that mounts the routes, so the commerceSessions == nil guards (e.g. tryIAMv2Auth's "packages unavailable" refusal) stay reachable when the surface is off.
	if commCfg.PortalOn() {
		h.commerceSessions = newCommerceSessionStore()
	}
	return h, nil
}

// clientIP uses the connection only: nftables DNAT preserves the source address, and no middleware may rewrite RemoteAddr from forwarding headers (see routes()), because this address becomes a device and then an entitlement.
func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// portalAssetDir is written by Hotel Admin and served here. Both run as the same service user.
const portalAssetDir = "/opt/stayconnect/portal-assets"

// neuteredFS serves files only: http.FileServer would list directories and publish every uploaded image, including replaced ones, to the guest network before sign-in.
type neuteredFS struct{ fs http.FileSystem }

func (n neuteredFS) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.IsDir() {
		_ = f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

func (h *handler) landing(w http.ResponseWriter, r *http.Request, errMsg string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	nonce := setPortalCSP(w)
	// The device's own addresses come from the connection and ARP table, never a header, and are shown only in the information panel the guest opens.
	var ipStr, macStr string
	if ip := clientIP(r); ip != nil {
		ipStr = ip.String()
		// Guarded: an unwired ARP lookup must not panic the landing page.
		// Deliberately the plain cache read, not the resolving lookup: every captive probe hits this page every few seconds, nothing is refused here, and a missing MAC only omits the support line.
		if h.arpCache != nil {
			if mac, ok := h.arpCache(ip); ok {
				macStr = mac.String()
			}
		}
	}
	// The published design is applied at first paint in the guest's language; the handler's message choice is untouched.
	d, ok := h.guestDesign(r)
	if !ok || d == nil {
		d = map[string]any{}
	}
	v := newLandingView(r, d, nonce, errMsg, ipStr, macStr)
	// The greeting is offered only on a plain visit: a refusal being rendered (errMsg) is answered as itself.
	if errMsg == "" && r.Method == http.MethodGet {
		v.Welcome = h.welcomeFor(r, macStr)
	}
	_ = h.tmplLand.Execute(w, v)
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	// A client who has signed in but not chosen continues at package selection while the commerce session is valid (10 minutes); captive probes carry no cookie.
	// Not continued there: a card payment in progress goes to the page asking where it stands (no second purchase), and an email account held for verification is not sent to a package it cannot use.
	if r.Method == http.MethodGet {
		if sess, ok := h.resolveCommerceSession(r); ok && !sess.acquired {
			switch {
			case sess.purchaseID != "":
				http.Redirect(w, r, "/pay/return?p="+sess.purchaseID, http.StatusSeeOther)
				return
			case !sess.held:
				http.Redirect(w, r, "/packages", http.StatusSeeOther)
				return
			}
		}
	}
	h.landing(w, r, "")
}

func (h *handler) authVoucher(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	code := strings.TrimSpace(strings.ToUpper(r.FormValue("code")))
	if code == "" {
		h.landing(w, r, "Please enter a voucher code.")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "Unable to detect your device address.")
		return
	}
	// The MAC comes from the kernel neighbour table and the kernel is asked to resolve it (arp_resolve.go).
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		slog.Warn("device could not be placed on a guest network", "ip", ip.String())
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return
	}

	body, _ := json.Marshal(map[string]string{
		"ip":      ip.String(),
		"mac":     mac.String(),
		"voucher": code,
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/sessions/authorize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd call", "err", err)
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		msg := msgVoucherInvalid
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &e)
		switch {
		case e.Error == "TOO_MANY_ATTEMPTS" || resp.StatusCode == http.StatusTooManyRequests:
			// The durable sign-in throttle refused the attempt: the client waits, the code may be fine.
			msg = msgTooManyAttempts
		case e.Error == "MAX_DEVICES_REACHED":
			msg = msgVoucherDevices
		case e.Error == "LICENSE_CAPACITY_REACHED":
			msg = guestCapacityMessage
		case e.Error == "METHOD_DISABLED" || isLicenceRefusal(e.Error):
			msg = guestLicenseRefusedMessage
		case resp.StatusCode >= 500:
			msg = "Service unavailable. Please try again."
		}
		// Any other refusal is the invalid-voucher sentence; scd's code is never shown or echoed.
		h.landing(w, r, msg)
		return
	}
	// IAM-v2 replies carry no session: the guest has authenticated, not connected. tryIAMv2Auth issues the commerce session and sends them to package selection; a legacy reply falls through.
	if h.tryIAMv2Auth(w, r, payload) {
		return
	}
	var ok2 struct {
		SessionID       string `json:"session_id"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	_ = json.Unmarshal(payload, &ok2)
	http.Redirect(w, r, "/success?s="+ok2.SessionID+"&t="+fmt.Sprint(ok2.DurationSeconds), http.StatusSeeOther)
}

func (h *handler) authCredentials(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		h.landing(w, r, "Please enter your username and password.")
		return
	}
	h.credentialsSignIn(w, r, username, password, false)
}

// credentialsSignIn is the account sign-in both forms share; scd decides the account kind and whether the method is on, and only a refusal's wording differs.
func (h *handler) credentialsSignIn(w http.ResponseWriter, r *http.Request, username, password string, emailForm bool) {
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "Unable to detect your device address.")
		return
	}
	// The MAC comes from the kernel neighbour table and the kernel is asked to resolve it (arp_resolve.go).
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return
	}
	body, _ := json.Marshal(map[string]string{
		"ip": ip.String(), "mac": mac.String(), "username": username, "password": password,
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/sessions/authorize-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd authorize-credentials", "err", err)
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &e)
		msg := "Invalid username or password."
		if emailForm {
			msg = msgAcctInvalid
		}
		switch {
		case e.Error == "LICENSE_CAPACITY_REACHED":
			msg = guestCapacityMessage
		case e.Error == "MAX_DEVICES_REACHED":
			msg = msgAccountDevices
		case e.Error == "TOO_MANY_ATTEMPTS":
			msg = "Too many attempts. Please wait a minute and try again."
		case e.Error == "METHOD_DISABLED" || isLicenceRefusal(e.Error):
			msg = guestLicenseRefusedMessage
		}
		h.landing(w, r, msg)
		return
	}
	// An account reply carries its token and state: the token serves Resend and the reminder, and a suspended account is shown the verification page instead of packages.
	email := ""
	if emailForm {
		email = username
	}
	r, done := h.accountSignedIn(w, r, payload, email)
	if done {
		return
	}
	// IAM-v2 replies carry no session: the guest has authenticated, not connected. tryIAMv2Auth issues the commerce session and sends them to package selection; a legacy reply falls through.
	if h.tryIAMv2Auth(w, r, payload) {
		return
	}
	var ok2 struct {
		SessionID       string `json:"session_id"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	_ = json.Unmarshal(payload, &ok2)
	http.Redirect(w, r, "/success?s="+ok2.SessionID+"&t="+fmt.Sprint(ok2.DurationSeconds), http.StatusSeeOther)
}

func (h *handler) success(w http.ResponseWriter, r *http.Request) {
	// This page is looked up from scd for the connection's device, not read from the address, where ?t= and ?s= were editable and often missing.
	st, known := h.deviceStatus(r.Context(), clientIP(r))
	if known && !st.Active {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce, "tl.", "dev.", "unit.")
	v := successView{
		guestPage: p,
		Reminder:  h.onlineReminderFor(r, p.T),
	}
	if known {
		v.SessionID = st.SessionID
		if d := st.remaining(time.Now()); d != nil {
			v.DurationSeconds = int(d.Seconds())
			v.HumanRemaining = humanSpan(p.T, *d)
			if v.HumanRemaining == "" {
				v.HumanRemaining = p.T["tl.none"]
			}
		} else {
			v.Unlimited = true
		}
	}
	// No package panel here: every device reaching this page is online with its sign-in spent, so a purchase offer was wrong; buying more is a later decision.
	if sess, ok := h.resolveCommerceSession(r); ok {
		v.ReturnCode = sess.returnCode
	}
	_ = h.tmplSucc.Execute(w, v)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ip == nil {
		// Disconnect is a plain form post: a browser gets a page in words, anything else the old plain answer.
		if wantsHTML(r) {
			h.renderGuestNotice(w, r, 400, "online.disconnect", "err.device.detect", "/", "errpage.back")
			return
		}
		http.Error(w, "bad ip", 400)
		return
	}
	// Without this device's leave token (leave.go) nothing is ended: the client is asked.
	_ = r.ParseForm()
	if !validLeaveToken(ip, r.PostFormValue("leave")) {
		if wantsHTML(r) || r.Header.Get("Accept") == "" {
			h.renderLeaveConfirm(w, r)
			return
		}
		jsonErr(w, http.StatusForbidden, "sign-out must come from this device's portal page")
		return
	}
	// "logout", not "admin" (audit P-30): the guest signed themselves out.
	body, _ := json.Marshal(map[string]string{"ip": ip.String(), "reason": "logout"})
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "http://unix/v1/sessions/revoke", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := h.scd.Do(req); err == nil {
		resp.Body.Close()
	}
	// Signing out also forgets the device: the next visit asks for a code.
	if token := h.resumeToken(r); token != "" {
		rb, _ := json.Marshal(map[string]string{"resume_token": token})
		rq, _ := http.NewRequestWithContext(r.Context(), "POST", "http://unix/v1/sessions/resume-revoke", bytes.NewReader(rb))
		rq.Header.Set("Content-Type", "application/json")
		if resp, err := h.scd.Do(rq); err == nil {
			resp.Body.Close()
		}
		clearClientResumeCookie(w, r)
	}
	h.endPortalSignIn(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// endPortalSignIn forgets the commerce session (sc_commerce) and account token (og_account); otherwise, with the commerce session still valid, every visit would return to the signed-out account's packages and the sign-in page would be unreachable on that device.
func (h *handler) endPortalSignIn(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(commerceCookie); err == nil && c.Value != "" && h.commerceSessions != nil {
		h.commerceSessions.delete(c.Value)
	}
	// og_resume too: an open-selection client who signs out is forgotten like any other; the recovery code still brings them back.
	for _, name := range []string{commerceCookie, accountCookie, resumeCookie} {
		if _, err := r.Cookie(name); err == nil {
			clearPortalCookie(w, r, name)
		}
	}
}

// status answers a script (the online page's fetch) with scd's JSON and plain errors unchanged, and a browser navigating here with a branded page; the lookup is keyed on the connection's address for both.
func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	page := wantsHTML(r)
	ip := clientIP(r)
	if ip == nil {
		if page {
			h.renderGuestNotice(w, r, 400, "online.status", "err.device.detect", onlinePageHref(r, ""), "online.back")
			return
		}
		http.Error(w, "bad ip", 400)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://unix/v1/sessions/status?ip="+ip.String(), nil)
	resp, err := h.scd.Do(req)
	if err != nil {
		if page {
			h.renderGuestNotice(w, r, 500, "online.status", "err.service", onlinePageHref(r, ""), "online.back")
			return
		}
		http.Error(w, "scd unreachable", 500)
		return
	}
	defer resp.Body.Close()
	if !page {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, resp.Body)
		return
	}
	var st scdStatus
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &st) != nil {
		h.renderGuestNotice(w, r, http.StatusBadGateway, "online.status", "err.service", onlinePageHref(r, ""), "online.back")
		return
	}
	if !st.Active {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.renderStatus(w, r, st)
}

// Well-known captive-detect probes: unauthenticated devices get content that triggers the OS captive-portal UI.
func (h *handler) appleProbe(w http.ResponseWriter, r *http.Request) {
	// We deliberately do not serve the iOS/macOS "Success" page, so the captive UI appears.
	h.landing(w, r, "")
}
func (h *handler) googleProbe(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, h.probeTarget(r), http.StatusFound)
}
func (h *handler) windowsProbe(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, h.probeTarget(r), http.StatusFound)
}

// probeTarget is an address the device can certainly reach (fallbackOrigin); PORTALD_FQDN is last because its .local name resolves by mDNS on Apple devices.
func (h *handler) probeTarget(r *http.Request) string {
	if o := h.fallbackOrigin(r); o != "" {
		return o + "/"
	}
	return "http://" + h.cfg.PortalFQDN + "/"
}

func (h *handler) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// middleware.RealIP is deliberately absent: it rewrites RemoteAddr from X-Forwarded-For/X-Real-IP, but guests reach the portal directly through DNAT, so those headers are guest-controlled and would let a guest name another guest's IP and act on their devices.
	// If a reverse proxy is ever added, trust exactly that proxy's address and strip client-supplied forwarding headers at the edge instead of re-adding RealIP.
	// Query strings are never logged: tap-to-connect and email links carry single-use codes and tokens in the query. Handlers read r.URL, which is untouched.
	r.Use(redactLoggedQuery)
	r.Use(reqlog.Middleware)
	r.Use(middleware.Recoverer)
	// The timeout fits a whole acquisition (quote 5 s + confirm 5 s + activation 12 s); a request that still runs out gets a page in words, not a blank 504 (timeout.go).
	r.Use(h.portalTimeout(portalRequestTimeout))
	// Text responses are gzipped for clients that accept it; images pass through.
	r.Use(gzipResponses)

	r.Get("/", h.index)
	// Routes that check a credential or send a message pass the flood damper first (throttle.go).
	r.Post("/auth/voucher", h.damped(h.authVoucher))
	r.Post("/auth/credentials", h.damped(h.authCredentials))
	r.Post("/auth/account/register", h.damped(h.accountRegister))
	r.Post("/auth/account/signin", h.damped(h.accountSignIn))
	r.Post("/auth/account/forgot", h.damped(h.accountForgot))
	r.Post("/auth/account/resend", h.accountResend)
	r.Post("/auth/account/grace", h.accountGrace)
	r.Get("/account/verification", h.accountVerificationPage)
	r.Get("/account/verify", h.accountVerifyPage)
	r.Post("/account/verify", h.accountVerify)
	r.Get("/account/reset", h.accountResetPage)
	r.Post("/account/reset", h.accountReset)
	r.Post("/auth/resume", h.damped(h.authResume))
	r.Post("/auth/resume/forget", h.authResumeForget)
	r.Post("/auth/otp/request", h.damped(h.authOTPRequest))
	r.Post("/auth/otp/resend", h.damped(h.authOTPResend))
	r.Post("/auth/otp/forget", h.authOTPForget)
	r.Get("/connect", h.connectPage)
	r.Post("/auth/otp/link", h.damped(h.authOTPLink))
	r.Post("/auth/otp/verify", h.damped(h.authOTPVerify))
	r.Get("/auth/social/start", h.socialStart)
	r.Get("/auth/social/callback", h.socialCallback)
	r.Post("/auth/social/callback", h.socialCallback)
	// The development OAuth stub exists only in a -tags devlicense build; a production binary has no such route.
	mountStubOAuth(r, h)
	// Phase 3 is mounted unconditionally as a pure proxy: while dark scd answers with the same uniform non-success a wrong room gets, so a guest cannot tell whether the feature exists.
	r.Post("/auth/pms/phase3", h.authPMSPhase3)
	// Read-only; answers "nothing to say" for every device that has not just run out.
	r.Post("/access/status", h.accessStatus)
	// Phase 5 is mounted unconditionally for the same reason: the hop failing gives the same uniform non-success a wrong PIN gets.
	r.Post("/poststay/issue", h.postStayIssue)
	r.Post("/auth/post-stay-pin", h.postStayAuth)

	// Phase 6 likewise: whether device management exists is decided twice behind this layer (deployment gate, per-appliance setting), and the guest gets the same uniform answer either way.
	r.Post("/devices/list", h.deviceList)
	r.Post("/devices/release", h.deviceRelease)
	r.Get("/devices/replace", h.replacePage)
	r.Post("/devices/replace", h.replaceDevice)
	r.Get("/api/auth-methods", h.authMethods)
	r.Get("/api/branding", h.branding)
	// The shipped wording is unauthenticated like the rest of /api here: it is the same text already in every served page.
	r.Get("/api/languages", h.languages)

	// The hotel's images are served from the appliance because a captive device has no internet; read-only, from a directory only Hotel Admin writes, with the content type decided at upload from the bytes, never from an extension.
	r.Handle("/assets/*", http.StripPrefix("/assets/",
		http.FileServer(neuteredFS{http.Dir(portalAssetDir)})))

	// Commerce bridge routes are mounted only when the portal surface is ON; otherwise 404 and no scd commerce call.
	if h.commerceCfg.PortalOn() {
		r.Get("/api/commerce/packages", h.commercePackages)
		r.Post("/api/commerce/quote", h.commerceQuote)
		r.Post("/api/commerce/confirm", h.commerceConfirm)
	}

	r.Get("/packages", h.packagesPage)
	r.Post("/packages/acquire", h.acquirePackage)
	r.Post("/auth/open", h.damped(h.authOpen))
	r.Get("/pay/return", h.payReturn)
	r.Get("/api/pay/status", h.payStatusAPI)
	r.Get("/success", h.success)
	r.Post("/logout", h.logout)
	r.Get("/status", h.status)

	r.Get("/hotspot-detect.html", h.appleProbe)
	r.Get("/library/test/success.html", h.appleProbe)
	r.Get("/generate_204", h.googleProbe)
	r.Get("/gen_204", h.googleProbe)
	r.Get("/connecttest.txt", h.windowsProbe)
	r.Get("/ncsi.txt", h.windowsProbe)
	// RFC 8908 captive portal API: TLS only, 404 over HTTP.
	r.Get("/captive-portal/api", h.captivePortalAPI)

	r.NotFound(func(w http.ResponseWriter, req *http.Request) { h.index(w, req) })
	return r
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	startupbackoff.Guard("portald")

	c := loadCfg()
	h, err := newHandler(c)
	if err != nil {
		slog.Error("handler init", "err", err)
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := h.routes()

	go h.pollTLSState(rootCtx)

	httpSrv := &http.Server{Addr: c.HTTPAddr, Handler: h.httpFront(mux), ReadHeaderTimeout: 5 * time.Second}
	httpsSrv := &http.Server{
		Addr: c.HTTPSAddr, Handler: h.tlsFront(mux), ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: portalTLSConfig(newPortalCertServer(h.tlsState)), ErrorLog: httpsErrorLog(),
	}

	go func() {
		slog.Info("portald HTTP", "addr", c.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http serve", "err", err)
		}
	}()
	go func() {
		// Always listening: until scd installs the first certificate handshakes fail harmlessly, and a later certificate is picked up by SNI without a restart.
		slog.Info("portald HTTPS", "addr", c.HTTPSAddr)
		if err := httpsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("https serve", "err", err)
		}
	}()

	<-rootCtx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = httpsSrv.Shutdown(shutCtx)
}

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// redactLoggedQuery strips the query from r.RequestURI so a code in a link never reaches the journal.
func redactLoggedQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if i := strings.IndexByte(r.RequestURI, '?'); i >= 0 {
			r.RequestURI = r.RequestURI[:i] + "?[redacted]"
		}
		next.ServeHTTP(w, r)
	})
}
