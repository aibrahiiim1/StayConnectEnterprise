package appliancecert

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

// foreignCentral reproduces finding F1: Central holds an active certificate signed from an EARLIER installation's CSR (a key
// this appliance does not hold). When signOnSubmit is set it behaves like an activated appliance's Central and signs the
// submitted CSR on arrival; otherwise the request just waits.
type foreignCentral struct {
	t              *testing.T
	mu             sync.Mutex
	certPEM, caPEM string
	signOnSubmit   bool
	csrSubmissions atomic.Int32
}

func (c *foreignCentral) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/appliance/csr", func(w http.ResponseWriter, r *http.Request) {
		c.csrSubmissions.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			CSRPem string `json:"csr_pem"`
		}
		_ = json.Unmarshal(raw, &in)
		block, _ := pem.Decode([]byte(in.CSRPem))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			c.t.Errorf("bad CSR: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.signOnSubmit {
			c.certPEM, c.caPEM = mintCertFor(c.t, csr.PublicKey.(ed25519.PublicKey))
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/v1/appliance/certificate", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": statusIssued, "certificate_pem": c.certPEM, "ca_chain": c.caPEM})
	})
	return mux
}

func newForeignCentral(t *testing.T, signOnSubmit bool) (*foreignCentral, *httptest.Server) {
	otherInstall, _, _ := ed25519.GenerateKey(rand.Reader)
	c := &foreignCentral{t: t, signOnSubmit: signOnSubmit}
	c.certPEM, c.caPEM = mintCertFor(t, otherInstall)
	srv := httptest.NewServer(c.handler())
	t.Cleanup(srv.Close)
	return c, srv
}

func (c *foreignCentral) issued() string { c.mu.Lock(); defer c.mu.Unlock(); return c.certPEM }

// F1, activated appliance: the certificate Central signed belongs to an earlier installation. It is never written, the appliance
// files ONE request for its own key, and installs the certificate Central signs for it — no operator action.
func TestForeignCertificateIsRefusedAndReplacedWithOwn(t *testing.T) {
	central, srv := newForeignCentral(t, true)
	m := newManagerFor(t, srv.URL)
	foreign := fprOfPEM(central.issued())

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("bootstrap should converge on the appliance's own certificate, got %v", err)
	}
	if !m.Ready() {
		t.Fatal("mTLS must be ready")
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("exactly one CSR for the local key, got %d", got)
	}
	if m.currentFpr() == foreign {
		t.Fatal("the foreign certificate was installed")
	}
	disk, err := os.ReadFile(m.certPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.matchesLocalKey(disk); err != nil {
		t.Fatalf("the certificate on disk must carry the local key: %v", err)
	}
	if _, err := os.Stat(m.rotationMarkerPath()); !os.IsNotExist(err) {
		t.Fatal("the request marker must be cleared once the own certificate is installed")
	}
}

// F1, Central cannot sign yet: the appliance waits (not terminal), writes nothing, and does not flood Central with requests.
func TestForeignCertificateWaitsWithoutFlooding(t *testing.T) {
	central, srv := newForeignCentral(t, false)
	m := newManagerFor(t, srv.URL)

	for i := 0; i < 5; i++ {
		err := m.Ensure(context.Background())
		if !errors.Is(err, ErrCertPending) || errors.Is(err, ErrCertTerminal) {
			t.Fatalf("attempt %d: want a waiting (pending) error, got %v", i, err)
		}
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("one request per foreign certificate per resubmit window, got %d", got)
	}
	if _, err := os.Stat(m.certPath()); !os.IsNotExist(err) {
		t.Fatal("a certificate for another key must never be written to disk")
	}
	if m.Ready() {
		t.Fatal("mTLS must not become ready on a foreign certificate")
	}
}

// Reconcile on an installed appliance: Central now names a certificate for another key. It is not installed over the working
// one; the appliance asks for its own and installs that.
func TestReconcileRefusesForeignCertificate(t *testing.T) {
	central, srv := newForeignCentral(t, true)
	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	own, ca := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	if err := m.store(context.Background(), []byte(own), []byte(ca)); err != nil {
		t.Fatalf("own certificate must install: %v", err)
	}
	before := m.currentFpr()

	out, err := m.Reconcile(context.Background())
	if err != nil || out != ReconcileReissued {
		t.Fatalf("want reissued, got %q %v", out, err)
	}
	if m.currentFpr() == before || m.currentFpr() == "" {
		t.Fatal("a new certificate for the local key should be installed")
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("exactly one CSR, got %d", got)
	}
}

// store is the single writer: a certificate for another key is refused before the file is touched.
func TestStoreRefusesForeignKey(t *testing.T) {
	m := newManagerFor(t, "http://unused")
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	certPEM, caPEM := mintCertFor(t, other)
	if err := m.store(context.Background(), []byte(certPEM), []byte(caPEM)); !errors.Is(err, ErrCertKeyMismatch) {
		t.Fatalf("want ErrCertKeyMismatch, got %v", err)
	}
	if _, err := os.Stat(m.certPath()); !os.IsNotExist(err) {
		t.Fatal("nothing may be written for a foreign certificate")
	}
}
