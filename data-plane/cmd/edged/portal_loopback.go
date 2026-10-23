package main

// Fetches portald pages over loopback. Once HTTPS is active the HTTP listener answers 302 to the canonical
// hostname, which on the appliance host resolves to Caddy's :443 (no certificate), so redirects are never followed:
// the TLS listener is asked directly with the canonical hostname as SNI/Host and its certificate verified.
// Nothing is fetched from a redirect target, so a Location header cannot take edged off the box.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	portalHTTPBase      = "http://127.0.0.1:8380"
	portalBodyLimit     = 2 << 20
	portalLoopbackLimit = 5 * time.Second
)

// portalLoopback locates portald's listeners; the zero value is the appliance's own.
type portalLoopback struct {
	httpBase  string         // portald's HTTP listener, e.g. http://127.0.0.1:8380
	httpsAddr string         // portald's TLS listener, e.g. 127.0.0.1:8343
	roots     *x509.CertPool // nil = the system roots, what a client's browser checks
}

func (p portalLoopback) withDefaults() portalLoopback {
	if p.httpBase == "" {
		p.httpBase = portalHTTPBase
	}
	if p.httpsAddr == "" {
		p.httpsAddr = portaldHTTPSAddr
	}
	return p
}

func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// get fetches path from portald. canonical is the canonical hostname while HTTPS is active, "" otherwise.
// A redirect that cannot be answered on loopback is returned as the redirect status, never followed.
func (p portalLoopback) get(ctx context.Context, path, canonical string) (int, []byte, error) {
	p = p.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, portalLoopbackLimit)
	defer cancel()

	cl := &http.Client{CheckRedirect: noRedirects}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.httpBase+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 || canonical == "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, portalBodyLimit))
		return resp.StatusCode, body, err
	}

	// HTTPS is active: ask the TLS listener the redirect points at, on loopback.
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", p.httpsAddr)
		},
		TLSClientConfig:     &tls.Config{ServerName: canonical, RootCAs: p.roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 3 * time.Second,
	}
	defer tr.CloseIdleConnections()
	tcl := &http.Client{Transport: tr, CheckRedirect: noRedirects}
	treq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+canonical+path, nil)
	if err != nil {
		return 0, nil, err
	}
	tresp, err := tcl.Do(treq)
	if err != nil {
		return 0, nil, err
	}
	defer tresp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(tresp.Body, portalBodyLimit))
	return tresp.StatusCode, body, err
}

// portalGet fetches path from this appliance's portal; an unreadable HTTPS state reads as "not active".
func (s *server) portalGet(ctx context.Context, path string) (int, []byte, error) {
	canonical := ""
	if s.db != nil {
		if st, err := s.loadPortalTLSState(ctx); err == nil && st.HTTPSActive {
			canonical = st.CanonicalHostname
		}
	}
	return s.portalLB.get(ctx, path, canonical)
}
