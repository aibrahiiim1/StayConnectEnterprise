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

// The certificate-status contract, as Central actually implements it (see CertBase.FetchCertificate):
//
//	issued   an active certificate exists; the PEM and CA chain come with it
//	pending  a certificate request is on file with status='pending'
//	none     no active certificate and no pending request
//
// The request table also allows 'rejected', but FetchCertificate does not surface it — a rejected request
// currently reads as "none", so the appliance submits a fresh CSR. That is existing, defensible behaviour
// and changing it would be a protocol change affecting appliances already in the field, so it is left
// alone. The terminal states below are handled because the client must not depend on Central never
// reporting one.
const (
	statusIssued  = "issued"
	statusPending = "pending"
	statusNone    = "none"
)

// terminalStatuses stop the bootstrap. Nothing the appliance does on its own can move a request out of
// these, so retrying is just noise that buries the reason.
var terminalStatuses = map[string]string{
	"rejected": "the certificate request was rejected",
	"revoked":  "the certificate was revoked",
	"denied":   "the certificate request was denied",
}

var (
	// ErrCertPending means Central has our CSR but has not issued a certificate yet. It is a WAITING
	// state, never a failure: first issuance is gated on a human pressing Activate, which may be minutes,
	// hours or days after the appliance was installed.
	ErrCertPending = errors.New("certificate not issued yet (awaiting activation)")

	// ErrCertTerminal means Central reported a state the appliance cannot advance from by retrying.
	ErrCertTerminal = errors.New("certificate request is in a terminal state")

	// ErrCertUnknownState means Central reported a status this client does not recognise.
	//
	// IT IS DELIBERATELY NOT TERMINAL. The dangerous action is submitting a CSR, and that is what is
	// refused: a client that does not understand the state must not guess "none" and post a request, which
	// is how one unrecognised word turns into a pile of duplicates. But treating it as fatal would mean a
	// Central that starts reporting one new status stops the certificate lifecycle on every appliance in
	// the fleet at once. So the appliance keeps waiting, loudly, and recovers by itself the moment Central
	// reports something it understands.
	ErrCertUnknownState = errors.New("unrecognised certificate status from Central")
)

// Ensure loads an existing local certificate, or makes ONE bootstrap attempt if none is present.
//
// It no longer blocks for ten minutes waiting for issuance. Callers that need the certificate for the life
// of the process use EnsureUntilInstalled; callers that only want it if it happens to be ready (the NATS
// transport choice at startup) get a fast answer instead of a startup stall.
func (m *Manager) Ensure(ctx context.Context) error {
	if err := m.ensureMTLSKey(); err != nil {
		return err
	}
	if err := m.loadLocal(); err == nil {
		return nil
	}
	return m.bootstrapOnce(ctx)
}

