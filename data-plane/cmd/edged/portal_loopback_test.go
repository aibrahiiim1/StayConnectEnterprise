package main

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The portal preview must not follow portald's HTTPS redirect off the box. These tests stand up portald's HTTP
// listener, its TLS listener and the redirect target, and assert who is asked.

type loopbackRig struct {
	httpSrv, tlsSrv, offBox *httptest.Server
	offBoxHits, tlsHits     atomic.Int32
	tlsHost                 atomic.Value
}

func newLoopbackRig(t *testing.T, httpsActive bool) *loopbackRig {
	t.Helper()
	rig := &loopbackRig{}
	rig.offBox = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.offBoxHits.Add(1)
		_, _ = w.Write([]byte("caddy"))
	}))
	t.Cleanup(rig.offBox.Close)
	rig.tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.tlsHits.Add(1)
		rig.tlsHost.Store(r.Host)
		if r.URL.Path == "/api/languages" {
			_, _ = w.Write([]byte(`{"en":{"title":"Welcome"}}`))
			return
		}
		_, _ = w.Write([]byte("<html>the real portal over TLS</html>"))
	}))
	t.Cleanup(rig.tlsSrv.Close)
	rig.httpSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpsActive {
			// portald's httpFront redirects every page to the canonical HTTPS origin.
			http.Redirect(w, r, rig.offBox.URL+r.URL.Path, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("<html>the real portal over HTTP</html>"))
	}))
	t.Cleanup(rig.httpSrv.Close)
	return rig
}

func (rig *loopbackRig) loopback() portalLoopback {
	roots := x509.NewCertPool()
	roots.AddCert(rig.tlsSrv.Certificate())
	return portalLoopback{httpBase: rig.httpSrv.URL, httpsAddr: strings.TrimPrefix(rig.tlsSrv.URL, "https://"), roots: roots}
}

func TestPortalLoopback_HTTPSInactiveReadsTheHTTPListener(t *testing.T) {
	rig := newLoopbackRig(t, false)
	st, body, err := rig.loopback().get(context.Background(), "/", "")
	if err != nil || st != 200 || !strings.Contains(string(body), "over HTTP") {
		t.Fatalf("get = %d %q %v", st, body, err)
	}
	if rig.tlsHits.Load() != 0 || rig.offBoxHits.Load() != 0 {
		t.Fatal("nothing but the HTTP listener may be asked while HTTPS is not active")
	}
}

func TestPortalLoopback_HTTPSActiveAsksTheTLSListenerOnLoopbackWithTheCanonicalHost(t *testing.T) {
	rig := newLoopbackRig(t, true)
	// httptest's certificate is for example.com, standing in for the canonical hostname.
	st, body, err := rig.loopback().get(context.Background(), "/api/languages", "example.com")
	if err != nil || st != 200 || !strings.Contains(string(body), "Welcome") {
		t.Fatalf("get = %d %q %v", st, body, err)
	}
	if rig.offBoxHits.Load() != 0 {
		t.Fatal("the redirect was FOLLOWED off the box: this is the 503 the audit found")
	}
	if rig.tlsHits.Load() != 1 || rig.tlsHost.Load() != "example.com" {
		t.Fatalf("TLS listener hits=%d host=%v, want one request for the canonical hostname",
			rig.tlsHits.Load(), rig.tlsHost.Load())
	}
}

func TestPortalLoopback_RedirectWithoutAKnownCanonicalIsReportedNotFollowed(t *testing.T) {
	rig := newLoopbackRig(t, true)
	st, _, err := rig.loopback().get(context.Background(), "/", "")
	if err != nil || st != http.StatusFound {
		t.Fatalf("get = %d %v, want portald's own 302 reported as it is", st, err)
	}
	if rig.offBoxHits.Load() != 0 || rig.tlsHits.Load() != 0 {
		t.Fatal("an unanswerable redirect must not be followed anywhere")
	}
}

func TestPortalLoopback_UntrustedCertificateIsAnError(t *testing.T) {
	rig := newLoopbackRig(t, true)
	lb := rig.loopback()
	lb.roots = x509.NewCertPool() // trusts nothing
	if _, _, err := lb.get(context.Background(), "/", "example.com"); err == nil {
		t.Fatal("a certificate a client's browser would refuse must not be read as the portal serving")
	}
}

func TestPortalPreviewHandler_ServesThePortalPageThroughTheLoopback(t *testing.T) {
	rig := newLoopbackRig(t, false)
	s := &server{portalLB: rig.loopback()}
	rec := httptest.NewRecorder()
	s.previewPortal(rec, httptest.NewRequest(http.MethodGet, "/portal-branding/preview", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "the real portal over HTTP") {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
}
