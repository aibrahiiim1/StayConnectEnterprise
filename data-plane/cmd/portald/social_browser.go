package main

// "OPEN IN YOUR BROWSER" (contract docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §5).
//
// Google and Facebook refuse to sign anyone in inside an Android WebView -- the old captive-portal login
// window, whose user agent carries "; wv)". Sending a guest there produces the provider's own refusal page
// ("disallowed_useragent") with no way back. So for exactly those two providers in exactly that window the
// portal does not start an attempt at all: it shows the portal's address, a button to copy it, and the way
// back to the other sign-in methods. Apple and Microsoft sign in fine in a WebView and proceed as usual; so
// does every provider in a real browser or an Android Custom Tab (whose user agent is Chrome's, without wv).

import (
	"html/template"
	"net"
	"net/http"
	"strings"
)

// isAndroidWebView reports the Android WebView marker. Chrome Custom Tabs and the Chrome app do not carry it.
func isAndroidWebView(ua string) bool { return strings.Contains(ua, "; wv)") }

// refusesEmbeddedBrowser names the providers that refuse sign-in inside a WebView.
func refusesEmbeddedBrowser(provider string) bool {
	return provider == "google" || provider == "facebook"
}

// providerDisplayName is the provider's own name; it is a brand and never translated.
func providerDisplayName(provider string) string {
	switch provider {
	case "google":
		return "Google"
	case "facebook":
		return "Facebook"
	case "microsoft":
		return "Microsoft"
	case "apple":
		return "Apple"
	}
	return provider
}

// portalAddress is the address a guest should open in their own browser: https://<canonical>/ while HTTPS is
// active, from portald's own view of scd's portal state (portal_https.go). While that view says HTTPS is not
// active there is no HTTPS address to give. A handler with no view at all (tests' literal handlers) falls back
// to the host this request arrived on over TLS -- the guest is evidently reaching the portal by that name. Over
// plain HTTP the page shows only its instructions and the way back.
func (h *handler) portalAddress(r *http.Request) string {
	if h != nil && h.portalTLS != nil {
		st := h.tlsState()
		if st.HTTPSActive && validHostname(st.Canonical) {
			return "https://" + strings.ToLower(st.Canonical) + "/"
		}
		return ""
	}
	if r.TLS != nil || forwardedHTTPS(r) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if validHostname(host) {
			return "https://" + strings.ToLower(host) + "/"
		}
	}
	return ""
}

// validHostname accepts a DNS name (not an address) of letters, digits, hyphens and dots. Anything else -- a
// Host header a client invented -- is not repeated back on the page.
func validHostname(h string) bool {
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return false
	}
	for _, c := range h {
		if !(c == '-' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

type openBrowserView struct {
	guestPage
	Lead    string
	Address string
}

var openBrowserTmpl = template.Must(template.New("open-browser").Parse(compactMarkup(openBrowserHTML)))

// renderOpenInBrowser draws the interstitial. 200: nothing failed; the guest is being told where to go.
func (h *handler) renderOpenInBrowser(w http.ResponseWriter, r *http.Request, provider string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	_ = openBrowserTmpl.Execute(w, openBrowserView{
		guestPage: p,
		Lead:      strings.ReplaceAll(p.T["social.browser.lead"], "{p}", providerDisplayName(provider)),
		Address:   h.portalAddress(r),
	})
}

const openBrowserHTML = guestHead + `
<title>{{index .T "social.browser.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <div class="status-icon" aria-hidden="true">` + iconGlobe + `</div>
  <h1 class="page-title">{{index .T "social.browser.title"}}</h1>
  <p class="page-lead">{{.Lead}}</p>
  {{if .Address}}<p class="page-lead"><strong id="portal-address" dir="ltr">{{.Address}}</strong></p>
  <div class="actions"><button class="btn" type="button" id="copy-address" data-copied="{{index .T "social.browser.copied"}}">{{index .T "social.browser.copy"}}</button></div>{{end}}
  <div class="actions"><a class="btn btn--outline" href="/">{{index .T "social.browser.other"}}</a></div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
<script nonce="{{.Nonce}}">
(function () {
  var b = document.getElementById('copy-address');
  var a = document.getElementById('portal-address');
  if (!b || !a) return;
  function done() { b.textContent = b.getAttribute('data-copied'); }
  function fallback() {
    var t = document.createElement('textarea');
    t.value = a.textContent; t.setAttribute('readonly', ''); t.style.position = 'fixed'; t.style.opacity = '0';
    document.body.appendChild(t); t.select();
    try { if (document.execCommand('copy')) done(); } catch (e) {}
    document.body.removeChild(t);
  }
  b.addEventListener('click', function () {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(a.textContent).then(done, fallback);
    } else { fallback(); }
  });
})();
</script>
</body></html>`
