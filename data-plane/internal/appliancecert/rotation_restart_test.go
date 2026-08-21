package appliancecert

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// restart returns a NEW Manager over the same certificate directory — the process-restart boundary.
//
// This is the distinction that matters. Repeated calls on one Manager only prove the in-memory guard works;
// scd restarts on assignment changes, deploys, crashes and reboots, and a guard that lives in a process
// variable is gone every time. Everything below goes through a fresh Manager on purpose.
func restart(t *testing.T, old *Manager, base string) *Manager {
	t.Helper()
	m := New(old.dir, base, base, old.applianceID, old.priv)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("restarted manager should load its certificate from disk: %v", err)
	}
	if !m.Ready() {
		t.Fatal("restarted manager should be mTLS-ready from disk")
	}
	return m
}

// certifiedManager returns a Manager holding a current certificate, plus the fake Central behind it.
func certifiedManager(t *testing.T) (*Manager, *statusCentral, func()) {
	t.Helper()
	central, srv := newStatusCentral(statusIssued)
	m := newManagerFor(t, srv.URL)
	if err := m.ensureMTLSKey(); err != nil {
		srv.Close()
		t.Fatalf("mtls key: %v", err)
	}
	cert, ca := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.caPEM = ca
	central.certPEM.Store(cert)
	if err := m.Ensure(context.Background()); err != nil {
		srv.Close()
		t.Fatalf("initial install: %v", err)
	}
	if central.csrSubmissions.Load() != 0 {
		srv.Close()
		t.Fatal("installing an already-issued certificate must not submit a CSR")
	}
	return m, central, srv.Close
}

func ageRotationMarker(t *testing.T, m *Manager, by time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(m.rotationMarkerPath())
	if err != nil {
		t.Fatalf("expected a rotation marker on disk: %v", err)
	}
	var mk rotationMarker
	if err := json.Unmarshal(raw, &mk); err != nil {
		t.Fatalf("marker unreadable: %v", err)
	}
	mk.SubmittedAt = mk.SubmittedAt.Add(-by)
	b, _ := json.Marshal(mk)
	if err := writeAtomic(m.rotationMarkerPath(), b, 0o644); err != nil {
		t.Fatalf("rewrite marker: %v", err)
	}
}

// THE GAP THIS CLOSES.
//
// A rotation CSR is filed and Central holds it pending. The appliance then restarts — a deploy, a reboot,
// an assignment change re-execing scd. Nothing about Central changed, so filing another request would be
// pure duplication, and over a fourteen-day rotation window a restart-happy appliance produces exactly the
// pile the suppression exists to prevent.
func TestPendingRotationSurvivesProcessRestart(t *testing.T) {
	m, central, done := certifiedManager(t)
	defer done()
	base := m.ctrlBase

	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("first rotation should report pending, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("first rotation should file exactly one CSR, got %d", got)
	}

	// Five restarts, each followed by a rotation check.
	for i := 0; i < 5; i++ {
		m = restart(t, m, base)
		if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
			t.Fatalf("rotation after restart %d should still be pending, got %v", i, err)
		}
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("restarts must not file duplicate rotation CSRs; got %d", got)
	}
}

// Once the new certificate is installed the marker must be gone, or the NEXT rotation — potentially years
// later — would be suppressed by a stale file and the certificate would expire.
func TestRotationMarkerClearsAfterSuccessfulInstall(t *testing.T) {
	m, central, done := certifiedManager(t)
	defer done()
	base := m.ctrlBase

	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	if _, err := os.Stat(m.rotationMarkerPath()); err != nil {
		t.Fatalf("a pending rotation should leave a marker: %v", err)
	}

	// Central issues the replacement; a restarted process collects it.
	newCert, _ := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM.Store(newCert)
	m = restart(t, m, base)
	if err := m.rotateCert(context.Background()); err != nil {
		t.Fatalf("rotation should complete after restart, got %v", err)
	}
	if _, err := os.Stat(m.rotationMarkerPath()); !os.IsNotExist(err) {
		t.Fatal("the marker must be removed once the new certificate is installed")
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("completing a rotation must not file another CSR; got %d", got)
	}

	// A later rotation is free to proceed — nothing stale is suppressing it.
	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("a subsequent rotation should file a fresh CSR, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 2 {
		t.Fatalf("the next rotation should file exactly one more CSR; got %d", got)
	}
}

