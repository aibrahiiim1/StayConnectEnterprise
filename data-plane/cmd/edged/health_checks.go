package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/livez"
)

// Health checks below are deliberately MEANINGFUL — they probe the service's
// real surface (socket/API/DNS/DB), not merely "is the process running", so an
// active-but-wedged service is caught. Each returns (ok, sanitized-detail, dep).

// unixGet does a short GET over a unix-socket HTTP server.
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

// tcpGet does a short GET to a loopback TCP address.
func tcpGet(ctx context.Context, url string) (int, error) {
	cl := &http.Client{Timeout: 3 * time.Second}
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

func checkPortald(ctx context.Context, s *server) (bool, string, string) {
	// The captive portal listener must answer. Any HTTP response (including a
	// redirect) means portald is serving.
	code, err := tcpGet(ctx, "http://127.0.0.1:8380/")
	if err != nil {
		return false, "captive portal not responding: " + errShort(err), ""
	}
	if code > 0 {
		return true, fmt.Sprintf("captive portal responding (http %d)", code), ""
	}
	return false, "captive portal no response", ""
}

func checkAcctd(ctx context.Context, s *server) (bool, string, string) {
	age, ok := livez.Age("acctd")
	if !ok {
		return false, "no accounting-loop heartbeat yet", ""
	}
	// acctd ticks ~1s; allow generous slack for load.
	if age > 15*time.Second {
		return false, fmt.Sprintf("accounting loop stalled (%.0fs since last tick)", age.Seconds()), ""
	}
	// A loop that is ticking on time can still be failing every observation it makes — unreadable counters,
	// refused ingestion, a shaping plan netd would not apply. That is not healthy, and it is invisible from
	// the heartbeat alone.
	if reason, reported := livez.Status("acctd"); reported && reason != "" {
		return false, "accounting loop is progressing but degraded: " + reason, ""
	}
	return true, fmt.Sprintf("accounting loop progressing (%.1fs)", age.Seconds()), ""
}

func checkHotelAdmin(ctx context.Context, s *server) (bool, string, string) {
	code, err := tcpGet(ctx, "http://127.0.0.1:3100/")
	if err != nil {
		return false, "Hotel Admin UI not responding: " + errShort(err), ""
	}
	if code >= 200 && code < 500 {
		return true, fmt.Sprintf("Hotel Admin UI responding (http %d)", code), ""
	}
	return false, fmt.Sprintf("Hotel Admin UI http %d", code), ""
}

func checkCaddy(ctx context.Context, s *server) (bool, string, string) {
	// Caddy's admin API answering means the proxy is loaded and routes are live.
	code, err := tcpGet(ctx, "http://127.0.0.1:2019/config/")
	if err != nil {
		return false, "caddy admin not responding: " + errShort(err), ""
	}
	if code == 200 {
		return true, "caddy admin responsive; routes loaded", ""
	}
	return false, fmt.Sprintf("caddy admin http %d", code), ""
}

func checkKea(ctx context.Context, s *server) (bool, string, string) {
	// netd owns Kea and reports kea_healthy from its own control-channel probe.
	code, body, err := unixGet(ctx, "/run/stayconnect/netd.sock", "/v1/health")
	if err != nil {
		return false, "cannot assess Kea (netd unreachable): " + errShort(err), "netd"
	}
	var v struct {
		KeaHealthy bool `json:"kea_healthy"`
	}
	_ = json.Unmarshal(body, &v)
	if code == 200 && v.KeaHealthy {
		return true, "Kea DHCP healthy (via netd control channel)", ""
	}
	return false, "Kea DHCP not healthy (netd reports kea_healthy=false)", ""
}

func checkUnbound(ctx context.Context, s *server) (bool, string, string) {
	// A real DNS round-trip: any DNS response (even NXDOMAIN) proves Unbound is
	// answering. Only a timeout / connection refused is unhealthy.
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

// dnsResponds sends a minimal DNS A query for "localhost." and returns true if
// the server sends any well-formed response with the matching id.
func dnsResponds(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	// DNS query: id=0x1234, RD=1, 1 question, "localhost" A IN.
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
