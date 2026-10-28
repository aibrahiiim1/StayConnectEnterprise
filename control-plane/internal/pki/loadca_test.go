package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type caFiles struct{ rootKey, rootCert, intCert, intKey string }

func newCAFiles(t *testing.T) caFiles {
	t.Helper()
	d := t.TempDir()
	return caFiles{
		rootKey:  filepath.Join(d, "offline", "root-ca.key"),
		rootCert: filepath.Join(d, "pki", "root-ca.crt"),
		intCert:  filepath.Join(d, "pki", "intermediate-ca.crt"),
		intKey:   filepath.Join(d, "pki", "intermediate-ca.key"),
	}
}

func (f caFiles) init(t *testing.T) *CA {
	t.Helper()
	ca, err := InitCA(f.rootKey, f.rootCert, f.intCert, f.intKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func dirFiles(t *testing.T, dirs ...string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		es, _ := os.ReadDir(d)
		for _, e := range es {
			out = append(out, filepath.Join(d, e.Name()))
		}
	}
	return out
}

// Audit Phase 1 H1: a missing root certificate, intermediate certificate or intermediate key is an error and
// creates nothing.
func TestLoadCAMissingFileFailsAndCreatesNothing(t *testing.T) {
	for _, missing := range []string{"rootCert", "intCert", "intKey"} {
		t.Run(missing, func(t *testing.T) {
			f := newCAFiles(t)
			f.init(t)
			victim := map[string]string{"rootCert": f.rootCert, "intCert": f.intCert, "intKey": f.intKey}[missing]
			if err := os.Remove(victim); err != nil {
				t.Fatal(err)
			}
			before := dirFiles(t, filepath.Dir(f.rootCert), filepath.Dir(f.rootKey))
			if _, err := LoadCA(f.rootCert, f.intCert, f.intKey, 1); err == nil {
				t.Fatal("LoadCA must fail when a CA file is missing")
			}
			after := dirFiles(t, filepath.Dir(f.rootCert), filepath.Dir(f.rootKey))
			if strings.Join(before, ",") != strings.Join(after, ",") {
				t.Fatalf("LoadCA created files: before %v after %v", before, after)
			}
		})
	}
}

func TestLoadCAAcceptsInitCA(t *testing.T) {
	f := newCAFiles(t)
	made := f.init(t)
	ca, err := LoadCA(f.rootCert, f.intCert, f.intKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ca.KeyFingerprint() != made.KeyFingerprint() || string(ca.RootPEM()) != string(made.RootPEM()) {
		t.Fatal("LoadCA does not load what InitCA wrote")
	}
	for _, p := range []string{f.rootKey, f.intKey} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want 0600", p, fi, err)
		}
	}
}

// Audit Phase 1 L16: an intermediate key from another CA, or an intermediate from another root, is refused.
func TestLoadCARefusesMismatchedMaterial(t *testing.T) {
	a, b := newCAFiles(t), newCAFiles(t)
	a.init(t)
	b.init(t)
	if _, err := LoadCA(a.rootCert, a.intCert, b.intKey, 1); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a foreign intermediate key must be refused, got %v", err)
	}
	if _, err := LoadCA(b.rootCert, a.intCert, a.intKey, 1); err == nil || !strings.Contains(err.Error(), "not signed by the root") {
		t.Fatalf("an intermediate of another root must be refused, got %v", err)
	}
}

func TestInitCARefusesWhenAnyFileExists(t *testing.T) {
	for i := 0; i < 4; i++ {
		f := newCAFiles(t)
		p := []string{f.rootKey, f.rootCert, f.intCert, f.intKey}[i]
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InitCA(f.rootKey, f.rootCert, f.intCert, f.intKey, 1); err == nil {
			t.Fatalf("InitCA must refuse when %s exists", p)
		}
		if b, _ := os.ReadFile(p); string(b) != "keep me" {
			t.Fatalf("InitCA touched %s", p)
		}
	}
}

func TestCheckChainRefusesExpired(t *testing.T) {
	f := newCAFiles(t)
	ca := f.init(t)
	root, _ := parseCertPEM(ca.RootPEM())
	if err := checkChain(root, ca.cert, ca.priv, time.Date(2041, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("an expired intermediate must be refused")
	}
}

// Audit Phase 1 L11: the CSR key is Ed25519 or ECDSA P-256/P-384.
func TestSignApplianceCSRKeyTypes(t *testing.T) {
	ca, _ := LoadOrCreate(filepath.Join(t.TempDir(), "ca.key"), 1)
	csrFor := func(key any) []byte {
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "a"}}, key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	}
	ok, _ := newCSR(t)
	if _, err := ca.SignApplianceCSR(ok, "a", "t", "s", time.Hour); err != nil {
		t.Fatalf("Ed25519: %v", err)
	}
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := ca.SignApplianceCSR(csrFor(p256), "a", "t", "s", time.Hour); err != nil {
		t.Fatalf("P-256: %v", err)
	}
	p224, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if _, err := ca.SignApplianceCSR(csrFor(p224), "a", "t", "s", time.Hour); err == nil {
		t.Fatal("P-224 must be refused")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	if _, err := ca.SignApplianceCSR(csrFor(rk), "a", "t", "s", time.Hour); err == nil {
		t.Fatal("RSA must be refused")
	}
}
