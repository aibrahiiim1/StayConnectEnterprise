package main

// THE REMEMBERED DEVICE ON THE PORTAL ("Welcome back"): contract §2.3.
//
// scd issues a device-bound resume credential after a verified OTP / identity-provider sign-in; the portal
// keeps it in the HttpOnly cookie og_client and nowhere else. On the landing page the portal asks scd whether
// the credential is still good FOR THIS DEVICE (IP from the connection, MAC from the kernel neighbour table --
// never from the browser) and, if so, draws one Connect button. Connect mints an ordinary auth context and the
// existing journey continues: join a live entitlement, or choose a package.
//
// The cookie is not Secure for the same reason the commerce cookie is not: a captive portal is reached over
// plain HTTP before the device has any access. The credential is bound server-side to the device and carries
// 256 bits; its value to anyone else is nil.

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
	clientResumeMaxAge = 365 * 24 * time.Hour // the server-side expiry (the site's setting) governs; this only bounds the jar
)

type welcomeView struct {
	Label  string // masked identity: a•••@example.com
	Method string // OTP | SOCIAL
}

func setClientResumeCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: clientResumeCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(clientResumeMaxAge)})
}

func clearClientResumeCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: clientResumeCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

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
	token := h.resumeToken(r)
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
	token := h.resumeToken(r)
	if token == "" {
		h.landing(w, r, "Please sign in.")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "We couldn't detect your device. Please reconnect to the Wi-Fi and try again.")
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
	// Anything else: the credential is gone (expired, revoked, another device, method switched off) or the
	// authority refused. The cookie is cleared so the greeting does not return, and the ordinary page is shown.
	clearClientResumeCookie(w)
	if status == http.StatusTooManyRequests {
		h.landing(w, r, "Too many attempts. Please wait a moment and try again.")
		return
	}
	h.landing(w, r, "Please sign in.")
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
	clearClientResumeCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
