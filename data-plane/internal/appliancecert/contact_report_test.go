package appliancecert

import (
	"context"
	"crypto/ed25519"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The attempt observer reports ONLY exchanges with Central. A certificate loaded from disk at restart is not one: scd
// feeds the observer into "last contact with Central", which must not move to the restart time while the network is cut.
func TestAttemptObserverIgnoresACertificateLoadedFromDisk(t *testing.T) {
	central := &fakeCentral{}
	srv := httptest.NewServer(central.handler())
	defer srv.Close()

	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	certPEM, caPEM := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM, central.caPEM = certPEM, caPEM
	central.issued.Store(true)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("first acquisition: %v", err)
	}

	// "Restart": a fresh manager over the same directory, with Central unreachable.
	srv.Close()
	m2 := New(m.dir, srv.URL, srv.URL, "appliance-1", m.priv)
	var observed atomic.Int32
	m2.OnAttempt(func(error) { observed.Add(1) })
	if err := m2.EnsureUntilInstalled(context.Background()); err != nil {
		t.Fatalf("a certificate on disk must load: %v", err)
	}
	if got := observed.Load(); got != 0 {
		t.Fatalf("a certificate loaded from disk was reported to the contact observer %d time(s): that is not an exchange with Central", got)
	}
}

// The attempt that really asks Central is still reported, success or not.
func TestAttemptObserverReportsARealExchange(t *testing.T) {
	central := &fakeCentral{}
	srv := httptest.NewServer(central.handler())
	defer srv.Close()
	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	certPEM, caPEM := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM, central.caPEM = certPEM, caPEM
	central.issued.Store(true)
	var observed atomic.Int32
	m.OnAttempt(func(error) { observed.Add(1) })
	if err := m.EnsureUntilInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observed.Load() != 1 {
		t.Fatalf("an attempt that asked Central must be reported once, got %d", observed.Load())
	}
}
