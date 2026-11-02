package main

// No HSTS anywhere: it would pin a guest's browser to HTTPS and make the HTTP fallback unreachable when needed.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
)

const portalTLSPollEvery = 15 * time.Second

// From this Captive Portal Login version Android opens the portal in a Chrome Custom Tab; Google refuses sign-in in the old WebView.
const androidCustomTabsVersion = 361335020

type portalTLSState struct {
	HTTPSActive     bool   `json:"https_active"`
	Canonical       string `json:"canonical_hostname"`
	OneGateHostname string `json:"onegate_hostname"`
	CustomDomain    string `json:"custom_domain"`
	// CustomDomainStatus is VERIFIED only while the Site's portal domain passed Central's check.
	CustomDomainStatus string   `json:"custom_domain_status"`
	PortalIP           string   `json:"portal_ip"`
	PortalIPActive     bool     `json:"portal_ip_active"`
	FallbackReason     string   `json:"fallback_reason"`
	Hostnames          []string `json:"portal_hostnames"`
	CertDir            string   `json:"cert_dir"`
}

// isSiteDomain reports whether host is the Site's own portal domain with an installed certificate. A journey that
// began there (Google returns to it, section 5 of the contract) stays there: the commerce cookie is host-only, so a
// redirect to the canonical name would lose the session.
func (st portalTLSState) isSiteDomain(host string) bool {
	if host == "" || st.CustomDomain == "" || st.CustomDomainStatus != "VERIFIED" || host != strings.ToLower(st.CustomDomain) {
		return false
	}
	for _, h := range st.Hostnames {
		if h == host {
			return true
		}
	}
	return false
}

func (st portalTLSState) isPortalHost(host string) bool {
	if host == "" {
		return false
	}
	if host == st.Canonical || host == st.OneGateHostname || host == st.CustomDomain {
		return true
	}
	for _, h := range st.Hostnames {
		if h == host {
			return true
		}
	}
	return false
}

func (st portalTLSState) certDir() string {
	if st.CertDir != "" {
		return st.CertDir
	}
	return portaltls.DefaultDir
}

// sanitize validates scd's values, which become file paths and redirect targets.
func (st portalTLSState) sanitize() portalTLSState {
	clean := func(h string) string {
		h = strings.ToLower(strings.TrimSpace(h))
		if portaltls.ValidHostname(h) {
			return h
		}
		return ""
	}
	st.Canonical, st.OneGateHostname, st.CustomDomain = clean(st.Canonical), clean(st.OneGateHostname), clean(st.CustomDomain)
	var hs []string
	for _, h := range st.Hostnames {
		if c := clean(h); c != "" {
			hs = append(hs, c)
		}
	}
	st.Hostnames = hs
	if st.CertDir != "" && (!filepath.IsAbs(st.CertDir) || filepath.Clean(st.CertDir) != st.CertDir) {
		st.CertDir = ""
	}
	if st.Canonical == "" {
		st.HTTPSActive = false
	}
	// The portal address becomes a redirect target, so it must be a plain IPv4 address and nothing else.
	if ip := net.ParseIP(strings.TrimSpace(st.PortalIP)); ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
		st.PortalIP, st.PortalIPActive = "", false
	} else {
		st.PortalIP = ip.To4().String()
	}
	return st
}

type tlsStateHolder struct {
	mu       sync.RWMutex
	st       portalTLSState
	failures int
}

// A handler built without a holder (tests) is on the HTTP fallback.
func (h *handler) tlsState() portalTLSState {
	if h == nil || h.portalTLS == nil {
		return portalTLSState{}
	}
	h.portalTLS.mu.RLock()
	defer h.portalTLS.mu.RUnlock()
	st := h.portalTLS.st
	st.Hostnames = append([]string(nil), h.portalTLS.st.Hostnames...)
	return st
}

