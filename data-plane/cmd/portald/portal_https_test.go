package main

// THE HTTPS PORTAL FROM PORTALD'S SIDE: the redirects in both states, the canonical-hostname rule on the TLS
// listener, the RFC 8908 API, certificates by SNI (and their reload), and the absence of HSTS.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
	"github.com/stayconnect/enterprise/data-plane/internal/portaltls/portaltlstest"
)

const (
	tOG     = "ab12cd34.gw.onegate.work"
	tCustom = "wifi.nilehotel.com"
)

type scdFunc func(r *http.Request) (int, any)

func (f scdFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	code, v := f(r)
	if code == 0 {
		return nil, errors.New("connection refused")
	}
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(b))),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
}

func httpsHandler(t *testing.T, st portalTLSState, scd scdFunc) *handler {
	t.Helper()
	h, err := newHandler(cfg{PortalFQDN: "portal.stayconnect.local"})
	if err != nil {
		t.Fatal(err)
	}
	if scd == nil {
		scd = func(*http.Request) (int, any) { return 0, nil }
	}
	h.scd = &http.Client{Transport: scd}
	h.designs = nil
	h.portalTLS = &tlsStateHolder{st: st}
	return h
}

var activeState = portalTLSState{HTTPSActive: true, Canonical: tCustom, OneGateHostname: tOG, CustomDomain: tCustom,
	Hostnames: []string{tOG, tCustom}, PortalIP: "10.255.255.1"}

// ---- the HTTP listener ---------------------------------------------------------------------------------------

func TestOnTheFallbackTheHTTPPortalIsExactlyAsBefore(t *testing.T) {
	for name, st := range map[string]*tlsStateHolder{
		"no state":     nil,
		"fallback":     {st: portalTLSState{FallbackReason: "NO_CERTIFICATE", OneGateHostname: tOG}},
		"no canonical": {st: portalTLSState{HTTPSActive: true}},
	} {
		h := httpsHandler(t, portalTLSState{}, nil)
		h.portalTLS = st
		srv := h.httpFront(h.routes())
		for path, want := range map[string]int{
			"/generate_204": http.StatusFound, "/connecttest.txt": http.StatusFound, "/hotspot-detect.html": http.StatusOK,
		} {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil))
			if rec.Code != want {
				t.Fatalf("%s %s: %d, want %d", name, path, rec.Code, want)
			}
			if loc := rec.Header().Get("Location"); strings.HasPrefix(loc, "https://") {
				t.Fatalf("%s %s: redirected to HTTPS on the fallback: %s", name, path, loc)
			}
		}
		// The Android/Windows probes keep today's http://<fqdn>/ target.
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/generate_204", nil))
		if loc := rec.Header().Get("Location"); loc != "http://portal.stayconnect.local/" {
			t.Fatalf("%s: probe target %q", name, loc)
		}
	}
}

