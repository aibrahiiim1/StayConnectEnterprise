package appliancecert

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// statusCentral answers /v1/appliance/certificate with whatever status the test sets and counts CSRs; "none" is the only status that authorises a CSR.
type statusCentral struct {
	status         atomic.Value // string
	csrSubmissions atomic.Int32
	certPEM        atomic.Value // string
	caPEM          string
	helloFailures  atomic.Int32 // the next N mTLS hellos fail
	hellos         atomic.Int32
}

func (c *statusCentral) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/appliance/hello", func(w http.ResponseWriter, r *http.Request) {
		c.hellos.Add(1)
		if c.helloFailures.Load() > 0 {
			c.helloFailures.Add(-1)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/v1/appliance/csr", func(w http.ResponseWriter, r *http.Request) {
		c.csrSubmissions.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
	})
	mux.HandleFunc("/v1/appliance/certificate", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"status": c.status.Load().(string), "ca_chain": c.caPEM}
		if pem, _ := c.certPEM.Load().(string); pem != "" {
			body["certificate_pem"] = pem
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	return mux
}

func newStatusCentral(status string) (*statusCentral, *httptest.Server) {
	c := &statusCentral{}
	c.status.Store(status)
	c.certPEM.Store("")
	srv := httptest.NewServer(c.handler())
	return c, srv
}

// A status the client does not recognise must NEVER be read as "none": it is the one state that authorises posting a CSR, and guessing would file
// duplicate requests across the fleet.
func TestUnknownStatusNeverSubmitsACSR(t *testing.T) {
	for _, status := range []string{"awaiting_review", "quarantined", "", "ISSUED", "Pending", "unknown"} {
		t.Run("status="+status, func(t *testing.T) {
			central, srv := newStatusCentral(status)
			defer srv.Close()
			m := newManagerFor(t, srv.URL)

			err := m.Ensure(context.Background())
			if !errors.Is(err, ErrCertUnknownState) {
				t.Fatalf("status %q should fail closed as unknown, got %v", status, err)
			}
			if got := central.csrSubmissions.Load(); got != 0 {
				t.Fatalf("status %q must not trigger a CSR; got %d submissions", status, got)
			}
			if m.Ready() {
				t.Fatal("mTLS must not become ready on an unrecognised status")
			}
		})
	}
}

// Case matters: the contract is lower-case, so "ISSUED" must not be accepted as issued and must never install anything.
func TestUnknownStatusIsNotTerminalSoTheFleetRecovers(t *testing.T) {
	central, srv := newStatusCentral("something_new")
	defer srv.Close()
	m := newManagerFor(t, srv.URL)

	err := m.Ensure(context.Background())
	if !errors.Is(err, ErrCertUnknownState) {
		t.Fatalf("expected unknown-state error, got %v", err)
	}
	// Deliberately NOT terminal: one new Central status must not stop the certificate lifecycle on every appliance at once.
	if errors.Is(err, ErrCertTerminal) {
		t.Fatal("an unrecognised status must not be terminal — the appliance has to be able to recover")
	}

	// And it does recover, with no restart and no CSR, the moment Central says something it understands.
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatalf("mtls key: %v", err)
	}
	certPEM, caPEM := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.caPEM = caPEM
	central.certPEM.Store(certPEM)
	central.status.Store(statusIssued)

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("should recover once the status is understood, got %v", err)
	}
	if !m.Ready() {
		t.Fatal("mTLS should be ready after recovery")
	}
	if got := central.csrSubmissions.Load(); got != 0 {
		t.Fatalf("recovery must not have submitted a CSR; got %d", got)
	}
}

// A rejected or revoked request cannot be advanced by retrying, so the loop must stop and say why.
func TestTerminalStatusStopsTheBootstrap(t *testing.T) {
	for status, want := range terminalStatuses {
		t.Run("status="+status, func(t *testing.T) {
			central, srv := newStatusCentral(status)
			defer srv.Close()
			m := newManagerFor(t, srv.URL)

			err := m.Ensure(context.Background())
			if !errors.Is(err, ErrCertTerminal) {
				t.Fatalf("status %q should be terminal, got %v", status, err)
			}
			// The reason has to reach the operator, not just a category.
			if !contains(err.Error(), want) || !contains(err.Error(), status) {
				t.Fatalf("error should name the reason %q and the status %q; got %q", want, status, err)
			}
			if got := central.csrSubmissions.Load(); got != 0 {
				t.Fatalf("a terminal status must not trigger a CSR; got %d", got)
			}

			// EnsureUntilInstalled must return rather than retry.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- m.EnsureUntilInstalled(ctx) }()
			select {
			case got := <-done:
				if !errors.Is(got, ErrCertTerminal) {
					t.Fatalf("EnsureUntilInstalled should surface the terminal error, got %v", got)
				}
			case <-ctx.Done():
				t.Fatal("EnsureUntilInstalled kept retrying a terminal state instead of stopping")
			}
		})
	}
}

// "none" is the only status that authorises a CSR.
func TestOnlyNoneSubmitsACSR(t *testing.T) {
	central, srv := newStatusCentral(statusNone)
	defer srv.Close()
	m := newManagerFor(t, srv.URL)

	if err := m.Ensure(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("none should submit a CSR and report pending, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("expected exactly 1 CSR submission, got %d", got)
	}
}

// Rotation idempotence: MaybeRotate runs every six hours through the last fourteen days, and a suspended, revoked or decommissioned appliance is not
// auto-issued, so unconditional submits would file ~56 duplicate requests.
func TestPendingRotationDoesNotFileDuplicateRequests(t *testing.T) {
	central, srv := newStatusCentral(statusIssued)
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatalf("mtls key: %v", err)
	}
	oldCert, caPEM := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.caPEM = caPEM
	central.certPEM.Store(oldCert)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	if central.csrSubmissions.Load() != 0 {
		t.Fatal("installing an already-issued certificate should not submit a CSR")
	}

	// Rotation begins; Central keeps returning the SAME certificate (request filed, not issued), as for a suspended appliance.
	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("first rotation should report pending, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("first rotation should file exactly one CSR, got %d", got)
	}

	// Fifty-six more ticks — a fortnight of six-hourly checks.
	for i := 0; i < 56; i++ {
		if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
			t.Fatalf("tick %d should still be pending, got %v", i, err)
		}
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("a pending rotation must not be resubmitted; got %d CSRs", got)
	}

	newCert, _ := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM.Store(newCert)
	if err := m.rotateCert(context.Background()); err != nil {
		t.Fatalf("rotation should complete once issued, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("collecting the rotated certificate must not submit another CSR; got %d", got)
	}
	if fprOfPEM(newCert) != m.fpr {
		t.Fatal("the newly issued certificate should be the installed one")
	}
}

// A rotation outstanding longer than the resubmit window may be filed again, so a CSR lost in transit cannot stall rotation until expiry.
func TestStaleRotationMayBeResubmitted(t *testing.T) {
	central, srv := newStatusCentral(statusIssued)
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatalf("mtls key: %v", err)
	}
	oldCert, caPEM := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.caPEM = caPEM
	central.certPEM.Store(oldCert)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("initial install: %v", err)
	}

	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	if central.csrSubmissions.Load() != 1 {
		t.Fatalf("expected 1 CSR, got %d", central.csrSubmissions.Load())
	}

	// Age the outstanding request past the window, on disk where it actually lives.
	ageRotationMarker(t, m, rotationResubmitAfter+time.Minute)

	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 2 {
		t.Fatalf("a stale rotation request should be filed once more; got %d CSRs", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
