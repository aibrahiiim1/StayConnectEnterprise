package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/livez"
)

func unixGet(ctx context.Context, sock, path string) (int, []byte, error) {
	cl := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://x"+path, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, buf[:n], nil
}

// tcpGet never follows a redirect: the listener's own answer is what is measured. portald answers 302 to its
// canonical hostname once HTTPS is active, and from the appliance host that name reaches Caddy, which has no
// certificate for it, so following it would report a healthy portal as degraded.
func tcpGet(ctx context.Context, url string) (int, error) {
	cl := &http.Client{Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func checkSCD(ctx context.Context, s *server) (bool, string, string) {
	code, body, err := unixGet(ctx, "/run/stayconnect/scd.sock", "/v1/health")
	if err != nil {
		return false, "scd socket unreachable: " + errShort(err), ""
	}
	if code == 200 && strings.Contains(string(body), "ok") {
		return true, "scd /v1/health ok", ""
	}
	return false, fmt.Sprintf("scd /v1/health http %d", code), ""
}

func checkEdged(ctx context.Context, s *server) (bool, string, string) {
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.db.Ping(pctx); err != nil {
		return false, "site DB unreachable: " + errShort(err), "postgres"
	}
	return true, "edged serving; site DB reachable", ""
}

func checkNetd(ctx context.Context, s *server) (bool, string, string) {
	code, body, err := unixGet(ctx, "/run/stayconnect/netd.sock", "/v1/health")
	if err != nil {
		return false, "netd socket unreachable: " + errShort(err), ""
	}
	if code == 200 && strings.Contains(string(body), "netd") {
		return true, "netd /v1/health ok", ""
	}
	return false, fmt.Sprintf("netd /v1/health http %d", code), ""
}

// portaldHTTPSAddr is portald's TLS listener (PORTALD_HTTPS_ADDR).
const portaldHTTPSAddr = "127.0.0.1:8343"

func checkPortald(ctx context.Context, s *server) (bool, string, string) {
	// Any HTTP response, including the redirect to the HTTPS portal, means portald is serving.
	code, err := tcpGet(ctx, "http://127.0.0.1:8380/")
	if err != nil {
		return false, "captive portal not responding: " + errShort(err), ""
	}
	if code <= 0 {
		return false, "captive portal no response", ""
	}
	// While HTTPS is active clients use the TLS listener, so it must handshake for the canonical hostname with a
	// verifying certificate. HTTPS state (fallback, expiry) is the separate portal_https check.
	if s != nil && s.db != nil {
		if st, err := s.loadPortalTLSState(ctx); err == nil && st.HTTPSActive && st.CanonicalHostname != "" {
			tcode, terr := tlsListenerGet(ctx, portaldHTTPSAddr, st.CanonicalHostname)
			if terr != nil {
				return false, "HTTPS portal not serving " + st.CanonicalHostname + ": " + errShort(terr), ""
			}
			return true, fmt.Sprintf("captive portal responding (http %d; https %d for %s)", code, tcode, st.CanonicalHostname), ""
		}
	}
	return true, fmt.Sprintf("captive portal responding (http %d)", code), ""
}

// tlsListenerGet presents host as SNI and verifies the served certificate against the system roots; redirects are not followed.
func tlsListenerGet(ctx context.Context, addr, host string) (int, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig:     &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 3 * time.Second,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Timeout: 3 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+host+"/", nil)
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func checkAcctd(ctx context.Context, s *server) (bool, string, string) {
	age, ok := livez.Age("acctd")
	if !ok {
		return false, "no accounting-loop heartbeat yet", ""
	}
	if age > 15*time.Second {
		return false, fmt.Sprintf("accounting loop stalled (%.0fs since last tick)", age.Seconds()), ""
	}
	// A loop ticking on time can still fail every observation; the heartbeat alone cannot show it.
	if reason, reported := livez.Status("acctd"); reported && reason != "" {
		return false, "accounting loop is progressing but degraded: " + reason, ""
	}
	return true, fmt.Sprintf("accounting loop progressing (%.1fs)", age.Seconds()), ""
}

func checkHotelAdmin(ctx context.Context, s *server) (bool, string, string) {
	code, err := tcpGet(ctx, "http://127.0.0.1:3100/")
	if err != nil {
		return false, "Admin Console UI not responding: " + errShort(err), ""
	}
	if code >= 200 && code < 500 {
		return true, fmt.Sprintf("Admin Console UI responding (http %d)", code), ""
	}
	return false, fmt.Sprintf("Admin Console UI http %d", code), ""
}

func checkCaddy(ctx context.Context, s *server) (bool, string, string) {
	// Probe TLS on 443; Caddy's admin API is a unix socket only Caddy's own account can open.
	return caddyServesTLS(ctx, "127.0.0.1:443")
}

// caddyServesTLS: a completed handshake or a TLS alert (only a TLS server sends one) means Caddy is serving.
// Certificate validity is the TLS-certificate check's job.
func caddyServesTLS(ctx context.Context, addr string) (bool, string, string) {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: "hotel.stayconnect.local", InsecureSkipVerify: true}} //nolint:gosec // liveness only
	c, err := d.DialContext(dctx, "tcp", addr)
	if err == nil {
		_ = c.Close()
		return true, "Caddy answering TLS on " + addr, ""
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return true, "Caddy answering TLS on " + addr + " (" + alert.Error() + ")", ""
	}
	return false, "Caddy is not answering TLS on " + addr + ": " + errShort(err), ""
}

