package main

// Who sent this request: the portal's own page, or a page of another origin.

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// jsonContentType: a cross-origin page can send a "simple" request without a preflight but not application/json, and portald answers no preflight.
func jsonContentType(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// crossSite reports positive evidence of another origin (an Origin host that is not the portal's, or Sec-Fetch-Site cross-site); a request with neither is not refused on that ground alone.
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

// ownHost reports whether host names this portal: the request host, canonical and other portal hostnames, portal address, or PORTALD_FQDN.
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
