package main

// A ONE-TIME CODE SURVIVES LEAVING THE CAPTIVE BROWSER.
//
// The code-entry step used to live only in the landing page's script. On a phone the captive mini-browser is
// dismissed the moment the client opens Mail to read the code, and the page it reopens starts from nothing:
// no challenge, no "enter the code" box, a second code to request (into a 60-second cooldown), and the same
// loop again. Found by the Product Owner on PRE-LIVE with Gmail over SMTP.
//
// Two remedies, both server-side:
//
//  1. THE PENDING CHALLENGE IS HELD BY THE PORTAL FOR THE DEVICE, keyed by the device's address, for the code's
//     own lifetime. The landing page asks for it with the sign-in methods and opens straight on the code box
//     ("We sent a code to a•••@example.com"), with Send again and Use a different address. Nothing here decides
//     anything: verification still needs the challenge AND the code, from a device the kernel can place.
//  2. THE EMAIL CARRIES A TAP-TO-CONNECT LINK into the phone's real browser, which the client can reach before
//     sign-in (the portal is in the pre-auth allow set). The link opens a page with ONE button; the button
//     POSTs the verification. The link is never verified on GET, so a mail scanner or a prefetcher that
//     follows it spends nothing. The code in the link is the same secret the email already carries.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type pendingOTP struct {
	Channel     string    `json:"channel"`
	ChallengeID string    `json:"challenge_id"`
	DestMasked  string    `json:"dest_masked"`
	dest        string    // the address, for Send again; never rendered
	ExpiresAt   time.Time `json:"-"`
}

// pendingOTPStore keys a device's in-flight challenge by its address. Addresses are leased by the appliance's
// own DHCP, so for the ten minutes a code lives the key is stable even as the captive browser comes and goes.
type pendingOTPStore struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]pendingOTP
}

func newPendingOTPStore() *pendingOTPStore {
	return &pendingOTPStore{now: time.Now, m: map[string]pendingOTP{}}
}

func (s *pendingOTPStore) put(key string, p pendingOTP) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = p
	if len(s.m) > 20000 {
		for k, v := range s.m {
			if !v.ExpiresAt.After(s.now()) {
				delete(s.m, k)
			}
		}
	}
}

