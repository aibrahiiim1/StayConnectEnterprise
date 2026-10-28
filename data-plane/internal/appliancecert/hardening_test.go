package appliancecert

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// P2 M5: an unreadable key file is an error and is never replaced by a new key.
func TestUnreadableMTLSKeyIsNotRegenerated(t *testing.T) {
	m := newManagerFor(t, "http://127.0.0.1:1")
	if err := os.MkdirAll(m.mtlsKeyPath(), 0o700); err != nil { // a directory: ReadFile fails with something other than ErrNotExist
		t.Fatal(err)
	}
	if err := m.ensureMTLSKey(); err == nil {
		t.Fatal("an unreadable key must be an error")
	}
	fi, err := os.Stat(m.mtlsKeyPath())
	if err != nil || !fi.IsDir() {
		t.Fatal("the unreadable key path must be left unchanged")
	}
	if m.mtlsPriv != nil {
		t.Fatal("no key may be generated over an unreadable key file")
	}
}

// P2 M7: an ingress call to a Central that never answers returns within the client timeout.
func TestIngressCallTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	old := ingressClient
	ingressClient = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { ingressClient = old }()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, _, err := m.collectIngress(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled ingress call must fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ingress call did not time out")
	}
	if _, cl := m.apiBase(); cl == http.DefaultClient {
		t.Fatal("the bootstrap must not use http.DefaultClient (no timeout)")
	}
}

// P2 L20: error bodies are capped in the error text.
func TestErrorBodyIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer srv.Close()
	m := newManagerFor(t, srv.URL)
	_, err := m.signedDoWith(context.Background(), ingressClient, srv.URL, http.MethodGet, "/v1/appliance/certificate", nil, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if n := strings.Count(err.Error(), "x"); n > maxErrorBody {
		t.Fatalf("error carries %d body bytes, want at most %d", n, maxErrorBody)
	}
}

// P2 M9: a rotated certificate whose hello fails is never switched to, the marker stays, and the next tick installs it with no new CSR.
func TestRotationVerifiesBeforeSwitching(t *testing.T) {
	m, central, done := certifiedManager(t)
	defer done()
	oldFpr := m.currentFpr()
	oldCert, _ := os.ReadFile(m.certPath())

	newCert, _ := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM.Store(newCert)
	central.helloFailures.Store(1)
	if err := m.rotateCert(context.Background()); err == nil {
		t.Fatal("rotation with a failing hello must report an error")
	}
	if m.currentFpr() != oldFpr {
		t.Fatal("a certificate that failed its hello must not be switched to")
	}
	if onDisk, _ := os.ReadFile(m.certPath()); !bytes.Equal(onDisk, oldCert) {
		t.Fatal("a certificate that failed its hello must not be written")
	}
	if _, err := os.Stat(m.rotationMarkerPath()); err != nil {
		t.Fatalf("the pending marker must stay after a failed hello: %v", err)
	}
	if err := m.rotateCert(context.Background()); err != nil {
		t.Fatalf("second tick should install the verified certificate: %v", err)
	}
	if m.currentFpr() == oldFpr {
		t.Fatal("the new certificate should be installed on the second tick")
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("a flaky hello must cost one CSR in total, got %d", got)
	}
}

// P2 M15 (certificate part): a CA bundle that does not parse leaves the installed pair untouched.
func TestBadCABundleLeavesInstalledPair(t *testing.T) {
	m, _, done := certifiedManager(t)
	defer done()
	oldCert, _ := os.ReadFile(m.certPath())
	oldCA, _ := os.ReadFile(m.caPath())
	newCert, _ := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	if err := m.store(context.Background(), []byte(newCert), []byte("not a pem")); err == nil {
		t.Fatal("store with a bad CA must fail")
	}
	cert, _ := os.ReadFile(m.certPath())
	ca, _ := os.ReadFile(m.caPath())
	if !bytes.Equal(cert, oldCert) || !bytes.Equal(ca, oldCA) {
		t.Fatal("a failed store must leave the previous pair on disk")
	}
	if err := New(m.dir, m.ctrlBase, m.mtlsBase, m.applianceID, m.priv).loadLocal(); err != nil {
		t.Fatalf("the pair on disk must still load: %v", errors.Unwrap(err))
	}
}
