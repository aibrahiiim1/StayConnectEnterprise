package main

// Google and Facebook refuse sign-in inside an Android WebView (the captive-portal login window, user agent with "; wv)"),
// leaving the guest on a dead-end refusal. For those two providers there, no attempt starts: the page shows the portal
// address to copy and the way back. Other providers and real browsers or Custom Tabs proceed as usual.

import (
	"html/template"
	"net"
	"net/http"
	"strings"
)

// Chrome Custom Tabs and the Chrome app do not carry the WebView marker.
func isAndroidWebView(ua string) bool { return strings.Contains(ua, "; wv)") }

func refusesEmbeddedBrowser(provider string) bool {
	return provider == "google" || provider == "facebook"
}

// Provider names are brands and never translated.
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

// portalAddress is https://<canonical>/ while HTTPS is active; with no view at all (tests' literal handlers) it falls
// back to the host this TLS request arrived on. Over plain HTTP with HTTPS inactive there is no address to give.
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

// validHostname: anything else (a Host header a client invented) is not repeated back on the page.
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
  <div class="actions"><button class="btn" type="button" id="copy-address" data-copied="{{index .T "social.browser.copied"}}">{{index .T "social.browser.copy"}}</button></div>
  <p class="sc-vh" id="copy-status" role="status" aria-live="polite"></p>{{end}}
  <div class="actions"><a class="btn btn--outline" href="/">{{index .T "social.browser.other"}}</a></div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
<script nonce="{{.Nonce}}">
(function () {
  var b = document.getElementById('copy-address');
  var a = document.getElementById('portal-address');
  var s = document.getElementById('copy-status');
  if (!b || !a) return;
  // The button says Copied; the status line says it to a screen reader, which does not re-read a button.
  function done() {
    var c = b.getAttribute('data-copied');
    b.textContent = c;
    if (s) { s.textContent = ''; setTimeout(function () { s.textContent = c; }, 50); }
  }
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
