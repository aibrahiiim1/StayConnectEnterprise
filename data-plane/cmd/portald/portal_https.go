package main

// THE CLIENT PORTAL OVER HTTPS, AND THE APPROVED HTTP FALLBACK.
//
// docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md sections 4 and 6. scd decides whether the portal is
// on HTTPS (portal_tls.go there) and which hostname is canonical; portald asks it every 15 seconds and acts on the
// answer:
//
//	HTTP :8380   HTTPS active: OS probes -> 302 https://<canonical>/, every other page -> the same path on
//	             https://<canonical>. HTTPS not active: EXACTLY today's behaviour -- this file changes nothing.
//	TLS  :8343   certificates by SNI from /etc/stayconnect/portal-tls (scd installs them; reloaded when the
//	             file changes); a portal hostname that is not canonical is redirected to the canonical one,
//	             except the captive API and social sign-in; anything else is 421.
//	/captive-portal/api   RFC 8908, TLS only.
//
// NO HSTS, ANYWHERE. It would pin a guest's browser to HTTPS for the portal hostname and make the approved HTTP
// fallback unreachable exactly when it is needed (contract section 2). A test asserts its absence.

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

// portalTLSPollEvery is how often portald asks scd for the HTTPS state.
const portalTLSPollEvery = 15 * time.Second

// androidCustomTabsVersion is the Captive Portal Login module version from which Android opens the portal in a
// Chrome Custom Tab when the captive API asks for it (contract section 5): Google sign-in is refused inside the
// old WebView login window and allowed in a Custom Tab.
const androidCustomTabsVersion = 361335020

// portalTLSState is scd's GET /v1/portal/tls-state, as portald uses it.
type portalTLSState struct {
	HTTPSActive     bool     `json:"https_active"`
	Canonical       string   `json:"canonical_hostname"`
	OneGateHostname string   `json:"onegate_hostname"`
	CustomDomain    string   `json:"custom_domain"`
	PortalIP        string   `json:"portal_ip"`
	PortalIPActive  bool     `json:"portal_ip_active"`
	FallbackReason  string   `json:"fallback_reason"`
	Hostnames       []string `json:"portal_hostnames"`
	CertDir         string   `json:"cert_dir"`
}

// isPortalHost reports whether host is one of the portal's own hostnames.
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

// sanitize keeps only what portald may act on: hostnames that are valid DNS names, an absolute clean directory,
// and "active" only with a canonical hostname. scd is trusted, but a value that becomes a file path or a
// redirect target is checked where it is used.
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

// tlsStateHolder is portald's copy of scd's answer.
type tlsStateHolder struct {
	mu       sync.RWMutex
	st       portalTLSState
	failures int
}

// tlsState is the current state. A handler built without a holder (the tests' literal handlers) is on the
// fallback: exactly the portal as it was before HTTPS existed.
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

// refreshTLSState asks scd once.
//
// WHEN scd CANNOT BE ASKED the safe default is the fallback: do not send guests to an HTTPS portal portald can
// no longer vouch for. Two consecutive failures (about 15-30 seconds), not one, so that scd's own restart does
// not flap every guest between schemes. The hostnames and the certificate directory are KEPT: a handshake in
// progress still gets its certificate.
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

// pollTLSState asks at startup and every 15 seconds.
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

// ---- the HTTP listener -----------------------------------------------------------------------------------------

// probePaths are the operating systems' captive-portal probes (the same paths routes() serves).
var probePaths = map[string]bool{
	"/hotspot-detect.html": true, "/library/test/success.html": true,
	"/generate_204": true, "/gen_204": true, "/connecttest.txt": true, "/ncsi.txt": true,
}

// httpExempt are answered over HTTP even while HTTPS is active: health checks (local tooling, no browser) and the
// captive API, which answers 404 over HTTP rather than a redirect an API client would not follow.
var httpExempt = map[string]bool{
	"/healthz": true, "/livez": true, "/readyz": true, "/captive-portal/api": true,
}

