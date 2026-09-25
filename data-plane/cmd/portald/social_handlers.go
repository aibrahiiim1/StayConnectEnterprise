package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

// ---- /auth/social/start?provider=google -------------------------------------

func (h *handler) socialStart(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		jsonErr(w, 400, "provider required")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		jsonErr(w, 400, "bad ip")
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		jsonErr(w, 400, "device not on guest network")
		return
	}

	// Build the redirect_uri the provider should send the browser back to.
	// Always over the portal's external host so the browser can reach it
	// without DNS surgery on the guest network.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host // includes :port
	redirectURI := fmt.Sprintf("%s://%s/auth/social/callback", scheme, host)

	body, _ := json.Marshal(map[string]string{
		"provider":     provider,
		"ip":           ipString(ip),
		"mac":          mac.String(),
		"redirect_uri": redirectURI,
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/auth/social/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd social start", "err", err)
		jsonErr(w, 502, "service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	var sr struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil || sr.AuthorizeURL == "" {
		jsonErr(w, 502, "bad scd response")
		return
	}
	http.Redirect(w, r, sr.AuthorizeURL, http.StatusFound)
}

// ---- /auth/social/callback?provider=google&state=...&code=... ---------------

func (h *handler) socialCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider, state, code := q.Get("provider"), q.Get("state"), q.Get("code")
	// Every refusal on this return leg is a page the guest's browser lands on, so each is the branded,
	// translated failure page. The status codes are the ones this handler has always sent.
	if provider == "" || state == "" || code == "" {
		h.renderGuestError(w, r, http.StatusBadRequest, "errpage.social")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.renderGuestError(w, r, http.StatusBadRequest, "err.device.detect")
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		h.renderGuestError(w, r, http.StatusBadRequest, "err.device.network")
		return
	}

	body, _ := json.Marshal(map[string]string{
		"provider": provider,
		"state":    state,
		"code":     code,
		"ip":       ipString(ip),
		"mac":      mac.String(),
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/sessions/authorize-social", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd authorize-social", "err", err)
		h.renderGuestError(w, r, http.StatusBadGateway, "err.service")
		return
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// A page the captive browser can show: branded, in the guest's language, and in words -- scd's reason
		// ("state expired", "provider mismatch") is for the log, not for the guest.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &e)
		slog.Info("social sign-in refused", "provider", provider, "status", resp.StatusCode, "reason", e.Error)
		h.renderGuestError(w, r, resp.StatusCode, "errpage.social")
		return
	}
	var ok2 struct {
		SessionID       string `json:"session_id"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	_ = json.Unmarshal(payload, &ok2)
	http.Redirect(w, r,
		fmt.Sprintf("/success?s=%s&t=%d", ok2.SessionID, ok2.DurationSeconds),
		http.StatusFound)
}

// ---- /api/oauth/stub/authorize — fake provider consent screen --------------
// Lives in portald (browser-reachable). For real OAuth this is hosted by Google.
// The page simply lets the test pick an email + email_verified flag and
// round-trips back to the redirect_uri.

func (h *handler) stubAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider := q.Get("provider")
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")
	if provider == "" || state == "" || redirectURI == "" {
		http.Error(w, "missing provider/state/redirect_uri", http.StatusBadRequest)
		return
	}

	// Auto-mode: if email is present in the query, skip the consent UI and
	// redirect immediately (used by E2E tests).
	if email := q.Get("email"); email != "" && q.Get("auto") == "1" {
		verified := q.Get("email_verified") != "false"
		code := social.EncodeStubCode(social.UserInfo{
			Sub:           "stub:" + email,
			Email:         email,
			EmailVerified: verified,
			Name:          "Stub User",
		})
		dst := redirectURI + "?provider=" + url.QueryEscape(provider) +
			"&state=" + url.QueryEscape(state) +
			"&code=" + url.QueryEscape(code)
		http.Redirect(w, r, dst, http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Template uses provider twice: page heading + hidden form field.
	fmt.Fprintf(w, stubConsentHTML, htmlEscape(provider), htmlEscape(provider),
		htmlEscape(state), htmlEscape(redirectURI))
}

// ---- helpers ----------------------------------------------------------------

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

const stubConsentHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Stub Provider</title>
<style>body{font-family:system-ui;max-width:420px;margin:8vh auto;padding:24px}
input,button{font-size:1rem;padding:10px 12px;width:100%%;box-sizing:border-box;margin-top:8px;border:1px solid #ccc;border-radius:6px}
button{background:#0a6cff;color:#fff;border:0;font-weight:600;cursor:pointer}
.small{font-size:.85rem;color:#777;margin:8px 0}
</style></head>
<body>
<h2>Stub OAuth — %s</h2>
<p class="small">Choose the identity to return to the application.</p>
<form method="POST" action="/api/oauth/stub/authorize-confirm">
  <input type="hidden" name="provider" value="%s">
  <input type="hidden" name="state" value="%s">
  <input type="hidden" name="redirect_uri" value="%s">
  <label>Email</label>
  <input name="email" type="email" required value="alice@example.com">
  <label class="small"><input type="checkbox" name="email_verified" checked> email_verified</label>
  <button type="submit">Continue</button>
</form>
</body></html>`

func (h *handler) stubAuthorizeConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	provider := r.FormValue("provider")
	state := r.FormValue("state")
	redirectURI := r.FormValue("redirect_uri")
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	verified := r.FormValue("email_verified") == "on"
	if provider == "" || state == "" || redirectURI == "" || email == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	code := social.EncodeStubCode(social.UserInfo{
		Sub:           "stub:" + email,
		Email:         email,
		EmailVerified: verified,
		Name:          "Stub User",
	})
	dst := redirectURI + "?provider=" + url.QueryEscape(provider) +
		"&state=" + url.QueryEscape(state) +
		"&code=" + url.QueryEscape(code)
	http.Redirect(w, r, dst, http.StatusFound)
}