// EnsureUntilInstalled keeps trying until the certificate is installed, the service is shutting down, or
// Central reports something genuinely terminal.
//
// WHY THIS EXISTS. The bootstrap used to be a single ten-minute window starting at boot, and on expiry the
// caller logged a warning and returned — killing the certificate lifecycle for the life of the process.
// On the Production appliance the CSR was submitted at 15:10:46, the window closed at 15:20:46, and the
// operator issued the certificate at 15:21:45. Fifty-nine seconds. The appliance never asked again.
//
// Nothing downstream could work after that: the assignment channel is mTLS-only, so the signed assignment,
// the licence and convergence were all blocked behind a certificate that was sitting on Central, ready to
// collect. The only recovery was a restart nobody knew to perform.
//
// A waiting appliance and a broken one are not the same thing, and only the second is worth giving up on.
func (m *Manager) EnsureUntilInstalled(ctx context.Context) error {
	const (
		first = 15 * time.Second
		cap   = 5 * time.Minute
	)
	delay := first
	attempt := 0
	for {
		attempt++
		err := m.Ensure(ctx)
		if err == nil {
			if attempt > 1 {
				slog.Info("appliancecert: certificate acquired after waiting", "attempts", attempt)
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// TERMINAL: retrying cannot move a rejected or revoked request forward, and continuing to poll
		// would bury the one line that says why. Stop, and surface the exact reason.
		if errors.Is(err, ErrCertTerminal) {
			slog.Error("appliancecert: certificate bootstrap cannot continue", "err", err,
				"note", "this needs an operator in the control panel; the appliance will not retry")
			return err
		}
		// Report the first few attempts, then only as the backoff itself lengthens, so an appliance that
		// waits a week for activation does not write a log line every fifteen seconds for a week.
		if attempt <= 3 || delay >= cap {
			switch {
			case errors.Is(err, ErrCertPending):
				slog.Info("appliancecert: waiting for the certificate to be issued",
					"attempt", attempt, "retry_in", delay.String(),
					"note", "issued when an operator activates this appliance in the control panel")
			case errors.Is(err, ErrCertUnknownState):
				// Loud, because it means this appliance and Central disagree about the protocol. No CSR
				// was sent, and none will be until the status is one this build understands.
				slog.Error("appliancecert: Central reported a certificate status this appliance does not "+
					"recognise; not submitting a CSR", "attempt", attempt, "retry_in", delay.String(),
					"err", err, "note", "upgrade the appliance, or check Central's version")
			default:
				// Transient: connection refused, TLS failure, 5xx, a timeout. Worth retrying.
				slog.Warn("appliancecert: certificate bootstrap attempt failed; will retry",
					"attempt", attempt, "retry_in", delay.String(), "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < cap {
			delay *= 2
			if delay > cap {
				delay = cap
			}
		}
	}
}

// bootstrapOnce COLLECTS BEFORE IT SUBMITS.
//
// Central's SubmitCSR inserts a new request row on every call, so a retry loop that always posts a CSR
// would leave a trail of duplicate pending requests for one appliance — and an operator approving "the"
// request would be picking one of many. So the existing protocol state decides:
//
//	issued   collect and install it. No CSR: the certificate already exists.
//	pending  Central is holding our CSR. Wait. Submitting another would only add noise.
//	none     genuinely nothing on file — this is the only case that warrants a CSR.
//
// The local mTLS private key is never regenerated here; ensureMTLSKey keeps the existing one, so a CSR
// submitted earlier still matches the key the certificate will be bound to.
func (m *Manager) bootstrapOnce(ctx context.Context) error {
	status, certPEM, caPEM, err := m.collect(ctx)
	if err != nil {
		return err
	}
	maySubmit, err := m.actOnStatus(ctx, status, certPEM, caPEM)
	if err != nil || !maySubmit {
		return err
	}
	// Only "none" reaches here: Central has no active certificate and no request on file.
	if err := m.submitCSR(ctx); err != nil {
		return err
	}
	// One immediate re-collect: an already-activated appliance is auto-issued the moment the CSR lands,
	// so this usually completes the bootstrap without waiting for the next retry.
	status, certPEM, caPEM, err = m.collect(ctx)
	if err != nil {
		return err
	}
	maySubmit, err = m.actOnStatus(ctx, status, certPEM, caPEM)
	if err != nil {
		return err
	}
	if maySubmit {
		// Still "none" immediately after a successful submit. Do not submit again in the same attempt;
		// wait and let the next retry look.
		return ErrCertPending
	}
	return nil
}

// actOnStatus is the ONE place a certificate status is interpreted.
//
// It returns (maySubmitCSR, err). Every branch is explicit and there is no fall-through: a status this
// client does not recognise must never be mistaken for "none", because "none" is the single state that
// authorises posting a CSR, and Central inserts a new request row for every one it receives.
func (m *Manager) actOnStatus(ctx context.Context, status, certPEM, caPEM string) (bool, error) {
	switch status {
	case statusIssued:
		return false, m.installCollected(ctx, certPEM, caPEM)
	case statusPending:
		return false, ErrCertPending
	case statusNone:
		return true, nil
	}
	if reason, ok := terminalStatuses[status]; ok {
		return false, fmt.Errorf("%w: %s (status %q)", ErrCertTerminal, reason, status)
	}
	return false, fmt.Errorf("%w: %q — refusing to submit a CSR for a state this appliance does not "+
		"understand", ErrCertUnknownState, status)
}

// collect asks Central what it holds for this appliance. It changes nothing.
func (m *Manager) collect(ctx context.Context) (status, certPEM, caPEM string, err error) {
	var out struct {
		Status         string `json:"status"`
		CertificatePEM string `json:"certificate_pem"`
		CAChain        string `json:"ca_chain"`
	}
	base, cl := m.apiBase()
	if _, err := m.signedDoWith(ctx, cl, base, http.MethodGet, "/v1/appliance/certificate", nil, &out); err != nil {
		return "", "", "", fmt.Errorf("cert fetch: %w", err)
	}
	return out.Status, out.CertificatePEM, out.CAChain, nil
}

func (m *Manager) installCollected(ctx context.Context, certPEM, caPEM string) error {
	if certPEM == "" {
		// Status said issued but nothing came with it: treat as still waiting rather than as success.
		return ErrCertPending
	}
	if err := m.store(ctx, []byte(certPEM), []byte(caPEM)); err != nil {
		return err
	}
	slog.Info("appliancecert: certificate issued + installed", "fpr", m.fpr)
	return nil
}

// rotateCert renews an EXISTING certificate before expiry.
//
// Rotation must submit a CSR — that is the whole operation, and Central auto-issues for an appliance that
// already holds a valid certificate. It deliberately does not collect-first like the bootstrap does:
// collecting would return the certificate we are trying to replace and quietly do nothing, so the appliance
// would sail past its expiry believing it had rotated.
// rotationResubmitAfter bounds how long a rotation request is assumed to be outstanding before another is
// sent. Long enough that a stuck rotation cannot generate a stream of duplicates; short enough that a CSR
// genuinely lost in transit still gets replaced well inside the fourteen-day rotation window.
const rotationResubmitAfter = 24 * time.Hour

func (m *Manager) rotateCert(ctx context.Context) error {
	m.mu.RLock()
	currentFpr := m.fpr
	m.mu.RUnlock()
	outstanding := m.loadRotationPending(currentFpr)

	// A ROTATION ALREADY IN FLIGHT IS NOT A REASON TO START ANOTHER.
	//
	// MaybeRotate runs every six hours for the last fourteen days of a certificate's life. Central
	// auto-issues for a healthy appliance, so rotation normally completes on the first pass and the
	// question never arises. But an appliance that is suspended, revoked or decommissioned is NOT
	// auto-issued: SubmitCSR files the request as pending and returns. Unconditionally submitting would
	// then post a fresh CSR every six hours for a fortnight — roughly fifty-six duplicate rotation
	// requests for one appliance, and an operator having to guess which is current.
	//
	// FetchCertificate reports "issued" for the certificate we are trying to replace (an active one
	// exists), so the status alone cannot tell us whether our rotation request is still outstanding. The
	// fingerprint can: a DIFFERENT certificate is the new one.
	if !outstanding.IsZero() && time.Since(outstanding) < rotationResubmitAfter {
		status, certPEM, caPEM, err := m.collect(ctx)
		if err != nil {
			return err
		}
		if status == statusIssued && certPEM != "" && fprOfPEM(certPEM) != currentFpr {
			if err := m.installCollected(ctx, certPEM, caPEM); err != nil {
				return err
			}
			m.clearRotationPending()
			return nil
		}
		return fmt.Errorf("%w: rotation request submitted %s ago is still outstanding",
			ErrCertPending, time.Since(outstanding).Truncate(time.Minute))
	}

	if err := m.submitCSR(ctx); err != nil {
		return err
	}
	m.markRotationPending(currentFpr)

	status, certPEM, caPEM, err := m.collect(ctx)
	if err != nil {
		return err
	}
	if status != statusIssued || certPEM == "" || fprOfPEM(certPEM) == currentFpr {
		// Filed, not yet issued. The next tick will look again rather than file another.
		return ErrCertPending
	}
	if err := m.installCollected(ctx, certPEM, caPEM); err != nil {
		return err
	}
	m.clearRotationPending()
	return nil
}

// THE PENDING-ROTATION MARKER IS ON DISK, NOT IN MEMORY.
//
// Suppression that lives in a process variable is not suppression: scd restarts on assignment changes,
// deploys, crashes and reboots, and each restart would file a fresh rotation CSR against a request Central
// is already holding. Over the fourteen-day rotation window that is the same pile of duplicates the
// in-process guard was written to prevent, just triggered by restarts instead of ticks.
//
// A small file beside the certificate is the smallest mechanism that actually survives that. It needs no
// schema, no migration and no coordination, and it lives exactly where the rest of this lifecycle's state
// already lives. It holds a timestamp and the fingerprint of the certificate being replaced — both public;
// the fingerprint is already in Status() and the logs. No key material is written, copied or weakened.
type rotationMarker struct {
	SubmittedAt    time.Time `json:"submitted_at"`
	ForFingerprint string    `json:"for_fingerprint"`
}

func (m *Manager) rotationMarkerPath() string { return filepath.Join(m.dir, "rotation-pending.json") }

// loadRotationPending returns when the outstanding rotation CSR was filed, or the zero time if there is
// none that still applies.
//
// It is SELF-INVALIDATING. The marker names the certificate it was filed against; if the installed
// certificate is no longer that one, the rotation it describes has already completed — by this process, by
// an earlier one, or by a plain Ensure() that collected the new certificate at startup — so the marker is
// stale and is removed. That is what makes a restart *after* a successful rotation clean up after itself
// instead of suppressing the next one.
func (m *Manager) loadRotationPending(currentFpr string) time.Time {
	raw, err := os.ReadFile(m.rotationMarkerPath())
	if err != nil {
		return time.Time{}
	}
	var mk rotationMarker
	if err := json.Unmarshal(raw, &mk); err != nil || mk.SubmittedAt.IsZero() {
		// A corrupt marker must not be able to block rotation forever: a certificate that silently expires
		// is a far worse outcome than one duplicate CSR. Drop it and let this pass proceed normally.
		slog.Warn("appliancecert: unreadable rotation marker, ignoring it", "path", m.rotationMarkerPath())
		_ = os.Remove(m.rotationMarkerPath())
		return time.Time{}
	}
	if mk.ForFingerprint != "" && currentFpr != "" && mk.ForFingerprint != currentFpr {
		// The certificate moved on; this marker describes a rotation that is already done.
		_ = os.Remove(m.rotationMarkerPath())
		return time.Time{}
	}
	return mk.SubmittedAt
}

func (m *Manager) markRotationPending(forFpr string) {
	b, err := json.Marshal(rotationMarker{SubmittedAt: time.Now().UTC(), ForFingerprint: forFpr})
	if err != nil {
		return
	}
	// 0644: a timestamp and a public fingerprint. Nothing here is secret.
	if err := writeAtomic(m.rotationMarkerPath(), b, 0o644); err != nil {
		// Non-fatal. Worst case the suppression is lost and one extra CSR is filed later — the rotation
		// itself has already been submitted and must not be undone by a bookkeeping failure.
		slog.Warn("appliancecert: could not record the pending rotation", "err", err)
	}
}

func (m *Manager) clearRotationPending() {
	if err := os.Remove(m.rotationMarkerPath()); err != nil && !os.IsNotExist(err) {
		slog.Warn("appliancecert: could not clear the pending-rotation marker", "err", err)
	}
}

// fprOfPEM computes the same fingerprint installPEM records, so a collected certificate can be compared
// against the installed one without installing it first.
func fprOfPEM(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return x509sha256(leaf.Raw)
}

func (m *Manager) submitCSR(ctx context.Context) error {
	csrPEM, err := m.makeCSR()
	if err != nil {
		return err
	}
	// Prefer the mTLS transport when a cert already exists (rotation); the very first bootstrap CSR falls
	// back to the HTTPS ingress.
	base, cl := m.apiBase()
	body, _ := json.Marshal(map[string]string{"csr_pem": string(csrPEM)})
	if _, err := m.signedDoWith(ctx, cl, base, http.MethodPost, "/v1/appliance/csr", body, nil); err != nil {
		return fmt.Errorf("csr submit: %w", err)
	}
	slog.Info("appliancecert: CSR submitted")
	return nil
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
	if err := m.rotateCert(ctx); err != nil {
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
