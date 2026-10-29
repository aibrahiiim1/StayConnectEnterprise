package appliancecert

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"testing"
)

// AD-2: with a provisioned pin, a bundle from another CA is refused and the installed pair is untouched.
func TestProvisionedPinRefusesAnotherCA(t *testing.T) {
	m := newManagerFor(t, "http://127.0.0.1:1")
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	pub := m.mtlsPriv.Public().(ed25519.PublicKey)
	certPEM, caPEM := mintCertFor(t, pub)
	if err := os.WriteFile(m.pinFile(), []byte(caPEM), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.store(context.Background(), []byte(certPEM), []byte(caPEM)); err != nil {
		t.Fatalf("a bundle that chains to the pin installs: %v", err)
	}
	oldCert, _ := os.ReadFile(m.certPath())
	oldCA, _ := os.ReadFile(m.caPath())

	evilCert, evilCA := mintCertUnderNewCA(t, pub)
	err := m.store(context.Background(), []byte(evilCert), []byte(evilCA))
	if !errors.Is(err, ErrCAChangedFromPin) {
		t.Fatalf("another CA must be refused with ErrCAChangedFromPin, got %v", err)
	}
	// A leaf from the pinned CA presented with the hostile root is refused too: the root itself is not the pin.
	goodLeaf, _ := mintCertFor(t, pub)
	if err := m.store(context.Background(), []byte(goodLeaf), []byte(evilCA)); !errors.Is(err, ErrCAChangedFromPin) {
		t.Fatalf("a hostile root beside a good leaf must be refused, got %v", err)
	}
	cert, _ := os.ReadFile(m.certPath())
	ca, _ := os.ReadFile(m.caPath())
	if !bytes.Equal(cert, oldCert) || !bytes.Equal(ca, oldCA) {
		t.Fatal("a refused bundle must leave the installed pair on disk")
	}
	pinned, _ := os.ReadFile(m.pinFile())
	if string(pinned) != caPEM {
		t.Fatal("the pin must not change")
	}
}

// AD-2: an appliance with no pin (provisioned before this existed) adopts the roots of the first bundle it installs,
// and from then on refuses any other.
func TestUnprovisionedApplianceAdoptsThenPins(t *testing.T) {
	m := newManagerFor(t, "http://127.0.0.1:1")
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	pub := m.mtlsPriv.Public().(ed25519.PublicKey)
	if _, err := os.Stat(m.pinFile()); err == nil {
		t.Fatal("no pin must exist before the first install")
	}
	certPEM, caPEM := mintCertFor(t, pub)
	if err := m.store(context.Background(), []byte(certPEM), []byte(caPEM)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(m.pinFile()); err != nil || string(b) != caPEM {
		t.Fatalf("the first bundle's root must become the pin: %v", err)
	}
	evilCert, evilCA := mintCertUnderNewCA(t, pub)
	if err := m.store(context.Background(), []byte(evilCert), []byte(evilCA)); !errors.Is(err, ErrCAChangedFromPin) {
		t.Fatalf("a different CA after adoption must be refused, got %v", err)
	}
}

// AD-2: an appliance that already holds a CA bundle but no pin (the PRE-LIVE case) pins that bundle at boot, without
// contacting Central and without refusing its own installed pair.
func TestInstalledBundleIsPinnedAtBoot(t *testing.T) {
	m := newManagerFor(t, "http://127.0.0.1:1")
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	pub := m.mtlsPriv.Public().(ed25519.PublicKey)
	certPEM, caPEM := mintCertFor(t, pub)
	if err := m.store(context.Background(), []byte(certPEM), []byte(caPEM)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.pinFile()); err != nil {
		t.Fatal(err)
	}
	m2 := New(m.dir, m.ctrlBase, m.mtlsBase, m.applianceID, m.priv)
	if err := m2.loadLocal(); err != nil {
		t.Fatalf("the installed pair must still load: %v", err)
	}
	if b, err := os.ReadFile(m2.pinFile()); err != nil || string(b) != caPEM {
		t.Fatalf("boot must pin the installed bundle's root: %v", err)
	}
}

// AD-2: a provisioned pin that the installed bundle does not chain to makes boot refuse mTLS rather than trust it.
func TestInstalledBundleThatContradictsTheProvisionedPinIsNotTrusted(t *testing.T) {
	m := newManagerFor(t, "http://127.0.0.1:1")
	if err := m.ensureMTLSKey(); err != nil {
		t.Fatal(err)
	}
	pub := m.mtlsPriv.Public().(ed25519.PublicKey)
	certPEM, caPEM := mintCertFor(t, pub)
	if err := m.store(context.Background(), []byte(certPEM), []byte(caPEM)); err != nil {
		t.Fatal(err)
	}
	_, otherCA := mintCertUnderNewCA(t, pub)
	if err := os.WriteFile(m.pinFile(), []byte(otherCA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New(m.dir, m.ctrlBase, m.mtlsBase, m.applianceID, m.priv).loadLocal(); !errors.Is(err, ErrCAChangedFromPin) {
		t.Fatalf("an installed bundle outside the pin must not load, got %v", err)
	}
}
