package appliancecert

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCentral is Central's certificate contract only (POST /v1/appliance/csr records a request and returns pending; GET /v1/appliance/certificate reports
// issued, pending or none). It counts CSR submissions because SubmitCSR inserts a new row every call, so a loop that resubmits leaves duplicate requests.
type fakeCentral struct {
	csrSubmissions atomic.Int32
	certFetches    atomic.Int32
	issued         atomic.Bool // flipped when the "operator" activates the appliance
	certPEM        string
	caPEM          string
}

func (f *fakeCentral) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/appliance/csr", func(w http.ResponseWriter, r *http.Request) {
		f.csrSubmissions.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending", "request_id": "req-1"})
	})
	mux.HandleFunc("/v1/appliance/certificate", func(w http.ResponseWriter, r *http.Request) {
		n := f.certFetches.Add(1)
		_ = n
		w.Header().Set("Content-Type", "application/json")
		if f.issued.Load() {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "issued", "certificate_pem": f.certPEM, "ca_chain": f.caPEM,
			})
			return
		}
		status := "none"
		if f.csrSubmissions.Load() > 0 {
			status = "pending"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "ca_chain": f.caPEM})
	})
	return mux
}

// mintCertFor issues a self-signed CA and a leaf bound to the manager's mTLS public key, so the certificate pairs with the key the appliance kept.
func mintCertFor(t *testing.T, pub ed25519.PublicKey) (certPEM, caPEM string) {
	t.Helper()
	caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caPub, caPriv)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "appliance"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, pub, caPriv)
	if err != nil {
		t.Fatalf("leaf: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

func newManagerFor(t *testing.T, base string) *Manager {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return New(t.TempDir(), base, base, "appliance-1", priv)
}

// The real failure end to end: a fixed ten-minute window expired 59 seconds before an operator issued the certificate, the bootstrap goroutine had already
// returned, and the mTLS-only assignment, licence and convergence stayed stuck until a restart. Activation is a human action with no deadline.
func TestCertificateAcquiredAfterLongWaitWithoutRestart(t *testing.T) {
	central := &fakeCentral{}
	srv := httptest.NewServer(central.handler())
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatalf("mtls key: %v", err)
	}
	central.certPEM, central.caPEM = mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))

	// First attempt: nothing on file, so a CSR is submitted and the answer is "not yet".
	if err := m.Ensure(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("first attempt should report pending, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("expected exactly 1 CSR submission, got %d", got)
	}

	// Many more attempts while the operator has not pressed Activate, far beyond the old ten-minute window.
	for i := 0; i < 50; i++ {
		if err := m.Ensure(context.Background()); !errors.Is(err, ErrCertPending) {
			t.Fatalf("attempt %d should still be pending, got %v", i, err)
		}
	}
	// IDEMPOTENCE: Central still holds exactly one request. A loop that resubmitted would have left 51.
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("retries must not resubmit a CSR while one is pending; got %d submissions", got)
	}
	if m.Ready() {
		t.Fatal("mTLS must not be ready before a certificate is issued")
	}

	// The operator activates the appliance. No restart, no new CSR.
	central.issued.Store(true)

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("the certificate should be collected once issued, got %v", err)
	}
	if !m.Ready() {
		t.Fatal("mTLS must be ready once the certificate is installed")
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("collecting an issued certificate must not submit another CSR; got %d", got)
	}
}

// EnsureUntilInstalled returns only when the certificate is installed and must not resubmit a CSR while waiting.
func TestEnsureUntilInstalledSurvivesTheWait(t *testing.T) {
	central := &fakeCentral{}
	srv := httptest.NewServer(central.handler())
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatalf("mtls key: %v", err)
	}
	central.certPEM, central.caPEM = mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- m.EnsureUntilInstalled(ctx) }()

	// Let it establish that it is waiting, then "activate" from the other side.
	waitUntil(t, 20*time.Second, func() bool { return central.csrSubmissions.Load() == 1 })
	waitUntil(t, 30*time.Second, func() bool { return central.certFetches.Load() >= 2 })
	central.issued.Store(true)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureUntilInstalled should succeed once issued, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("EnsureUntilInstalled never completed after the certificate was issued")
	}
	if !m.Ready() {
		t.Fatal("mTLS must be ready")
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("the retry loop must not resubmit CSRs; got %d submissions", got)
	}
}

// Shutdown must end the loop; a service stopping is not something to retry through.
func TestEnsureUntilInstalledStopsOnShutdown(t *testing.T) {
	central := &fakeCentral{}
	srv := httptest.NewServer(central.handler())
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.EnsureUntilInstalled(ctx) }()
	waitUntil(t, 20*time.Second, func() bool { return central.csrSubmissions.Load() == 1 })
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled on shutdown, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("EnsureUntilInstalled did not stop when the context was cancelled")
	}
}

// An appliance whose certificate already exists locally must not talk to Central at all.
func TestExistingLocalCertificateNeedsNoCSR(t *testing.T) {
	central := &fakeCentral{}
	srv := httptest.NewServer(central.handler())
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatalf("mtls key: %v", err)
	}
	certPEM, caPEM := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM, central.caPEM = certPEM, caPEM
	central.issued.Store(true)

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	subs, fetches := central.csrSubmissions.Load(), central.certFetches.Load()

	// A fresh manager over the same directory loads from disk and contacts nobody.
	m2 := New(m.dir, srv.URL, srv.URL, "appliance-1", m.priv)
	if err := m2.Ensure(context.Background()); err != nil {
		t.Fatalf("second manager should load the local certificate: %v", err)
	}
	if !m2.Ready() {
		t.Fatal("second manager should be mTLS-ready from disk")
	}
	if central.csrSubmissions.Load() != subs || central.certFetches.Load() != fetches {
		t.Fatal("an appliance with a local certificate must not call Central during Ensure")
	}
}

func waitUntil(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("condition not met within " + limit.String())
}
