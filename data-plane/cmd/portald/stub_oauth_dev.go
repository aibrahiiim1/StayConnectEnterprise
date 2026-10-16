//go:build devlicense

package main

// THE DEVELOPMENT OAUTH STUB: a fake provider consent screen, compiled ONLY into a `-tags devlicense` build
// (the same tag internal/buildprofile uses for a development binary). A production portald has no such route at
// all (stub_oauth_prod.go), exactly as a production scd never registers the stub provider (HTTPS contract
// section 11).
//
// Even here it is not an open redirect (audit P-2): it only ever sends the browser back to THIS portal's own
// social callback on the origin the stub was reached on, and its page carries the portal's CSP.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

func mountStubOAuth(r chi.Router, h *handler) {
	r.Get("/api/oauth/stub/authorize", h.stubAuthorize)
	r.Post("/api/oauth/stub/authorize-confirm", h.stubAuthorizeConfirm)
}

// stubRedirectAllowed reports whether redirectURI is this portal's own social callback, on the origin this
// request reached: http(s)://<r.Host>/auth/social/callback with nothing else in it.
func stubRedirectAllowed(r *http.Request, redirectURI string) bool {
	u, err := url.Parse(redirectURI)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != "" && strings.EqualFold(u.Host, r.Host) && u.Path == "/auth/social/callback"
}

func stubReturn(w http.ResponseWriter, r *http.Request, provider, state, redirectURI, email string, verified bool) {
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

func (h *handler) stubAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider := q.Get("provider")
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")
	if provider == "" || state == "" || redirectURI == "" {
		http.Error(w, "missing provider/state/redirect_uri", http.StatusBadRequest)
		return
	}
	if !stubRedirectAllowed(r, redirectURI) {
		http.Error(w, "redirect_uri must be this portal's social callback", http.StatusBadRequest)
		return
	}
	// Auto-mode: if email is present in the query, skip the consent UI and redirect immediately (E2E tests).
	if email := q.Get("email"); email != "" && q.Get("auto") == "1" {
		stubReturn(w, r, provider, state, redirectURI, email, q.Get("email_verified") != "false")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = setPortalCSP(w)
	// Template uses provider twice: page heading + hidden form field.
	fmt.Fprintf(w, stubConsentHTML, htmlEscape(provider), htmlEscape(provider),
		htmlEscape(state), htmlEscape(redirectURI))
}

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
	if !stubRedirectAllowed(r, redirectURI) {
		http.Error(w, "redirect_uri must be this portal's social callback", http.StatusBadRequest)
		return
	}
	stubReturn(w, r, provider, state, redirectURI, email, verified)
}