// The marker names the certificate it was filed against, so it invalidates itself.
//
// The case this covers: a rotation is filed, the process restarts, and at startup a plain Ensure() collects
// the already-issued replacement before any rotation check runs. The marker now describes a rotation that
// is finished. Without the fingerprint it would go on suppressing rotations until it aged out.
func TestRotationMarkerSelfInvalidatesWhenCertificateChanged(t *testing.T) {
	m, central, done := certifiedManager(t)
	defer done()
	base := m.ctrlBase

	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("expected pending, got %v", err)
	}

	// The replacement lands and is picked up by ordinary startup, not by rotateCert.
	newCert, newCA := mintCertFor(t, m.mtlsPriv.Public().(ed25519.PublicKey))
	central.certPEM.Store(newCert)
	if err := m.store(context.Background(), []byte(newCert), []byte(newCA)); err != nil {
		t.Fatalf("install replacement: %v", err)
	}

	m = restart(t, m, base)
	if got := m.loadRotationPending(m.fpr); !got.IsZero() {
		t.Fatal("a marker for a certificate that is no longer installed must not suppress rotation")
	}
	if _, err := os.Stat(m.rotationMarkerPath()); !os.IsNotExist(err) {
		t.Fatal("the stale marker should have been removed")
	}
}

// A corrupt marker must not be able to block rotation until the certificate expires. One duplicate CSR is
// the lesser failure by a wide margin.
func TestCorruptRotationMarkerDoesNotBlockRotation(t *testing.T) {
	m, central, done := certifiedManager(t)
	defer done()

	if err := writeAtomic(m.rotationMarkerPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt marker: %v", err)
	}
	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	if got := central.csrSubmissions.Load(); got != 1 {
		t.Fatalf("a corrupt marker should be discarded and rotation proceed; got %d CSRs", got)
	}
}

// The marker is bookkeeping, not key material.
func TestRotationMarkerHoldsNoSensitiveMaterial(t *testing.T) {
	m, _, done := certifiedManager(t)
	defer done()

	if err := m.rotateCert(context.Background()); !errors.Is(err, ErrCertPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	raw, err := os.ReadFile(m.rotationMarkerPath())
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	for _, forbidden := range []string{"PRIVATE KEY", "BEGIN CERTIFICATE", "BEGIN RSA", "BEGIN EC"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the rotation marker must not contain %q; got %s", forbidden, raw)
		}
	}
	var mk rotationMarker
	if err := json.Unmarshal(raw, &mk); err != nil {
		t.Fatalf("marker should be plain JSON: %v", err)
	}
	if mk.SubmittedAt.IsZero() || mk.ForFingerprint == "" {
		t.Fatalf("marker should carry a timestamp and the fingerprint it was filed against; got %+v", mk)
	}

	// The private key on disk is untouched by any of this.
	info, err := os.Stat(m.mtlsKeyPath())
	if err != nil {
		t.Fatalf("mtls key should still exist: %v", err)
	}
	// Windows has no Unix mode bits — chmod(0600) there only clears the read-only flag and Stat reports
	// 0666 — so the permission itself is only assertable where the appliance actually runs.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("the mTLS private key must remain 0600, got %o", perm)
		}
	}
	// Everywhere: the marker must not have become a second copy of the key.
	keyRaw, err := os.ReadFile(m.mtlsKeyPath())
	if err != nil {
		t.Fatalf("read mtls key: %v", err)
	}
	if strings.Contains(string(raw), string(keyRaw)) {
		t.Fatal("the rotation marker must not contain the private key")
	}
}
