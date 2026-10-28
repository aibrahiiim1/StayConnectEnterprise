package main

// A one-time code survives the captive mini-browser being dismissed: the portal holds the pending challenge
// per device so the landing page reopens on the code box, and the message can carry a tap-to-connect link.
// The link is never verified on GET (a scanner or prefetcher must spend nothing); its one button POSTs.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
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

// pendingKeyFor keys by address and hardware address: with the address alone, a reassigned IP would open on
// the previous device's code box and resend to its number. The MAC comes from the kernel neighbour table.
func pendingKeyFor(ip net.IP, mac net.HardwareAddr) string {
	if ip == nil || len(mac) == 0 {
		return ""
	}
	return ip.String() + "|" + mac.String()
}

// "" when the device cannot be placed: no pending code is offered then.
func (h *handler) pendingKey(r *http.Request) string {
	ip := clientIP(r)
	if ip == nil {
		return ""
	}
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		return ""
	}
	return pendingKeyFor(ip, mac)
}

// maskDestination: +20•••1234.
func maskDestination(channel, dest string) string {
	dest = strings.TrimSpace(dest)
	if len(dest) <= 4 {
		return "•••"
	}
	return dest[:3] + "•••" + dest[len(dest)-4:]
}

// POST /auth/otp/forget drops the pending challenge here; scd still expires it on its own clock.
func (h *handler) authOTPForget(w http.ResponseWriter, r *http.Request) {
	if h.pendingOTP != nil {
		h.pendingOTP.clear(h.pendingKey(r))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// POST /auth/otp/resend reuses the held address (never echoed); scd's cooldown and caps apply.
func (h *handler) authOTPResend(w http.ResponseWriter, r *http.Request) {
	if h.pendingOTP == nil {
		jsonErr(w, 404, "challenge not found")
		return
	}
	p, ok := h.pendingOTP.get(h.pendingKey(r))
	if !ok {
		jsonErr(w, 404, "challenge not found")
		return
	}
	body, _ := json.Marshal(otpReqIn{Channel: p.Channel, Destination: p.dest})
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	// The caller's Origin header is kept so the origin rule still applies.
	r2.Header.Set("Content-Type", "application/json")
	h.authOTPRequest(w, r2)
}

var reChallengeID = regexp.MustCompile(`^[0-9a-fA-F-]{20,64}$`)
var reCode = regexp.MustCompile(`^[0-9]{4,10}$`)

type connectView struct {
	guestPage
	ChallengeID, Code string
	// Secure: served over HTTPS, so "Remember this device" can be offered.
	Secure bool
}

// GET /connect verifies nothing; only the button's POST does.
func (h *handler) connectPage(w http.ResponseWriter, r *http.Request) {
	c, k := strings.TrimSpace(r.URL.Query().Get("c")), strings.TrimSpace(r.URL.Query().Get("k"))
	if !reChallengeID.MatchString(c) || !reCode.MatchString(k) {
		h.renderGuestNotice(w, r, http.StatusBadRequest, "link.title", "err.otp.expired", "/", "errpage.back")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The URL carries the code: no Referer, indexing or caching. The code is device-bound, so a leaked link
	// is refused elsewhere anyway.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	nonce := setPortalCSP(w)
	pg := h.newGuestPage(r, nonce, "link.", "welcome.connect")
	_ = connectTmpl.Execute(w, connectView{guestPage: pg, ChallengeID: c, Code: k, Secure: r.TLS != nil})
}

// POST /auth/otp/link is the same verification as the code box, with the device resolved from the connection.
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
			h.pendingOTP.clear(h.pendingKey(r))
		}
		if h.tryIAMv2Auth(w, r, payload) {
			return
		}
	}
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(payload, &e)
	key := "err.otp.expired"
	if e.Reason == "assisted_existing_client" {
		e.Error = "assisted_existing_client"
	}
	switch strings.ToLower(e.Error) {
	case "assisted_existing_client":
		// A code staff showed cannot sign in an address that already belongs to a Client.
		key = "err.otp.assisted"
	case "incorrect code":
		key = "err.otp.code"
	case "too_many_attempts":
		key = "err.attempts"
	case "other_device":
		key = "err.otp.device"
	}
	h.renderGuestNotice(w, r, http.StatusForbidden, "link.title", key, "/", "errpage.back")
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
    {{if .Secure}}<label class="remember-device"><input type="checkbox" name="remember" value="1"> <span>{{index .T "remember.device"}}</span></label>{{end}}
    <button class="primary" type="submit">{{index .T "welcome.connect"}}</button>
  </form>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`

var connectTmpl = template.Must(template.New("connect").Parse(compactMarkup(connectHTML)))
