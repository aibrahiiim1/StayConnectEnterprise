// Package clientip decides which address a request came from. Caddy connects from loopback and replaces
// X-Real-IP, so that header is trusted only when the TCP peer is loopback; elsewhere it is client-chosen and the
// TCP peer is the answer. Without X-Real-IP, the last X-Forwarded-For hop (appended by the proxy) is used, never
// the first, which is the client's own claim.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

func From(r *http.Request) string {
	return Resolve(r.RemoteAddr, r.Header.Get("X-Real-IP"), r.Header.Get("X-Forwarded-For"))
}

func Resolve(remoteAddr, xRealIP, xForwardedFor string) string {
	peer := hostOf(remoteAddr)
	if !IsLoopback(peer) {
		return peer
	}
	if ip := net.ParseIP(strings.TrimSpace(xRealIP)); ip != nil {
		return ip.String()
	}
	if xForwardedFor != "" {
		hops := strings.Split(xForwardedFor, ",")
		if ip := net.ParseIP(strings.TrimSpace(hops[len(hops)-1])); ip != nil {
			return ip.String()
		}
	}
	return peer
}

// Peer is the TCP peer, ignoring every forwarding header.
func Peer(r *http.Request) string { return hostOf(r.RemoteAddr) }

func Proxied(r *http.Request) bool {
	return r.Header.Get("X-Real-IP") != "" || r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("Forwarded") != ""
}

func IsLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}