// httpFront wraps the plain-HTTP listener. While HTTPS is active it sends every portal page to the canonical
// HTTPS origin; otherwise it is transparent and the portal behaves exactly as it always has.
func (h *handler) httpFront(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := h.tlsState()
		if httpExempt[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if !st.HTTPSActive || st.Canonical == "" {
			// THE HTTP FALLBACK HAS ONE ORIGIN TOO (audit P-27). A captive device arrives on whatever name it
			// probed -- captive.apple.com, connectivitycheck.gstatic.com, a site the guest typed -- and every
			// cookie the portal set was then tied to that name, so a step that changed host (the card provider's
			// return, a link) arrived without the sign-in. While the portal address is answering, a page load on
			// any other name is sent to http://<portal address>/ first, so the whole journey lives on one origin.
			// Only GET/HEAD page loads: a form posts to the origin its page came from, which is then this one.
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
			// Answered here, before the router and its request log, so it is logged here: without this line a
			// phone that checked the network and one that never did look the same in the journal, and "why did
			// the sign-in sheet not open" cannot be answered from it. The address only -- no headers, no MAC.
			slog.Info("captive probe redirected to the HTTPS portal", "path", r.URL.Path, "from", clientIPString(r))
			http.Redirect(w, r, origin+"/", http.StatusFound)
			return
		}
		// 302 for a navigation. A form posted to the HTTP page (a page loaded just before HTTPS came up) gets a
		// 307 instead, which keeps the method and the body: a 302 would turn the sign-in into a GET and lose it.
		code := http.StatusFound
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			code = http.StatusTemporaryRedirect
		}
		http.Redirect(w, r, origin+requestPathAndQuery(r), code)
	})
}

// fallbackPassThrough are the HTTP fallback's paths that are answered on whatever name they arrive: a page's own
// sub-resources and the JSON its scripts read (they follow the page, which is already on the portal's origin).
func fallbackPassThrough(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/assets/")
}

// fallbackOrigin is the HTTP fallback's own origin (audit P-21, P-15): the portal address while it is answering,
// otherwise the address the device's connection actually reached -- the network's gateway, which nftables
// redirects to the portal for a device that is not signed in. Never the .local name in PORTALD_FQDN: Apple
// devices resolve .local over multicast DNS only, so a redirect there never arrives. "" when neither is known.
func (h *handler) fallbackOrigin(r *http.Request) string {
	if st := h.tlsState(); st.PortalIPActive && st.PortalIP != "" {
		return "http://" + st.PortalIP
	}
	if ip := connectionLocalIP(r); ip != nil {
		return "http://" + ip.String()
	}
	return ""
}

// connectionLocalIP is the appliance address this request's TCP connection arrived on (after nftables DNAT), or nil.
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

// requestPathAndQuery is the request's path and query, rebuilt from the parsed URL: never a scheme or a host a
// client could smuggle into an absolute-form request line.
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

// ---- the TLS listener --------------------------------------------------------------------------------------------

// requestHost is the Host header, lower case, without port or trailing dot.
func requestHost(r *http.Request) string {
	host := r.Host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// canonicalExempt are answered on every portal hostname, without the canonical redirect: the captive API (API
// clients need not follow redirects, contract section 6) and social sign-in (the attempt's stored redirect URI
// decides which hostname the provider returns to, contract section 5).
func canonicalExempt(path string) bool {
	return path == "/captive-portal/api" || strings.HasPrefix(path, "/auth/social/")
}

// setTLSSecurityHeaders: the headers every page served over TLS carries, consistent with the portal's existing
// ones (portal_design.go setPortalCSP: nosniff and frame-ancestors 'none'). A handler may still set a stricter
// Referrer-Policy of its own (the /connect page does). NEVER Strict-Transport-Security -- see the file comment.
func setTLSSecurityHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

// tlsFront wraps the TLS listener.
func (h *handler) tlsFront(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setTLSSecurityHeaders(w)
		st := h.tlsState()
		host := requestHost(r)
		if !st.isPortalHost(host) {
			// A name this portal does not serve. The certificate it was handed was for another name, so a real
			// browser never gets here; whatever did is told plainly that it reached the wrong server.
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		if st.Canonical != "" && host != st.Canonical && !canonicalExempt(r.URL.Path) {
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

// ---- certificates by SNI -----------------------------------------------------------------------------------------

type loadedPair struct {
	cert             *tls.Certificate
	crtMod, keyMod   time.Time
	crtSize, keySize int64
}

// portalCertServer serves the certificate scd installed for each portal hostname. It re-reads a pair only when
// its files change (scd replaces them atomically on renewal), and when a re-read fails -- a renewal caught
// between its two renames -- it keeps serving the pair it already had.
type portalCertServer struct {
	state func() portalTLSState
	mu    sync.Mutex
	cache map[string]*loadedPair
}

func newPortalCertServer(state func() portalTLSState) *portalCertServer {
	return &portalCertServer{state: state, cache: map[string]*loadedPair{}}
}

// GetCertificate picks the certificate by SNI: the requested name when it is one of the portal's hostnames with a
// certificate on disk, otherwise the canonical hostname's. No certificate at all is a handshake error -- the
// listener runs before the first certificate exists and simply cannot complete a handshake until then.
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
			return old.cert, nil
		}
		return nil, os.ErrNotExist
	}
	if old != nil && old.crtMod.Equal(ci.ModTime()) && old.keyMod.Equal(ki.ModTime()) &&
		old.crtSize == ci.Size() && old.keySize == ki.Size() {
		return old.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		if old != nil {
			return old.cert, nil
		}
		return nil, err
	}
	c.cache[host] = &loadedPair{cert: &pair, crtMod: ci.ModTime(), keyMod: ki.ModTime(), crtSize: ci.Size(), keySize: ki.Size()}
	return &pair, nil
}

// portalTLSConfig is the TLS listener's configuration: TLS 1.2 and later, certificates by SNI.
func portalTLSConfig(certs *portalCertServer) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate}
}

