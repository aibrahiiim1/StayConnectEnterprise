package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// ---- /auth/social/start?provider=google -------------------------------------

func (h *handler) socialStart(w http.ResponseWriter, r *http.Request) {
	// The sign-in page reaches this route with a LINK, so every refusal below is a page the guest's browser
	// lands on. A browser gets the branded, translated failure page with the same status code; any other
	// client keeps the JSON it always had.
	fail := func(status int, key, msg string) {
		if wantsHTML(r) {
			h.renderGuestError(w, r, status, key)
			return
		}
		jsonErr(w, status, msg)
	}
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		fail(400, "errpage.social", "provider required")
		return
	}
	// Google and Facebook refuse sign-in inside an Android WebView (the captive login window). Starting an
	// attempt there only lands the guest on the provider's refusal; send them to their own browser instead,
	// before any attempt exists (social_browser.go).
	if refusesEmbeddedBrowser(provider) && isAndroidWebView(r.UserAgent()) {
		h.renderOpenInBrowser(w, r, provider)
		return
	}
	ip := clientIP(r)
	if ip == nil {
		fail(400, "err.device.detect", "bad ip")
		return
	}
	// The hardware address comes from the kernel's neighbour table, never from the client, and the kernel is
	// ASKED to resolve it rather than only consulted (arp_resolve.go): a cold cache is not evidence that a
	// device is off the guest network. Same mechanism, same guarantee, on every sign-in path.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		fail(400, "err.device.network", "device not on this Wi-Fi network")
		return
	}

	// The portal's own view of its callback URL. scd does NOT send this to a real provider: the redirect URI
	// of an attempt is always https://<canonical>/auth/social/callback, decided by scd from the portal's HTTPS
	// state (a Host header is the client's word). It is read only for the development stub over plain HTTP.
	scheme := "http"
	if r.TLS != nil || forwardedHTTPS(r) {
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
		fail(502, "err.service", "service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if wantsHTML(r) {
			// scd's reason ("provider not enabled") is for the log, not for the guest.
			slog.Info("social sign-in start refused", "provider", provider, "status", resp.StatusCode)
			h.renderGuestError(w, r, resp.StatusCode, "errpage.social")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	var sr struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil || sr.AuthorizeURL == "" {
		fail(502, "err.service", "bad scd response")
		return
	}
	http.Redirect(w, r, sr.AuthorizeURL, http.StatusFound)
}

// ---- /auth/social/callback?state=...&code=... (GET) or the same as a POST form ---
//
// Google, Microsoft and Facebook return code + state in the query of a GET. Sign in with Apple POSTs them as
// a form (response_mode=form_post), which Apple mandates as soon as the email scope is requested, so the
// route accepts both and FormValue reads either. A real provider sends no `provider` parameter -- the
// redirect_uri registered with it is this bare path -- so scd resolves the provider from the state row; the
// Stub still round-trips one, and scd checks it against the state when present. The POST is a cross-site
// form submission by design; it carries no more authority than the GET, because the state it names is bound
// to the device that started the flow and is consumed once.

func (h *handler) socialCallback(w http.ResponseWriter, r *http.Request) {
	provider, state, code := r.FormValue("provider"), r.FormValue("state"), r.FormValue("code")
	// Every refusal on this return leg is a page the guest's browser lands on, so each is the branded,
	// translated failure page. The status codes are the ones this handler has always sent.
	if state == "" || code == "" {
		h.renderGuestError(w, r, http.StatusBadRequest, "errpage.social")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.renderGuestError(w, r, http.StatusBadRequest, "err.device.detect")
		return
	}
	// The hardware address comes from the kernel's neighbour table, never from the client, and the kernel is
	// ASKED to resolve it rather than only consulted (arp_resolve.go): a cold cache is not evidence that a
	// device is off the guest network. Same mechanism, same guarantee, on every sign-in path.
	mac, ok := h.deviceMAC(r.Context(), ip)
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
	// The provider verified who this is; the Client still has to join their live access or choose a package.
	// The legacy session_id shape this used to read is gone: every IAM-v2 reply goes through the same path
	// as the voucher and account forms.
	if h.tryIAMv2Auth(w, r, payload) {
		return
	}
	h.renderGuestError(w, r, http.StatusBadGateway, "err.service")
}

// forwardedHTTPS reports whether the request reached the portal over HTTPS through the appliance's own reverse
// proxy (Caddy terminates TLS for portal.stayconnect.local and forwards over loopback). The header is believed
// only from a loopback peer: a guest cannot set it on a direct request.
func forwardedHTTPS(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