func TestWhileHTTPSIsActiveHTTPGoesToTheCanonicalOrigin(t *testing.T) {
	h := httpsHandler(t, activeState, nil)
	srv := h.httpFront(h.routes())
	for _, p := range []string{"/hotspot-detect.html", "/library/test/success.html", "/generate_204", "/gen_204", "/connecttest.txt", "/ncsi.txt"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://captive.apple.com"+p, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://"+tCustom+"/" {
			t.Errorf("probe %s: %d %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	for in, want := range map[string]string{
		"/":                   "https://" + tCustom + "/",
		"/packages?x=1&y=two": "https://" + tCustom + "/packages?x=1&y=two",
		"/connect?c=a&k=1":    "https://" + tCustom + "/connect?c=a&k=1",
		"/any/other/path":     "https://" + tCustom + "/any/other/path",
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://10.0.0.1:8380"+in, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q, want %q", in, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	// A form posted to the HTTP page keeps its method and body.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://10.0.0.1:8380/auth/voucher", strings.NewReader("code=X")))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "https://"+tCustom+"/auth/voucher" {
		t.Fatalf("POST: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// The captive API is not redirected: over HTTP it does not exist.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://10.0.0.1:8380/captive-portal/api", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("captive API over HTTP: %d", rec.Code)
	}
}

func TestARedirectNeverCarriesAForeignHost(t *testing.T) {
	h := httpsHandler(t, activeState, nil)
	srv := h.httpFront(h.routes())
	req := httptest.NewRequest(http.MethodGet, "http://10.0.0.1:8380/", nil)
	req.URL.Path = "//evil.example/x"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://"+tCustom+"/") || strings.Contains(loc, "//evil") {
		t.Fatalf("location %q", loc)
	}
}

// ---- the TLS listener ----------------------------------------------------------------------------------------

func tlsReq(method, host, path string) *http.Request {
	r := httptest.NewRequest(method, "https://"+host+path, nil)
	r.TLS = &tls.ConnectionState{ServerName: host}
	r.RemoteAddr = "10.20.0.7:51515"
	return r
}

func TestTheTLSListenerKeepsGuestsOnTheCanonicalHostname(t *testing.T) {
	h := httpsHandler(t, activeState, func(*http.Request) (int, any) { return 200, map[string]any{"active": false} })
	srv := h.tlsFront(h.routes())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, tlsReq(http.MethodGet, "evil.example", "/"))
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("a name that is not the portal's: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, tlsReq(http.MethodGet, tOG+":443", "/packages?a=1"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://"+tCustom+"/packages?a=1" {
		t.Fatalf("non-canonical portal host: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// The captive API and social sign-in are answered on every portal hostname.
	for _, p := range []string{"/captive-portal/api", "/auth/social/callback?state=x", "/auth/social/start?provider=apple"} {
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, tlsReq(http.MethodGet, tOG, p))
		if loc := rec.Header().Get("Location"); strings.Contains(loc, tCustom) {
			t.Errorf("%s was redirected to the canonical hostname: %q", p, loc)
		}
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, tlsReq(http.MethodGet, tCustom, "/"))
	if rec.Code != http.StatusOK {
		t.Fatalf("canonical: %d", rec.Code)
	}
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "strict-origin-when-cross-origin"} {
		if rec.Header().Get(k) != v {
			t.Errorf("%s = %q", k, rec.Header().Get(k))
		}
	}
}

// NO HSTS -- on any response, over either listener, in either state. It would make the HTTP fallback unreachable.
func TestNoResponseEverCarriesHSTS(t *testing.T) {
	for _, st := range []portalTLSState{activeState, {}} {
		h := httpsHandler(t, st, func(*http.Request) (int, any) { return 200, map[string]any{"active": false} })
		mux := h.routes()
		for _, srv := range []http.Handler{h.tlsFront(mux), h.httpFront(mux)} {
			for _, p := range []string{"/", "/captive-portal/api", "/generate_204", "/hotspot-detect.html", "/connect?c=a&k=b", "/nope", "/status"} {
				for _, host := range []string{tCustom, tOG, "evil.example"} {
					rec := httptest.NewRecorder()
					srv.ServeHTTP(rec, tlsReq(http.MethodGet, host, p))
					if v := rec.Header().Get("Strict-Transport-Security"); v != "" {
						t.Fatalf("%s%s: Strict-Transport-Security %q", host, p, v)
					}
				}
			}
		}
	}
}

// ---- RFC 8908 --------------------------------------------------------------------------------------------------

func TestTheCaptiveAPIAnswersForTheConnectingDevice(t *testing.T) {
	var askedIP atomic.Value
	online := false
	h := httpsHandler(t, activeState, func(r *http.Request) (int, any) {
		if r.URL.Path != "/v1/sessions/status" {
			return 404, nil
		}
		askedIP.Store(r.URL.Query().Get("ip"))
		if !online {
			return 200, map[string]any{"active": false}
		}
		return 200, map[string]any{"active": true, "session_id": "s", "remaining_online_seconds": 3540.0}
	})
	srv := h.tlsFront(h.routes())

	req := tlsReq(http.MethodGet, tCustom, "/captive-portal/api")
	req.Header.Set("X-Forwarded-For", "10.99.99.99") // ignored: the device is the TCP peer
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/captive+json" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if askedIP.Load() != "10.20.0.7" {
		t.Fatalf("the device must be the connection's source address, asked %v", askedIP.Load())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	want := map[string]any{"captive": true, "user-portal-url": "https://" + tCustom + "/", "venue-info-url": "https://" + tCustom + "/",
		"x-android-use-custom-tabs": float64(androidCustomTabsVersion)}
	if len(got) != len(want) {
		t.Fatalf("captive: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("captive %s = %v, want %v", k, got[k], v)
		}
	}

	online = true
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, tlsReq(http.MethodGet, tOG, "/captive-portal/api")) // any portal hostname answers
	got = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["captive"] != false || got["seconds-remaining"] != float64(3540) || got["user-portal-url"] != "https://"+tCustom+"/" {
		t.Fatalf("online: %v", got)
	}
	if _, ok := got["x-android-use-custom-tabs"]; ok {
		t.Fatal("custom tabs are only asked for while captive")
	}

	// scd unreachable: captive (the recoverable mistake).
	h.scd = &http.Client{Transport: scdFunc(func(*http.Request) (int, any) { return 0, nil })}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, tlsReq(http.MethodGet, tCustom, "/captive-portal/api"))
	got = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["captive"] != true {
		t.Fatalf("scd down: %v", got)
	}
}

// ---- scd's state -----------------------------------------------------------------------------------------------

func TestTheStateComesFromSCDAndFallsBackWhenSCDIsGone(t *testing.T) {
	up := true
	h := httpsHandler(t, portalTLSState{}, func(r *http.Request) (int, any) {
		if !up {
			return 0, nil
		}
		if r.URL.Path != "/v1/portal/tls-state" {
			return 404, nil
		}
		return 200, map[string]any{"https_active": true, "canonical_hostname": tOG, "onegate_hostname": tOG,
			"portal_hostnames": []string{tOG, "../../etc/passwd"}, "cert_dir": "relative/dir"}
	})
	h.portalTLS = &tlsStateHolder{}
	h.refreshTLSState(context.Background())
	st := h.tlsState()
	if !st.HTTPSActive || st.Canonical != tOG || len(st.Hostnames) != 1 || st.CertDir != "" {
		t.Fatalf("sanitized state: %+v", st)
	}
	up = false
	h.refreshTLSState(context.Background())
	if !h.tlsState().HTTPSActive {
		t.Fatal("one failed poll must not flap the scheme")
	}
	h.refreshTLSState(context.Background())
	st = h.tlsState()
	if st.HTTPSActive || st.Canonical != tOG || len(st.Hostnames) != 1 {
		t.Fatalf("scd gone: fallback, but certificates still known: %+v", st)
	}
	// Active without a canonical hostname is not active.
	if (portalTLSState{HTTPSActive: true, Canonical: "Bad Host"}).sanitize().HTTPSActive {
		t.Fatal("an invalid canonical hostname cannot be active")
	}
}

