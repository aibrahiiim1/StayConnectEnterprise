// Package clientip decides which address a request came from.
//
// ctrlapi sits behind Caddy on the same host (deploy/caddy/Caddyfile.central), which connects from loopback
// and REPLACES X-Real-IP with the address it accepted the connection from. That header is therefore trusted
// only when the TCP peer is loopback. From anywhere else it is whatever the client chose to send, and a rate
// limit keyed on it is a rate limit the client controls, so it is ignored and the TCP peer is the answer.
//
// When the loopback peer sent no X-Real-IP, the LAST X-Forwarded-For hop is used: it is the one appended by
// the proxy that connected to us. The first hop is the client's own claim.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// From returns the client address for r.
func From(r *http.Request) string {
	return Resolve(r.RemoteAddr, r.Header.Get("X-Real-IP"), r.Header.Get("X-Forwarded-For"))
}

// Resolve is From without the request, for testing.
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

// Proxied reports whether the request carries forwarding headers, i.e. arrived through a proxy.
func Proxied(r *http.Request) bool {
	return r.Header.Get("X-Real-IP") != "" || r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("Forwarded") != ""
}

// IsLoopback reports whether host is a loopback address.
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
