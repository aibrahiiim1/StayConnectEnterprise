package pmsd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pms.hotel.local"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// pmsServer accepts one connection (plain TCP when cert is nil) and echoes one line.
func pmsServer(t *testing.T, cert *tls.Certificate) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if cert != nil {
			c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*cert}})
		}
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err == nil {
			_, _ = c.Write(buf)
		}
	}()
	return ln.Addr().String()
}

func dialFIAS(t *testing.T, rev Revision) (net.Conn, error) {
	t.Helper()
	raw, err := net.Dial("tcp", rev.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return fiasTLS(context.Background(), raw, rev)
}

func TestFIASTLS_PinnedCertificateIsTheOnlyOneAccepted(t *testing.T) {
	cert := selfSigned(t)
	sum := sha256.Sum256(cert.Certificate[0])
	pin := strings.ToUpper(hex.EncodeToString(sum[:]))

	c, err := dialFIAS(t, Revision{Endpoint: pmsServer(t, &cert), TLS: true, TLSCertSHA256: pin, DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("pinned certificate refused: %v", err)
	}
	_, _ = c.Write([]byte("LS|1\r"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "LS|1\r" {
		t.Fatalf("no traffic over TLS: %q %v", buf, err)
	}
	c.Close()

	other := strings.Repeat("ab", 32)
	if _, err := dialFIAS(t, Revision{Endpoint: pmsServer(t, &cert), TLS: true, TLSCertSHA256: other, DialTimeout: 2 * time.Second}); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a different certificate was accepted: %v", err)
	}
}

func TestFIASTLS_NeverFallsBackToPlainTCP(t *testing.T) {
	// A PMS that speaks plain TCP: the handshake fails and no guest data would flow.
	if _, err := dialFIAS(t, Revision{Endpoint: pmsServer(t, nil), TLS: true, DialTimeout: 500 * time.Millisecond}); err == nil {
		t.Fatal("a plain-TCP PMS was accepted with TLS on")
	}
	// Without a pin, a self-signed certificate is refused: it does not chain to a system authority.
	cert := selfSigned(t)
	if _, err := dialFIAS(t, Revision{Endpoint: pmsServer(t, &cert), TLS: true, DialTimeout: 2 * time.Second}); err == nil {
		t.Fatal("an unpinned self-signed certificate was accepted")
	}
}
