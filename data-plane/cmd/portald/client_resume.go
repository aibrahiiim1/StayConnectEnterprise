package main

// THE REMEMBERED DEVICE ON THE PORTAL ("Welcome back"): contract §2.3.
//
// scd issues a device-bound resume credential after a verified OTP / identity-provider sign-in; the portal
// keeps it in the HttpOnly cookie og_client and nowhere else. On the landing page the portal asks scd whether
// the credential is still good FOR THIS DEVICE (IP from the connection, MAC from the kernel neighbour table --
// never from the browser) and, if so, draws one Connect button. Connect mints an ordinary auth context and the
// existing journey continues: join a live entitlement, or choose a package.
//
// HTTPS ONLY (GDPR audit B10). The cookie is set, and honoured, only over the portal's own HTTPS listener, where
// it is Secure (cookies.go). On the plain-HTTP fallback a long-lived credential would cross the guest network in
// clear text, so there the device is simply not remembered and the guest signs in as usual. Signing out and
// "Not you?" still revoke a credential an older portal stored over HTTP. The credential is bound server-side to
// the device and carries 256 bits.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	clientResumeCookie = "og_client"
	// clientResumeDefaultDays is the contract's default Remember-devices setting (Identity policy §2.3), used when
	// scd's reply does not say which setting the credential was issued under. The cookie lives as long as the
	// credential: it used to be kept for a year whatever the site set (audit P-17).
	clientResumeDefaultDays = 30
	clientResumeMaxDays     = 365
)

type welcomeView struct {
	Label  string // masked identity: a•••@example.com
	Method string // OTP | SOCIAL
}

// setClientResumeCookie keeps the remembered-device credential for `days` (the site's setting at issue).
func setClientResumeCookie(w http.ResponseWriter, r *http.Request, token string, days int) {
	if r == nil || r.TLS == nil {
		return
	}
	if days <= 0 {
		days = clientResumeDefaultDays
	}
	if days > clientResumeMaxDays {
		days = clientResumeMaxDays
	}
	setPortalCookie(w, r, clientResumeCookie, token, time.Duration(days)*24*time.Hour)
}

func clearClientResumeCookie(w http.ResponseWriter, r *http.Request) {
	clearPortalCookie(w, r, clientResumeCookie)
}

// rememberCookie is the guest's own choice, "Remember this device" ticked on the sign-in page (GDPR audit B10,
// D-B10). A session cookie the page sets; it carries no identifier.
const rememberCookie = "og_remember"

// guestAskedToRemember reports whether this sign-in may keep a remembered-device credential: the guest ticked
// the box -- on the sign-in page (the cookie) or on the message-link page (the form field) -- and the request
// is over HTTPS.
func guestAskedToRemember(r *http.Request) bool {
	if r == nil || r.TLS == nil {
		return false
	}
	if r.Method == http.MethodPost && r.PostFormValue("remember") == "1" {
		return true
	}
	c, err := r.Cookie(rememberCookie)
	return err == nil && c.Value == "1"
}

// revokeResume asks scd to revoke a resume credential, best effort.
func (h *handler) revokeResume(ctx context.Context, token string) {
	if h.scd == nil {
		return
	}
	body, _ := json.Marshal(map[string]string{"resume_token": token})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/sessions/resume-revoke", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := h.scd.Do(req); err == nil {
		resp.Body.Close()
	} else {
		slog.Warn("scd resume revoke", "err", err)
	}
}

// rememberedToken is the credential the Welcome-back line and Connect may use: over HTTPS only.
func (h *handler) rememberedToken(r *http.Request) string {
	if r.TLS == nil {
		return ""
	}
	return h.resumeToken(r)
}

// resumeToken reads the cookie whatever the transport, for revoking it.
func (h *handler) resumeToken(r *http.Request) string {
	c, err := r.Cookie(clientResumeCookie)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return ""
	}
	return c.Value
}

// resumeCall posts the device-bound credential to one of scd's resume routes.
func (h *handler) resumeCall(ctx context.Context, path string, ip net.IP, mac string, token string) (int, []byte) {
	body, _ := json.Marshal(map[string]string{"ip": ipString(ip), "mac": mac, "resume_token": token})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Warn("scd resume call", "path", path, "err", err)
		return 0, nil
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, payload
}

// welcomeFor answers whether the landing page should greet this device as a remembered Client. It takes the
// MAC the landing page already read from the neighbour cache (one read, documented there): a cold cache simply
// means no greeting, and the normal sign-in page is the correct fallback. Nothing is minted here; Connect
// resolves the device properly before anything is decided.
func (h *handler) welcomeFor(r *http.Request, mac string) *welcomeView {
	token := h.rememberedToken(r)
	if token == "" || mac == "" {
		return nil
	}
	ip := clientIP(r)
	if ip == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, payload := h.resumeCall(ctx, "/v1/sessions/resume-peek", ip, mac, token)
	if status != http.StatusOK {
		return nil
	}
	var out struct {
		OK     bool   `json:"ok"`
		Label  string `json:"identity_label"`
		Method string `json:"method"`
	}
	if json.Unmarshal(payload, &out) != nil || !out.OK {
		return nil
	}
	return &welcomeView{Label: out.Label, Method: out.Method}
}

// POST /auth/resume -- "Connect" on the Welcome-back line.
func (h *handler) authResume(w http.ResponseWriter, r *http.Request) {
	token := h.rememberedToken(r)
	if token == "" {
		h.landing(w, r, msgPleaseSignIn)
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, msgCannotDetectDevice)
		return
	}
	// Resolving lookup, as on every sign-in path: a cold cache is not evidence the device left the network.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return
	}
	status, payload := h.resumeCall(r.Context(), "/v1/sessions/authorize-resume", ip, mac.String(), token)
	if status == http.StatusOK && h.tryIAMv2Auth(w, r, payload) {
		return
	}
	// A REFUSAL THAT SAYS NOTHING ABOUT THE CREDENTIAL KEEPS IT (audit P-9). Too many attempts, scd unreachable
	// (status 0) or a server error are about this moment, not about the remembered device: clearing the cookie
	// then threw away a credential that was still good, and the next visit asked for a code again.
	switch {
	case status == http.StatusTooManyRequests:
		h.landing(w, r, msgTooManyAttemptsMoment)
		return
	case status == 0 || status >= 500:
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	// Anything else: the credential is gone (expired, revoked, another device, method switched off) or the
	// authority refused it. The cookie is cleared so the greeting does not return, and the ordinary page is shown.
	clearClientResumeCookie(w, r)
	h.landing(w, r, msgPleaseSignIn)
}

// POST /auth/resume/forget -- "Not you?": revoke the credential and forget the device.
func (h *handler) authResumeForget(w http.ResponseWriter, r *http.Request) {
	if token := h.resumeToken(r); token != "" {
		body, _ := json.Marshal(map[string]string{"resume_token": token})
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://unix/v1/sessions/resume-revoke", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if resp, err := h.scd.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	clearClientResumeCookie(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
