package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newCSR(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "appliance"},
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), priv
}

func TestSignAndBind(t *testing.T) {
	ca, err := LoadOrCreate(filepath.Join(t.TempDir(), "ca.key"), 1)
	if err != nil {
		t.Fatal(err)
	}
	csr, _ := newCSR(t)
	appID := "abc-123"
	sc, err := ca.SignApplianceCSR(csr, appID, "tenantX", "siteY", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	block, _ := pem.Decode(sc.CertPEM)
	cert, _ := x509.ParseCertificate(block.Bytes)
	if got := ApplianceIDFromCert(cert); got != appID {
		t.Fatalf("binding mismatch: got %q", got)
	}
	// Verify the cert chains to the CA.
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM())
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("chain verify: %v", err)
	}
	if cert.Subject.OrganizationalUnit[0] != "tenantX" || cert.Subject.Organization[0] != "siteY" {
		t.Fatalf("tenant/site binding wrong: %+v", cert.Subject)
	}
}

func TestPersistedKeyStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ca.key")
	ca1, _ := LoadOrCreate(p, 1)
	ca2, _ := LoadOrCreate(p, 1) // reload
	if ca1.KeyFingerprint() != ca2.KeyFingerprint() {
		t.Fatal("CA identity not stable across reload")
	}
	// POSIX perms are only meaningful on the Linux deployment target.
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("CA key mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestBadCSRRejected(t *testing.T) {
	ca, _ := LoadOrCreate(filepath.Join(t.TempDir(), "ca.key"), 1)
	if _, err := ca.SignApplianceCSR([]byte("not a csr"), "a", "t", "s", time.Hour); err == nil {
		t.Fatal("expected rejection of garbage CSR")
	}
}
