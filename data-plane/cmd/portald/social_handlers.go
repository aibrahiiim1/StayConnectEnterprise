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

func (h *handler) socialStart(w http.ResponseWriter, r *http.Request) {
	// The sign-in page reaches this route with a link, so a browser gets the branded failure page with the same status; other clients keep JSON.
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
	// Google and Facebook refuse sign-in inside an Android WebView; send the guest to their own browser before any attempt exists.
	if refusesEmbeddedBrowser(provider) && isAndroidWebView(r.UserAgent()) {
		h.renderOpenInBrowser(w, r, provider)
		return
	}
	ip := clientIP(r)
	if ip == nil {
		fail(400, "err.device.detect", "bad ip")
		return
	}
	// The MAC comes from the kernel neighbour table, never the client, and the kernel is asked to resolve it: a cold cache is not evidence the device is off the guest network.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		fail(400, "err.device.network", "device not on this Wi-Fi network")
		return
	}

	// Used only by the development stub over plain HTTP: for a real provider scd decides the redirect URI from the portal's HTTPS state, since a Host header is the client's word.
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
			// scd's reason is for the log, not for the guest.
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

// Google, Microsoft and Facebook return code + state in a GET query; Sign in with Apple POSTs them as a form
// (form_post), so both are accepted. A real provider sends no `provider` parameter: scd resolves it from the state row.
// The cross-site POST carries no more authority than the GET: the state is bound to the starting device and consumed once.

func (h *handler) socialCallback(w http.ResponseWriter, r *http.Request) {
	provider, state, code := r.FormValue("provider"), r.FormValue("state"), r.FormValue("code")
	// Every refusal here lands in the guest's browser, so each is the branded failure page.
	if state == "" || code == "" {
		h.renderGuestError(w, r, http.StatusBadRequest, "errpage.social")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.renderGuestError(w, r, http.StatusBadRequest, "err.device.detect")
		return
	}
	// The MAC comes from the kernel neighbour table, never the client, and the kernel is asked to resolve it: a cold cache is not evidence the device is off the guest network.
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
		// scd's reason is for the log, not for the guest.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &e)
		slog.Info("social sign-in refused", "provider", provider, "status", resp.StatusCode, "reason", e.Error)
		h.renderGuestError(w, r, resp.StatusCode, "errpage.social")
		return
	}
	// The provider verified identity; the Client still has to join live access or choose a package.
	if h.tryIAMv2Auth(w, r, payload) {
		return
	}
	h.renderGuestError(w, r, http.StatusBadGateway, "err.service")
}

// forwardedHTTPS: Caddy terminates TLS and forwards over loopback; the header is trusted only from a loopback peer.
func forwardedHTTPS(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
