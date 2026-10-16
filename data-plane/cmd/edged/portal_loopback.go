package main

// READING A PAGE FROM THE PORTAL ON THIS APPLIANCE, WITHOUT LEAVING IT.
//
// The portal preview and the portal's built-in wording (resources_branding.go) are portald's own responses,
// fetched over loopback. They used http.DefaultClient against portald's HTTP listener, which FOLLOWS REDIRECTS --
// and once HTTPS is active that listener answers every page with 302 https://<canonical portal hostname>/. From
// the appliance host that name resolves to Caddy's :443 (clients reach portald's TLS listener only through the
// guest-interface DNAT), which has no certificate for it, so both screens answered 503 "portal_unreachable" on a
// portal that was serving clients perfectly. It is the defect the portald health check had and lost on
// 2026-10-15 (health_checks.go tcpGet / tlsListenerGet), and it is fixed the same way:
//
//   - the HTTP listener is asked WITHOUT following redirects;
//   - a redirect while HTTPS is active is answered by asking portald's TLS listener on loopback directly
//     (127.0.0.1:8343), presenting the canonical hostname as SNI and Host -- exactly what a client's browser
//     sends after the redirect -- and verifying the certificate it serves;
//   - nothing is ever fetched from the redirect's target address, so a Location header can never take edged off
//     the box.

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

// portalLoopback is where portald listens on this appliance. The zero value is the appliance's own listeners;
// tests point it at their own.
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

// get fetches path from portald. canonical is the portal's canonical hostname while HTTPS is active, "" when it is
// not (or unknown). It returns portald's own status and body; a redirect that cannot be answered on loopback is
// returned as the redirect status, never followed.
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

	// HTTPS IS ACTIVE: ask the TLS listener this redirect points clients at, on loopback.
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

// portalGet fetches path from this appliance's portal, with the canonical hostname read from the portal's
// HTTPS state. An unreadable state reads as "HTTPS not active": the HTTP listener's answer is then reported as it
// is, never followed.
func (s *server) portalGet(ctx context.Context, path string) (int, []byte, error) {
	canonical := ""
	if s.db != nil {
		if st, err := s.loadPortalTLSState(ctx); err == nil && st.HTTPSActive {
			canonical = st.CanonicalHostname
		}
	}
	return s.portalLB.get(ctx, path, canonical)
}
