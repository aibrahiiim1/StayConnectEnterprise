package main

// Remembered device ("Welcome back"): scd issues a device-bound resume credential after a verified OTP or IdP sign-in; the portal keeps it only in the HttpOnly cookie og_client and asks scd whether it is good for this device (IP from the connection, MAC from the kernel, never the browser).
// HTTPS only: over the HTTP fallback a long-lived credential would cross the guest network in clear text, so the device is not remembered; sign-out and "Not you?" still revoke one an older portal stored.

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
	// clientResumeDefaultDays is the default Remember-devices setting, used when scd does not say which applied; the cookie lives as long as the credential.
	clientResumeDefaultDays = 30
	clientResumeMaxDays     = 365
)

type welcomeView struct {
	Label  string
	Method string
}

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

// rememberCookie is the guest's own "Remember this device" choice; a session cookie carrying no identifier.
const rememberCookie = "og_remember"

// rememberMethods are the sign-ins the box is offered for; without this a tick given on a code sign-in carried over to methods that never offered it.
var rememberMethods = map[string]bool{"OTP": true, "SOCIAL": true}

// guestAskedToRemember: the sign-in is one the box is offered for, the guest ticked it, and the request is over HTTPS.
func guestAskedToRemember(r *http.Request, method string) bool {
	if r == nil || r.TLS == nil || !rememberMethods[method] {
		return false
	}
	if r.Method == http.MethodPost && r.PostFormValue("remember") == "1" {
		return true
	}
	c, err := r.Cookie(rememberCookie)
	return err == nil && c.Value == "1"
}

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

func (h *handler) rememberedToken(r *http.Request) string {
	if r.TLS == nil {
		return ""
	}
	return h.resumeToken(r)
}

// resumeToken reads the cookie on any transport, for revoking.
func (h *handler) resumeToken(r *http.Request) string {
	c, err := r.Cookie(clientResumeCookie)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return ""
	}
	return c.Value
}

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

// welcomeFor reuses the MAC the landing page already read; a cold cache means no greeting, and Connect resolves the device properly before anything is decided.
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
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return
	}
	status, payload := h.resumeCall(r.Context(), "/v1/sessions/authorize-resume", ip, mac.String(), token)
	if status == http.StatusOK && h.tryIAMv2Auth(w, r, payload) {
		return
	}
	// A refusal that says nothing about the credential keeps it: rate limits, scd down or server errors are about this moment, and clearing the cookie would discard a good credential.
	switch {
	case status == http.StatusTooManyRequests:
		h.landing(w, r, msgTooManyAttemptsMoment)
		return
	case status == 0 || status >= 500:
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	// Otherwise the credential is gone or refused: clear the cookie so the greeting does not return.
	clearClientResumeCookie(w, r)
	h.landing(w, r, msgPleaseSignIn)
}

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