// Two consecutive failures fall back to HTTP (so an scd restart does not flap schemes); hostnames and the
// certificate directory are kept so in-flight handshakes still get a certificate.
func (h *handler) refreshTLSState(ctx context.Context) {
	if h.portalTLS == nil || h.scd == nil {
		return
	}
	st, err := h.fetchTLSState(ctx)
	h.portalTLS.mu.Lock()
	defer h.portalTLS.mu.Unlock()
	prev := h.portalTLS.st
	if err != nil {
		h.portalTLS.failures++
		if h.portalTLS.failures >= 2 && h.portalTLS.st.HTTPSActive {
			h.portalTLS.st.HTTPSActive = false
			h.portalTLS.st.FallbackReason = "SCD_UNREACHABLE"
			slog.Warn("portal https: scd cannot be asked; serving the HTTP fallback until it answers", "err", err)
		}
		return
	}
	h.portalTLS.failures = 0
	h.portalTLS.st = st
	// The portal's HTTPS names may be where a form posted over HTTP is re-posted (307): form-action allows them.
	setPortalFormOrigins(st)
	if prev.HTTPSActive != st.HTTPSActive || prev.Canonical != st.Canonical {
		slog.Info("portal https: state changed", "https_active", st.HTTPSActive, "canonical", st.Canonical,
			"fallback_reason", st.FallbackReason)
	}
}

func (h *handler) fetchTLSState(ctx context.Context) (portalTLSState, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/portal/tls-state", nil)
	resp, err := h.scd.Do(req)
	if err != nil {
		return portalTLSState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return portalTLSState{}, errors.New("scd answered " + resp.Status)
	}
	var st portalTLSState
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st); err != nil {
		return portalTLSState{}, err
	}
	return st.sanitize(), nil
}

func (h *handler) pollTLSState(ctx context.Context) {
	h.refreshTLSState(ctx)
	t := time.NewTicker(portalTLSPollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.refreshTLSState(ctx)
		}
	}
}

var probePaths = map[string]bool{
	"/hotspot-detect.html": true, "/library/test/success.html": true,
	"/generate_204": true, "/gen_204": true, "/connecttest.txt": true, "/ncsi.txt": true,
}

// httpExempt paths stay on HTTP while HTTPS is active; the captive API answers 404 rather than a redirect clients would not follow.
var httpExempt = map[string]bool{
	"/healthz": true, "/livez": true, "/readyz": true, "/captive-portal/api": true,
}

// isAdminLoopback reports whether the request is the Admin Console's own read of the portal: from this machine, with no
// forwarding header. Clients reach portald only through the gateway's DNAT, so their source is never loopback, and a
// proxy in front would add a forwarding header, which disqualifies the request.
func isAdminLoopback(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-Ip") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	ip := clientIP(r)
	return ip != nil && ip.IsLoopback()
}

func (h *handler) httpFront(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := h.tlsState()
		// The Admin Console's preview and wording reads (edged on loopback) are answered directly, whatever the portal's
		// HTTPS state. Redirecting them to the canonical name made the template preview fail every time that state, or
		// the portal's address, changed (2026-11-02; earlier too): the console depends on the page, not on how clients reach it.
		if httpExempt[r.URL.Path] || isAdminLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !st.HTTPSActive || st.Canonical == "" {
			// Cookies are tied to the host name a captive device probed, so a page load on any other name goes to the
			// portal address first. GET/HEAD only: a form posts to the origin its page came from.
			if st.PortalIPActive && st.PortalIP != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
				requestHost(r) != st.PortalIP && !fallbackPassThrough(r.URL.Path) {
				if probePaths[r.URL.Path] {
					slog.Info("captive probe redirected to the portal address", "path", r.URL.Path, "from", clientIPString(r))
				}
				http.Redirect(w, r, "http://"+st.PortalIP+requestPathAndQuery(r), http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		origin := "https://" + st.Canonical
		if probePaths[r.URL.Path] {
			// Logged here because this is answered before the router's request log; address only, no headers or MAC.
			slog.Info("captive probe redirected to the HTTPS portal", "path", r.URL.Path, "from", clientIPString(r))
			http.Redirect(w, r, origin+"/", http.StatusFound)
			return
		}
		// 302 for navigation; 307 for a form posted over HTTP, which keeps the method and body.
		code := http.StatusFound
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			code = http.StatusTemporaryRedirect
		}
		http.Redirect(w, r, origin+requestPathAndQuery(r), code)
	})
}

