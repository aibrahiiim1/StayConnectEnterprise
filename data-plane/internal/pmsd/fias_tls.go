package pmsd

// TLS ON THE FIAS LINK (GDPR remediation plan C8, decision D-C8).
//
// The guest list (names, rooms, reservation numbers) crosses the hotel network on this socket. A revision with
// tls.enabled wraps it in TLS 1.2 or later; there is no fallback to plain TCP, so a failed handshake leaves the
// link down rather than sending guest data in the clear. Off by default: whether a property's Protel interface
// speaks TLS, and turning it on, is the hotel's decision.
//
// A hotel's PMS rarely has a publicly trusted certificate. With a pinned SHA-256 fingerprint the appliance
// accepts exactly that certificate and nothing else; without one the certificate must chain to a system
// authority and match the host in the PMS address.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func fiasTLSConfig(rev Revision) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(rev.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("fias tls: endpoint %q: %w", rev.Endpoint, err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if rev.TLSCertSHA256 == "" {
		return cfg, nil
	}
	want, err := hex.DecodeString(strings.ToLower(strings.ReplaceAll(rev.TLSCertSHA256, ":", "")))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("fias tls: the pinned certificate fingerprint is not a SHA-256 value")
	}
	// The chain is not checked against an authority: the pin replaces it, and is checked on every handshake.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("fias tls: the PMS presented no certificate")
		}
		got := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(got[:], want) != 1 {
			return errors.New("fias tls: the PMS certificate does not match the pinned fingerprint")
		}
		return nil
	}
	return cfg, nil
}

// fiasTLS completes the TLS handshake on an open connection, within the revision's dial timeout. On any failure
// the connection is closed.
func fiasTLS(ctx context.Context, raw net.Conn, rev Revision) (net.Conn, error) {
	cfg, err := fiasTLSConfig(rev)
	if err != nil {
		raw.Close()
		return nil, err
	}
	timeout := rev.DialTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := tls.Client(raw, cfg)
	if err := c.HandshakeContext(hctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("fias tls handshake: %w", err)
	}
	return c, nil
}