// handshakeNoiseFilter drops "TLS handshake error" lines from the HTTPS server's log. Until the first
// certificate is installed every handshake fails by design, and the journal must not fill with it; every other
// server error is still logged.
type handshakeNoiseFilter struct{}

func (handshakeNoiseFilter) Write(p []byte) (int, error) {
	if !strings.Contains(string(p), "TLS handshake error") {
		slog.Warn("portald https", "msg", strings.TrimSpace(string(p)))
	}
	return len(p), nil
}

func httpsErrorLog() *log.Logger { return log.New(handshakeNoiseFilter{}, "", 0) }

// ---- RFC 8908 ----------------------------------------------------------------------------------------------------

type captiveAPIAnswer struct {
	Captive             bool   `json:"captive"`
	UserPortalURL       string `json:"user-portal-url"`
	VenueInfoURL        string `json:"venue-info-url,omitempty"`
	SecondsRemaining    *int64 `json:"seconds-remaining,omitempty"`
	AndroidUseCustomTab int    `json:"x-android-use-custom-tabs,omitempty"`
}

// captivePortalAPI answers GET /captive-portal/api for the requesting device (contract section 6). The device
// is the TCP peer address and nothing else -- portald has no RealIP middleware, deliberately (main.go). Over
// plain HTTP it does not exist: RFC 8908 requires TLS, and option 114 only ever names the HTTPS URI.
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

// deviceOnline asks scd (the same /v1/sessions/status the Status page uses) whether this address has a live
// session, and how long it has left when that is known. Any failure answers "captive": a device told it is
// captive opens the portal, which is the recoverable mistake; one wrongly told it is online is stranded.
func (h *handler) deviceOnline(ctx context.Context, ip net.IP) (bool, *int64) {
	st, ok := h.deviceStatus(ctx, ip)
	// NOTHING BUT A SESSION IS ONLINE. The temporary-access window the email code used to open went with the
	// email code (self-service contract section 1): no pre-auth Internet exists, so a device without a session
	// is captive.
	if !ok || !st.Active {
		return false, nil
	}
	// THE EARLIEST OF THE CLOCKS (audit P-22). Online time left counts only while connected; the hard expiry and
	// the package's calendar end arrive whether the minutes were used or not. Access ends at whichever comes
	// first, so that is what the device is told -- the online time alone overstated it whenever the window
	// closed sooner.
	if d := st.remaining(time.Now()); d != nil {
		n := int64(d.Seconds())
		return true, &n
	}
	return true, nil
}

// clientIPString is the connection's address for a log line ("" when it cannot be read).
func clientIPString(r *http.Request) string {
	if ip := clientIP(r); ip != nil {
		return ip.String()
	}
	return ""
}
