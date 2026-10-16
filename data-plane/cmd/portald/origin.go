package main

// WHO SENT THIS REQUEST: the portal's own page, or a page of another origin (audit P-18).

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// jsonContentType reports whether the request body is declared JSON. A cross-origin page can send a "simple"
// request (form or text/plain) without a CORS preflight; it cannot send application/json without one, and
// portald answers no preflight.
func jsonContentType(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// crossSite reports POSITIVE evidence that another origin's page sent this request: an Origin header naming a
// host that is not one of the portal's own, or Sec-Fetch-Site saying cross-site. A request with neither (an
// older captive browser) is not refused on that ground alone.
func (h *handler) crossSite(r *http.Request) bool {
	if o := strings.TrimSpace(r.Header.Get("Origin")); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" {
			return true
		}
		return !h.ownHost(r, u.Host)
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	return false
}

// ownHost reports whether host (with or without a port) names this portal: the host the request came to, the
// canonical and other portal hostnames, the portal address, or PORTALD_FQDN.
func (h *handler) ownHost(r *http.Request, host string) bool {
	bare := strings.ToLower(host)
	if hh, _, err := net.SplitHostPort(bare); err == nil {
		bare = hh
	}
	bare = strings.TrimSuffix(bare, ".")
	if bare == "" {
		return false
	}
	if bare == requestHost(r) {
		return true
	}
	st := h.tlsState()
	if st.isPortalHost(bare) || (st.PortalIP != "" && bare == st.PortalIP) {
		return true
	}
	return h.cfg.PortalFQDN != "" && bare == strings.ToLower(h.cfg.PortalFQDN)
}