func fallbackPassThrough(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/assets/")
}

// The portal address while it answers, else the address the connection reached (the gateway, DNAT'd by nftables).
// Never the .local name: Apple resolves .local over multicast DNS only.
func (h *handler) fallbackOrigin(r *http.Request) string {
	if st := h.tlsState(); st.PortalIPActive && st.PortalIP != "" {
		return "http://" + st.PortalIP
	}
	if ip := connectionLocalIP(r); ip != nil {
		return "http://" + ip.String()
	}
	return ""
}

func connectionLocalIP(r *http.Request) net.IP {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return nil
	}
	return ip.To4()
}

// Rebuilt from the parsed URL so a client cannot smuggle a scheme or host through an absolute-form request line.
func requestPathAndQuery(r *http.Request) string {
	p := r.URL.EscapedPath()
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		p = "/"
	}
	if r.URL.RawQuery != "" {
		p += "?" + r.URL.RawQuery
	}
	return p
}

func requestHost(r *http.Request) string {
	host := r.Host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// canonicalExempt paths are served on every portal hostname: the captive API and social sign-in (the attempt's stored
// redirect URI picks the hostname).
func canonicalExempt(path string) bool {
	return path == "/captive-portal/api" || strings.HasPrefix(path, "/auth/social/")
}

// Never Strict-Transport-Security (see file top); a handler may set a stricter Referrer-Policy.
func setTLSSecurityHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

func (h *handler) tlsFront(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setTLSSecurityHeaders(w)
		st := h.tlsState()
		host := requestHost(r)
		if !st.isPortalHost(host) {
			// The certificate was for another name, so a real browser never gets here.
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		if st.Canonical != "" && host != st.Canonical && !st.isSiteDomain(host) && !canonicalExempt(r.URL.Path) {
			code := http.StatusFound
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				code = http.StatusTemporaryRedirect
			}
			http.Redirect(w, r, "https://"+st.Canonical+requestPathAndQuery(r), code)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loadedPair struct {
	cert             *tls.Certificate
	crtMod, keyMod   time.Time
	crtSize, keySize int64
	// checked is when the files were last looked at; within portalCertRecheck the pair is served without a look.
	checked time.Time
}

// portalCertRecheck matches the TLS state poll: every handshake from an unsigned device's apps reaches here, so the files
// are looked at once per interval, not twice per handshake under the lock (perf D3-L3).
const portalCertRecheck = 15 * time.Second

// Re-reads a pair only when its files change; a failed re-read (a renewal between its two renames) keeps the old pair.
type portalCertServer struct {
	state func() portalTLSState
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]*loadedPair
}

func newPortalCertServer(state func() portalTLSState) *portalCertServer {
	return &portalCertServer{state: state, now: time.Now, cache: map[string]*loadedPair{}}
}

// Picks by SNI, else the canonical hostname's. No certificate is a handshake error: the listener starts before any exists.
func (c *portalCertServer) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	st := c.state()
	name := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
	if !portaltls.ValidHostname(name) || !st.isPortalHost(name) {
		name = ""
	}
	var tried []string
	for _, cand := range append([]string{name, st.Canonical}, st.Hostnames...) {
		if cand == "" || contains(tried, cand) {
			continue
		}
		tried = append(tried, cand)
		if cert, err := c.load(st.certDir(), cand); err == nil {
			return cert, nil
		}
	}
	return nil, errors.New("no portal certificate is installed")
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (c *portalCertServer) load(dir, host string) (*tls.Certificate, error) {
	now := c.now()
	c.mu.Lock()
	if old := c.cache[host]; old != nil && now.Sub(old.checked) < portalCertRecheck {
		c.mu.Unlock()
		return old.cert, nil
	}
	c.mu.Unlock()
	crtPath, keyPath := filepath.Join(dir, host+".crt"), filepath.Join(dir, host+".key")
	ci, cerr := os.Stat(crtPath)
	ki, kerr := os.Stat(keyPath)
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.cache[host]
	if cerr != nil || kerr != nil {
		if cerr != nil && kerr != nil {
			// Both gone: the hostname is no longer served (scd removed it). Forget it.
			delete(c.cache, host)
			return nil, os.ErrNotExist
		}
		if old != nil {
			old.checked = now
			return old.cert, nil
		}
		return nil, os.ErrNotExist
	}
	if old != nil && old.crtMod.Equal(ci.ModTime()) && old.keyMod.Equal(ki.ModTime()) &&
		old.crtSize == ci.Size() && old.keySize == ki.Size() {
		old.checked = now
		return old.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		if old != nil {
			old.checked = now
			return old.cert, nil
		}
		return nil, err
	}
	c.cache[host] = &loadedPair{cert: &pair, crtMod: ci.ModTime(), keyMod: ki.ModTime(), crtSize: ci.Size(), keySize: ki.Size(), checked: now}
	return &pair, nil
}

func portalTLSConfig(certs *portalCertServer) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate}
}

