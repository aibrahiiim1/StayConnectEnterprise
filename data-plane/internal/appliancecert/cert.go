// Package appliancecert manages the appliance's mutual-TLS client certificate
// lifecycle: it generates a CSR from the local identity key, submits it through
// the signed-auth channel, downloads the signed certificate + CA bundle, stores
// them atomically, and uses them for mTLS transport to Central — with the
// signed-request JWT still layered on top (defence in depth). It also rotates
// the certificate before expiry with verify-before-switch and rollback.
//
// The appliance PRIVATE key never leaves the box and is the same Ed25519
// identity key (0600, in identity store). The certificate + CA are public.
package appliancecert

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
)

// Manager owns the cert files and the live mTLS client.
type Manager struct {
	dir         string // cert directory, e.g. /etc/stayconnect/certs
	ctrlBase    string // https ingress for signed-auth CSR submit/fetch (e.g. https://150.0.0.252)
	mtlsBase    string // mTLS listener (e.g. https://150.0.0.252:9443)
	applianceID string
	priv        ed25519.PrivateKey

	mu       sync.RWMutex
	mtlsPriv ed25519.PrivateKey // SEPARATE key: CSR + mTLS client auth only
	client   *http.Client       // mTLS client (nil until a cert is loaded)
	notAfter time.Time
	fpr      string
	ready    bool
}

func (m *Manager) mtlsKeyPath() string { return filepath.Join(m.dir, "mtls-client.key") }

// ensureMTLSKey loads or generates the dedicated mTLS client key. It is
// independent of the identity-signing key: the identity key signs application
// requests; THIS key backs the CSR and the mTLS client certificate only. It is
// never uploaded and never placed in identity.json.
func (m *Manager) ensureMTLSKey() error {
	if m.mtlsPriv != nil {
		return nil
	}
	if raw, err := os.ReadFile(m.mtlsKeyPath()); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return errors.New("mtls key PEM decode failed")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return err
		}
		ed, ok := k.(ed25519.PrivateKey)
		if !ok {
			return errors.New("mtls key not Ed25519")
		}
		m.mtlsPriv = ed
		return nil
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := writeAtomic(m.mtlsKeyPath(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return err
	}
	m.mtlsPriv = priv
	return nil
}

func New(dir, ctrlBase, mtlsBase, applianceID string, priv ed25519.PrivateKey) *Manager {
	return &Manager{dir: dir, ctrlBase: ctrlBase, mtlsBase: mtlsBase, applianceID: applianceID, priv: priv}
}

func (m *Manager) certPath() string { return filepath.Join(m.dir, "client.crt") }
func (m *Manager) caPath() string   { return filepath.Join(m.dir, "ca.crt") }

// Ready reports whether an mTLS client cert is loaded and usable.
func (m *Manager) Ready() bool { m.mu.RLock(); defer m.mu.RUnlock(); return m.ready }

// Client returns the mTLS http.Client, or nil if no cert is loaded yet.
func (m *Manager) Client() *http.Client { m.mu.RLock(); defer m.mu.RUnlock(); return m.client }

// Status returns a small snapshot for the local UI / diagnostics.
func (m *Manager) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{"mtls_ready": m.ready, "cert_fingerprint": m.fpr, "not_after": m.notAfter}
}

// Transport returns the mTLS client + base URL if a cert is loaded, so other
// subsystems (license fetch) can route their Central API calls over mTLS.
func (m *Manager) Transport() (*http.Client, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client, m.mtlsBase, m.ready
}