type keaView struct {
	KeaConfigured bool   `json:"kea_configured"`
	KeaHealthy    bool   `json:"kea_healthy"`
	KeaDetail     string `json:"kea_detail"`
}

func keaFromNetd(ctx context.Context, s *server) (keaView, bool) {
	code, body, err := unixGet(ctx, "/run/stayconnect/netd.sock", "/v1/health")
	if err != nil || code != 200 {
		return keaView{}, false
	}
	var v keaView
	if json.Unmarshal(body, &v) != nil {
		return keaView{}, false
	}
	return v, true
}

// keaApplicable: Kea runs only once a guest network is applied (it binds the LAN bridge); netd owns that fact.
// An unreachable netd means APPLICABLE, so it can never silently suppress a broken Kea.
func keaApplicable(ctx context.Context, s *server) (bool, string) {
	v, reachable := keaFromNetd(ctx, s)
	return keaApplicableFrom(v, reachable)
}

// keaApplicableFrom is split from the transport so the fail-open rule can be tested.
func keaApplicableFrom(v keaView, reachable bool) (bool, string) {
	if !reachable || v.KeaConfigured {
		return true, ""
	}
	detail := v.KeaDetail
	if detail == "" {
		detail = "waiting for client networking: Kea serves the LAN bridge, which does not exist yet"
	}
	return false, detail
}

// checkKea runs only once Kea is expected (see keaApplicable), so a failure here is real.
func checkKea(ctx context.Context, s *server) (bool, string, string) {
	code, body, err := unixGet(ctx, "/run/stayconnect/netd.sock", "/v1/health")
	if err != nil {
		return false, "cannot assess Kea (netd unreachable): " + errShort(err), "netd"
	}
	var v keaView
	_ = json.Unmarshal(body, &v)
	if code == 200 && v.KeaHealthy {
		return true, "Kea DHCP healthy (via netd control channel)", ""
	}
	if v.KeaDetail != "" {
		return false, v.KeaDetail, ""
	}
	return false, "Kea DHCP not healthy (netd reports it is not answering)", ""
}

func checkUnbound(ctx context.Context, s *server) (bool, string, string) {
	// Any DNS response, even NXDOMAIN, proves Unbound is answering.
	if dnsResponds(ctx, "127.0.0.1:53") {
		return true, "Unbound answering DNS on 127.0.0.1:53", ""
	}
	return false, "Unbound not answering DNS queries", ""
}

func checkPostgres(ctx context.Context, s *server) (bool, string, string) {
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.db.Ping(pctx); err != nil {
		return false, "site DB not ready: " + errShort(err), ""
	}
	return true, "site DB ready (ping ok)", ""
}

func dnsResponds(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	q := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		9, 'l', 'o', 'c', 'a', 'l', 'h', 'o', 's', 't', 0x00,
		0x00, 0x01, 0x00, 0x01,
	}
	if _, err := conn.Write(q); err != nil {
		return false
	}
	resp := make([]byte, 512)
	n, err := conn.Read(resp)
	if err != nil || n < 4 {
		return false
	}
	return resp[0] == 0x12 && resp[1] == 0x34 // response id matches
}

func errShort(err error) string {
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}