// ---- certificates by SNI ---------------------------------------------------------------------------------------

func installFor(t *testing.T, store *portaltls.Store, ca *portaltlstest.CA, host string, now time.Time) string {
	t.Helper()
	csr, err := store.NewPendingKey(host)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := ca.IssueForCSR(csr, now.Add(-time.Hour), now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := store.Install(host, chain)
	if err != nil {
		t.Fatal(err)
	}
	return inst.Serial
}

func leafSerial(c *tls.Certificate) string {
	leaf := c.Leaf
	if leaf == nil {
		leaf, _ = x509.ParseCertificate(c.Certificate[0])
	}
	return strings.ToLower(leaf.SerialNumber.Text(16))
}

func TestCertificatesAreServedBySNIAndReloadedWhenReplaced(t *testing.T) {
	now := time.Now()
	ca, _ := portaltlstest.New(now)
	dir := t.TempDir()
	store := &portaltls.Store{Dir: dir, LookupGID: func(string) (int, bool) { return 1, true },
		Chown: func(string, int, int) error { return nil }, Roots: ca.Pool()}
	ogSerial := installFor(t, store, ca, tOG, now)
	cuSerial := installFor(t, store, ca, tCustom, now)

	st := activeState
	st.CertDir = dir
	cs := newPortalCertServer(func() portalTLSState { return st })
	for sni, want := range map[string]string{
		tOG: ogSerial, strings.ToUpper(tCustom): cuSerial, tOG + ".": ogSerial,
		"": cuSerial, "unknown.example.com": cuSerial, "../../etc/passwd": cuSerial,
	} {
		c, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: sni})
		if err != nil || leafSerial(c) != want {
			t.Errorf("SNI %q: %v (want serial %s)", sni, err, want)
		}
	}

	// A renewal replaces the files: the next handshake serves the new certificate, without a restart.
	time.Sleep(20 * time.Millisecond) // a distinct modification time on coarse filesystems
	newSerial := installFor(t, store, ca, tOG, now)
	c, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: tOG})
	if err != nil || leafSerial(c) != newSerial || newSerial == ogSerial {
		t.Fatalf("after renewal: %v serial %s, want %s", err, leafSerial(c), newSerial)
	}
	// Caught between the key and the certificate rename (a mismatched pair): the previous pair is kept.
	if _, err := store.NewPendingKey(tOG); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(store.PendingKeyPath(tOG))
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(store.KeyPath(tOG), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = cs.GetCertificate(&tls.ClientHelloInfo{ServerName: tOG})
	if err != nil || leafSerial(c) != newSerial {
		t.Fatalf("mid-renewal the previous pair must be served: %v", err)
	}
}

func TestTheTLSListenerHandshakesWithTheInstalledCertificateAndFailsWithoutOne(t *testing.T) {
	now := time.Now()
	ca, _ := portaltlstest.New(now)
	dir := t.TempDir()
	st := portalTLSState{CertDir: dir}
	var cur atomic.Value
	cur.Store(st)
	h := httpsHandler(t, st, nil)
	cs := newPortalCertServer(func() portalTLSState { return cur.Load().(portalTLSState) })
	srv := httptest.NewUnstartedServer(h.tlsFront(h.routes()))
	srv.TLS = portalTLSConfig(cs)
	srv.Config.ErrorLog = httpsErrorLog()
	srv.StartTLS()
	defer srv.Close()

	dial := func(name string, min uint16) error {
		pool := ca.Pool()
		conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{ServerName: name, RootCAs: pool, MaxVersion: min})
		if err != nil {
			return err
		}
		return conn.Close()
	}
	if err := dial(tOG, 0); err == nil {
		t.Fatal("with no certificate installed the handshake must fail")
	}
	store := &portaltls.Store{Dir: dir, LookupGID: func(string) (int, bool) { return 1, true },
		Chown: func(string, int, int) error { return nil }, Roots: ca.Pool()}
	installFor(t, store, ca, tOG, now)
	st = portalTLSState{HTTPSActive: true, Canonical: tOG, OneGateHostname: tOG, Hostnames: []string{tOG}, CertDir: dir}
	cur.Store(st)
	if err := dial(tOG, 0); err != nil {
		t.Fatalf("handshake with the installed certificate: %v", err)
	}
	if err := dial(tOG, tls.VersionTLS11); err == nil {
		t.Fatal("TLS 1.1 must be refused")
	}
}