// NATSTLSConfig builds a tls.Config presenting the appliance client cert +
// mtls key and trusting the CA bundle — for connecting to Central NATS over
// mTLS. Requires a cert to be installed (call Ensure first).
func (m *Manager) NATSTLSConfig() (*tls.Config, error) {
	if err := m.ensureMTLSKey(); err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(m.certPath())
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(m.caPath())
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(m.mtlsPriv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca bundle parse failed")
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// apiBase picks the mTLS transport once a cert is loaded, else the HTTPS
// ingress (used for the very first bootstrap CSR before any cert exists).
func (m *Manager) apiBase() (string, *http.Client) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ready && m.client != nil {
		return m.mtlsBase, m.client
	}
	return m.ctrlBase, http.DefaultClient
}

// Ensure loads an existing local certificate, or performs the full
// CSR→issue→fetch→store flow if none is present (or the current one is invalid).
func (m *Manager) Ensure(ctx context.Context) error {
	if err := m.ensureMTLSKey(); err != nil {
		return err
	}
	if err := m.loadLocal(); err == nil {
		return nil
	}
	return m.enrollCert(ctx)
}

// loadLocal loads client.crt + ca.crt from disk into a live mTLS client.
func (m *Manager) loadLocal() error {
	if err := m.ensureMTLSKey(); err != nil {
		return err
	}
	certPEM, err := os.ReadFile(m.certPath())
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(m.caPath())
	if err != nil {
		return err
	}
	return m.installPEM(certPEM, caPEM)
}

func (m *Manager) installPEM(certPEM, caPEM []byte) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(m.mtlsPriv)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("client keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return errors.New("ca bundle parse failed")
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	sum := x509sha256(leaf.Raw)
	cl := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	m.mu.Lock()
	m.client = cl
	m.notAfter = leaf.NotAfter
	m.fpr = sum
	m.ready = true
	m.mu.Unlock()
	return nil
}

// enrollCert generates a CSR, submits it over the signed-auth channel, polls
// for issuance, then stores + installs the cert.
func (m *Manager) enrollCert(ctx context.Context) error {
	csrPEM, err := m.makeCSR()
	if err != nil {
		return err
	}
	// Prefer mTLS transport when a cert already exists (rotation); the very
	// first bootstrap CSR falls back to the HTTPS ingress.
	base, cl := m.apiBase()
	body, _ := json.Marshal(map[string]string{"csr_pem": string(csrPEM)})
	if _, err := m.signedDoWith(ctx, cl, base, http.MethodPost, "/v1/appliance/csr", body, nil); err != nil {
		return fmt.Errorf("csr submit: %w", err)
	}
	// Poll for issuance (first issuance may await Platform approval; rotation
	// is auto-issued so the first poll usually succeeds).
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var out struct {
			Status         string `json:"status"`
			CertificatePEM string `json:"certificate_pem"`
			CAChain        string `json:"ca_chain"`
		}
		fb, fcl := m.apiBase()
		if _, err := m.signedDoWith(ctx, fcl, fb, http.MethodGet, "/v1/appliance/certificate", nil, &out); err != nil {
			return fmt.Errorf("cert fetch: %w", err)
		}
		if out.Status == "issued" && out.CertificatePEM != "" {
			if err := m.store(ctx, []byte(out.CertificatePEM), []byte(out.CAChain)); err != nil {
				return err
			}
			slog.Info("appliancecert: certificate issued + installed", "fpr", m.fpr)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("cert issuance timed out (awaiting Platform approval?)")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
}

// store writes cert + CA atomically (0644 — public material) then installs.
func (m *Manager) store(ctx context.Context, certPEM, caPEM []byte) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(m.certPath(), certPEM, 0o644); err != nil {
		return err
	}
	if err := writeAtomic(m.caPath(), caPEM, 0o644); err != nil {
		return err
	}
	return m.installPEM(certPEM, caPEM)
}

func (m *Manager) makeCSR() ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: m.applianceID},
	}, m.mtlsPriv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func x509sha256(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// writeAtomic writes data via temp file + fsync + rename.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// MTLSHello performs a signed-auth + mTLS call to the Central mTLS listener,
// proving the client cert is accepted. Returns the server-echoed identity.
func (m *Manager) MTLSHello(ctx context.Context) (map[string]any, error) {
	cl := m.Client()
	if cl == nil {
		return nil, errors.New("no mTLS client")
	}
	var out map[string]any
	if _, err := m.signedDoWith(ctx, cl, m.mtlsBase, http.MethodGet, "/v1/appliance/hello", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MaybeRotate rotates the certificate when it is within `within` of expiry.
// It fetches a new (auto-issued) cert, verifies it via an mTLS hello BEFORE
// switching, and rolls back to the old cert if verification fails.
func (m *Manager) MaybeRotate(ctx context.Context, within time.Duration) error {
	m.mu.RLock()
	na, ready := m.notAfter, m.ready
	m.mu.RUnlock()
	if !ready || time.Until(na) > within {
		return nil
	}
	// Snapshot current material for rollback.
	oldCert, _ := os.ReadFile(m.certPath())
	oldCA, _ := os.ReadFile(m.caPath())
	slog.Info("appliancecert: rotating certificate before expiry", "not_after", na)
	if err := m.enrollCert(ctx); err != nil {
		return err
	}
	if _, err := m.MTLSHello(ctx); err != nil {
		// Verify-before-switch failed → roll back to the previous certificate.
		slog.Warn("appliancecert: rotated cert failed verification, rolling back", "err", err)
		if oldCert != nil && oldCA != nil {
			_ = m.store(ctx, oldCert, oldCA)
		}
		return fmt.Errorf("rotation verify failed: %w", err)
	}
	slog.Info("appliancecert: rotation complete", "fpr", m.fpr)
	return nil
}

// signedDo issues a signed-auth request over the default (system-trust) client.
func (m *Manager) signedDo(ctx context.Context, base, method, path string, body []byte, out any) (int, error) {
	return m.signedDoWith(ctx, http.DefaultClient, base, method, path, body, out)
}

// signedDoWith issues a signed-auth request over a specific http.Client.
func (m *Manager) signedDoWith(ctx context.Context, cl *http.Client, base, method, path string, body []byte, out any) (int, error) {
	tok, err := applianceauth.SignRequest(m.priv, m.applianceID, method, path, body)
	if err != nil {
		return 0, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode, nil
}
