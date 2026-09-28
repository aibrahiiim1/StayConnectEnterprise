package appliancecert

// REISSUE IN CENTRAL MUST COMPLETE ON THE APPLIANCE BY ITSELF.
//
// Found on PRE-LIVE: an operator pressed "Reissue certificate" in Central. Central revoked the active
// certificate and waited for the appliance's next CSR. The appliance still had its certificate FILE, so it
// never re-entered the bootstrap: it kept presenting the revoked certificate, every mTLS call was refused
// (HTTP 403), the Admin Console said Central was unreachable, and Central showed "Activating" for good.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// reissueCentral is Central's certificate contract with a revocation: while revoked it reports "none"; a CSR
// is signed at once (the appliance is activated) with a NEW certificate.
type reissueCentral struct {
	mu        sync.Mutex
	status    string // issued | none | pending
	certPEM   string
	caPEM     string
	next      func() (string, string) // what a CSR is signed into
	csrs      atomic.Int32
	autoIssue bool
}

func (c *reissueCentral) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/appliance/csr", func(w http.ResponseWriter, r *http.Request) {
		c.csrs.Add(1)
		c.mu.Lock()
		if c.autoIssue && c.next != nil {
			c.certPEM, c.caPEM = c.next()
			c.status = "issued"
		} else {
			c.status = "pending"
		}
		c.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
	})
	mux.HandleFunc("/v1/appliance/certificate", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		out := map[string]any{"status": c.status, "ca_chain": c.caPEM}
		if c.status == "issued" {
			out["certificate_pem"] = c.certPEM
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}

func certifiedForReissue(t *testing.T) (*Manager, *reissueCentral, func()) {
	t.Helper()
	c := &reissueCentral{}
	srv := httptest.NewServer(c.handler())
	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	pub := m.mtlsPriv.Public().(ed25519.PublicKey)
	c.certPEM, c.caPEM = mintCertFor(t, pub)
	c.status = "issued"
	c.next = func() (string, string) { return mintCertFor(t, pub) }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("initial certificate: %v", err)
	}
	return m, c, srv.Close
}

func TestReconcileRepairsARevokedCertificateWithOneCSR(t *testing.T) {
	m, c, stop := certifiedForReissue(t)
	defer stop()
	old := m.currentFpr()

	// "Reissue certificate" in Central: revoked, nothing pending; the next CSR is signed at once.
	c.mu.Lock()
	c.status, c.autoIssue = "none", true
	c.mu.Unlock()

	out, err := m.Reconcile(context.Background())
	if err != nil || out != ReconcileReissued {
		t.Fatalf("want reissued, got %s %v", out, err)
	}
	if m.currentFpr() == old || m.currentFpr() == "" {
		t.Fatal("the new certificate must be installed")
	}
	if c.csrs.Load() != 1 {
		t.Fatalf("exactly one CSR, got %d", c.csrs.Load())
	}
	// Settled: nothing further happens.
	if out, _ := m.Reconcile(context.Background()); out != ReconcileCurrent || c.csrs.Load() != 1 {
		t.Fatalf("a current certificate needs nothing: %s csrs=%d", out, c.csrs.Load())
	}
}

func TestReconcileNeverFilesASecondRequestWhileOneIsPending(t *testing.T) {
	m, c, stop := certifiedForReissue(t)
	defer stop()
	c.mu.Lock()
	c.status, c.autoIssue = "none", false // Central records the CSR and waits
	c.mu.Unlock()
	if out, _ := m.Reconcile(context.Background()); out != ReconcileWaiting {
		t.Fatalf("want waiting, got %s", out)
	}
	for i := 0; i < 3; i++ {
		if out, _ := m.Reconcile(context.Background()); out != ReconcileWaiting {
			t.Fatalf("want waiting, got %s", out)
		}
	}
	if c.csrs.Load() != 1 {
		t.Fatalf("one outstanding request only, got %d", c.csrs.Load())
	}
}

func TestReconcileInstallsANewerCertificateCentralAlreadySigned(t *testing.T) {
	m, c, stop := certifiedForReissue(t)
	defer stop()
	old := m.currentFpr()
	c.mu.Lock()
	c.certPEM, c.caPEM = c.next() // reissued from a CSR that was waiting
	c.mu.Unlock()
	if out, err := m.Reconcile(context.Background()); err != nil || out != ReconcileInstalled {
		t.Fatalf("want installed, got %s %v", out, err)
	}
	if m.currentFpr() == old || c.csrs.Load() != 0 {
		t.Fatalf("installed without a CSR: csrs=%d", c.csrs.Load())
	}
}

func TestReconcileLeavesTheBootstrapAlone(t *testing.T) {
	c := &reissueCentral{status: "none"}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	m := newManagerFor(t, srv.URL)
	if out, err := m.Reconcile(context.Background()); err != nil || out != ReconcileNotYetReady || c.csrs.Load() != 0 {
		t.Fatalf("no installed certificate: the bootstrap owns this (%s %v csrs=%d)", out, err, c.csrs.Load())
	}
}