func (s *pendingOTPStore) get(key string) (pendingOTP, bool) {
	if key == "" {
		return pendingOTP{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[key]
	if !ok || !p.ExpiresAt.After(s.now()) {
		delete(s.m, key)
		return pendingOTP{}, false
	}
	return p, true
}

func (s *pendingOTPStore) clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

// pendingKey is the device address as the connection reports it (never a header).
func pendingKey(r *http.Request) string {
	ip := clientIP(r)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// maskDestination renders an address for the page without disclosing it: a•••@example.com, +20•••1234.
func maskDestination(channel, dest string) string {
	dest = strings.TrimSpace(dest)
	if channel == "email" {
		at := strings.IndexByte(dest, '@')
		if at <= 0 {
			return "•••"
		}
		return strings.ToLower(dest[:1]) + "•••" + strings.ToLower(dest[at:])
	}
	if len(dest) <= 4 {
		return "•••"
	}
	return dest[:3] + "•••" + dest[len(dest)-4:]
}

// POST /auth/otp/forget -- "Use a different address": the device's pending challenge is dropped. The
// challenge itself still expires on its own clock in scd; dropping it here only stops the page reopening it.
func (h *handler) authOTPForget(w http.ResponseWriter, r *http.Request) {
	if h.pendingOTP != nil {
		h.pendingOTP.clear(pendingKey(r))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// POST /auth/otp/resend -- "Send again" with the address the device gave earlier (held server-side, never
// echoed). It is the ordinary issue call; scd's cooldown and caps apply unchanged.
func (h *handler) authOTPResend(w http.ResponseWriter, r *http.Request) {
	if h.pendingOTP == nil {
		jsonErr(w, 404, "challenge not found")
		return
	}
	p, ok := h.pendingOTP.get(pendingKey(r))
	if !ok {
		jsonErr(w, 404, "challenge not found")
		return
	}
	body, _ := json.Marshal(otpReqIn{Channel: p.Channel, Destination: p.dest})
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	h.authOTPRequest(w, r2)
}

// ---- the tap-to-connect link ------------------------------------------------------------------------------

var reChallengeID = regexp.MustCompile(`^[0-9a-fA-F-]{20,64}$`)
var reCode = regexp.MustCompile(`^[0-9]{4,10}$`)

type connectView struct {
	guestPage
	ChallengeID, Code string
}

// GET /connect?c=<challenge>&k=<code> -- the page the email's link opens. It verifies NOTHING: it shows one
// Connect button whose POST does. A link without both parts, or a malformed one, is an ordinary refusal page.
func (h *handler) connectPage(w http.ResponseWriter, r *http.Request) {
	c, k := strings.TrimSpace(r.URL.Query().Get("c")), strings.TrimSpace(r.URL.Query().Get("k"))
	if !reChallengeID.MatchString(c) || !reCode.MatchString(k) {
		h.renderGuestNotice(w, r, http.StatusBadRequest, "link.title", "err.otp.expired", "/", "errpage.back")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	pg := h.newGuestPage(r, nonce, "link.", "welcome.connect")
	_ = connectTmpl.Execute(w, connectView{guestPage: pg, ChallengeID: c, Code: k})
}

// POST /auth/otp/link -- the Connect button: the same verification as the code box, from the browser the
// link opened, with the device resolved from the connection; then the same journey as every sign-in.
func (h *handler) authOTPLink(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Please sign in.")
		return
	}
	c, k := strings.TrimSpace(r.PostFormValue("c")), strings.TrimSpace(r.PostFormValue("k"))
	if !reChallengeID.MatchString(c) || !reCode.MatchString(k) {
		h.renderGuestNotice(w, r, http.StatusBadRequest, "link.title", "err.otp.expired", "/", "errpage.back")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "We couldn't detect your device. Please reconnect to the Wi-Fi and try again.")
		return
	}
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return
	}
	body, _ := json.Marshal(map[string]string{"ip": ipString(ip), "mac": mac.String(), "challenge_id": c, "code": k})
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "http://unix/v1/sessions/authorize-otp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd authorize-otp (link)", "err", err)
		h.renderGuestNotice(w, r, http.StatusBadGateway, "link.title", "err.service", "/", "errpage.back")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusOK {
		if h.pendingOTP != nil {
			h.pendingOTP.clear(pendingKey(r))
		}
		if h.tryIAMv2Auth(w, r, payload) {
			return
		}
	}
	// A spent, expired or wrong code: say so in words, and offer the sign-in page (which may still hold the
	// pending box for a fresh code).
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(payload, &e)
	key := "err.otp.expired"
	switch strings.ToLower(e.Error) {
	case "incorrect code":
		key = "err.otp.code"
	case "too_many_attempts":
		key = "err.attempts"
	}
	h.renderGuestNotice(w, r, http.StatusForbidden, "link.title", key, "/", "errpage.back")
}

// portalLinkBase is the origin the email's link should use: the address the client reached the portal by,
// which is the one its browser can reach before sign-in. Anything that does not parse as a plain http(s)
// origin yields "", and the email then carries the code alone.
func portalLinkBase(r *http.Request) string {
	o := portalOrigin(r)
	if o == "" {
		return ""
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || u.Path != "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(u.Host); err == nil && host == "" {
		return ""
	}
	return o
}

const connectHTML = guestHead + `
<title>{{index .T "link.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <h1 class="page-title">{{index .T "link.title"}}</h1>
  <p class="page-lead">{{index .T "link.lead"}}</p>
  <form method="post" action="/auth/otp/link" id="form-link">
    <input type="hidden" name="c" value="{{.ChallengeID}}">
    <input type="hidden" name="k" value="{{.Code}}">
    <button class="primary" type="submit">{{index .T "welcome.connect"}}</button>
  </form>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`

var connectTmpl = template.Must(template.New("connect").Parse(compactMarkup(connectHTML)))