// Drops "TLS handshake error" lines: until the first certificate is installed every handshake fails by design.
type handshakeNoiseFilter struct{}

func (handshakeNoiseFilter) Write(p []byte) (int, error) {
	if !strings.Contains(string(p), "TLS handshake error") {
		slog.Warn("portald https", "msg", strings.TrimSpace(string(p)))
	}
	return len(p), nil
}

func httpsErrorLog() *log.Logger { return log.New(handshakeNoiseFilter{}, "", 0) }

type captiveAPIAnswer struct {
	Captive             bool   `json:"captive"`
	UserPortalURL       string `json:"user-portal-url"`
	VenueInfoURL        string `json:"venue-info-url,omitempty"`
	SecondsRemaining    *int64 `json:"seconds-remaining,omitempty"`
	AndroidUseCustomTab int    `json:"x-android-use-custom-tabs,omitempty"`
}

// The device is the TCP peer address only (portald has no RealIP middleware, deliberately). Not served over plain HTTP: RFC 8908 requires TLS.
func (h *handler) captivePortalAPI(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.NotFound(w, r)
		return
	}
	st := h.tlsState()
	host := st.Canonical
	if host == "" {
		host = requestHost(r) // tlsFront already refused a name that is not the portal's
	}
	portalURL := "https://" + host + "/"
	ans := captiveAPIAnswer{Captive: true, UserPortalURL: portalURL}
	if online, remaining := h.deviceOnline(r.Context(), clientIP(r)); online {
		ans.Captive = false
		ans.SecondsRemaining = remaining
	} else {
		ans.VenueInfoURL = portalURL
		ans.AndroidUseCustomTab = androidCustomTabsVersion
	}
	w.Header().Set("Content-Type", "application/captive+json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(ans)
}

// Any failure answers "captive": a device wrongly told it is captive opens the portal (recoverable); one wrongly told it is online is stranded.
func (h *handler) deviceOnline(ctx context.Context, ip net.IP) (bool, *int64) {
	st, ok := h.deviceStatus(ctx, ip)
	// Only a session is online; there is no pre-auth Internet.
	if !ok || !st.Active {
		return false, nil
	}
	// Access ends at the earliest of online time left, hard expiry and the package's calendar end.
	if d := st.remaining(time.Now()); d != nil {
		n := int64(d.Seconds())
		return true, &n
	}
	return true, nil
}

func clientIPString(r *http.Request) string {
	if ip := clientIP(r); ip != nil {
		return ip.String()
	}
	return ""
}
