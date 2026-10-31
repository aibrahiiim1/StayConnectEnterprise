// Package appliancecert manages the appliance mutual-TLS client certificate lifecycle: CSR from the local identity key, signed-auth submission, atomic
// storage, mTLS transport to Central (the signed-request JWT stays layered on top) and rotation that proves a new certificate with an mTLS hello before
// it is stored. The private key never leaves the box; the certificate and CA are public.
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

type Manager struct {
	dir         string // cert directory, e.g. /etc/stayconnect/certs
	ctrlBase    string // https ingress for signed-auth CSR submit/fetch (e.g. https://central.example.com)
	mtlsBase    string // mTLS listener (e.g. https://central.example.com:9443)
	applianceID string
	priv        ed25519.PrivateKey
	// PinPath is the pinned appliance Root CA file (AD-2); empty means <parent of dir>/appliance-root-ca.crt.
	PinPath string

	mu       sync.RWMutex
	mtlsPriv ed25519.PrivateKey // SEPARATE key: CSR + mTLS client auth only
	client   *http.Client       // mTLS client (nil until a cert is loaded)
	notAfter time.Time
	fpr      string
	ready    bool

	// kick cuts the bootstrap backoff short (Check now, a freshly adopted assignment); onAttempt observes every
	// bootstrap attempt that actually consulted Central (never a certificate merely loaded from disk), which is how
	// scd knows Central is answering while the appliance waits.
	kick      chan struct{}
	onAttempt func(error)
}

func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) OnAttempt(fn func(error)) { m.onAttempt = fn }

func (m *Manager) mtlsKeyPath() string { return filepath.Join(m.dir, "mtls-client.key") }

// ingressClient carries every call to Central's HTTPS ingress. A bounded timeout matters: a silently dropping firewall would otherwise block the
// bootstrap or the reconcile loop for good.
var ingressClient = &http.Client{Timeout: 15 * time.Second}

const (
	maxResponseBody = 1 << 20
	maxErrorBody    = 512
)

// Independent of the identity-signing key: this key backs the CSR and the mTLS client certificate only, and is never uploaded or placed in identity.json.
func (m *Manager) ensureMTLSKey() error {
	if m.mtlsPriv != nil {
		return nil
	}
	raw, err := os.ReadFile(m.mtlsKeyPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Only a missing key is created. Any other failure (permissions after a restore, I/O) must not replace the key the installed
		// certificate carries.
		return fmt.Errorf("mtls key unreadable (not regenerating): %w", err)
	}
	if err == nil {
		if fi, serr := os.Stat(m.mtlsKeyPath()); serr == nil && fi.Mode().Perm()&0o077 != 0 {
			slog.Warn("appliancecert: mTLS key file is readable by group or others", "path", m.mtlsKeyPath(), "mode", fi.Mode().Perm())
		}
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
	return &Manager{dir: dir, ctrlBase: ctrlBase, mtlsBase: mtlsBase, applianceID: applianceID, priv: priv,
		kick: make(chan struct{}, 1)}
}

func (m *Manager) certPath() string { return filepath.Join(m.dir, "client.crt") }
func (m *Manager) caPath() string   { return filepath.Join(m.dir, "ca.crt") }

func (m *Manager) Ready() bool { m.mu.RLock(); defer m.mu.RUnlock(); return m.ready }

func (m *Manager) Client() *http.Client { m.mu.RLock(); defer m.mu.RUnlock(); return m.client }

func (m *Manager) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{"mtls_ready": m.ready, "cert_fingerprint": m.fpr, "not_after": m.notAfter}
}

func (m *Manager) Transport() (*http.Client, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client, m.mtlsBase, m.ready
}

// Prefers the mTLS transport once a cert is loaded, else the HTTPS ingress (the very first bootstrap CSR).
func (m *Manager) apiBase() (string, *http.Client) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ready && m.client != nil {
		return m.mtlsBase, m.client
	}
	return m.ctrlBase, ingressClient
}

// The certificate-status contract, as Central implements it (CertBase.FetchCertificate):
//
//	issued   an active certificate exists; the PEM and CA chain come with it
//	pending  a certificate request is on file with status='pending'
//	none     no active certificate and no pending request
//
// A rejected request currently reads as "none", so the appliance submits a fresh CSR; changing that would be a protocol change for fielded appliances.
const (
	statusIssued  = "issued"
	statusPending = "pending"
	statusNone    = "none"
)

// terminalStatuses stop the bootstrap: nothing the appliance does can move a request out of these, so retrying just buries the reason.
var terminalStatuses = map[string]string{
	"rejected": "the certificate request was rejected",
	"revoked":  "the certificate was revoked",
	"denied":   "the certificate request was denied",
}

var (
	// ErrCertPending: Central has our CSR but has not issued yet. A WAITING state, never a failure: first issuance waits on a human pressing Activate.
	ErrCertPending = errors.New("certificate not issued yet (awaiting activation)")

	// ErrCertTerminal means Central reported a state the appliance cannot advance from by retrying.
	ErrCertTerminal = errors.New("certificate request is in a terminal state")

	// ErrCertUnknownState: Central reported a status this client does not recognise. DELIBERATELY NOT TERMINAL: it must not guess "none" and post a CSR
	// (duplicate requests), but treating it as fatal would stop the certificate lifecycle on every appliance at once when Central adds a status. The
	// appliance keeps waiting, loudly, and recovers when Central says something it understands.
	ErrCertUnknownState = errors.New("unrecognised certificate status from Central")

	// ErrCertKeyMismatch: Central issued a certificate for a key this appliance does not hold, i.e. it signed a request from an
	// earlier installation of the same hardware (finding F1). The certificate is never written; the appliance asks for its own.
	ErrCertKeyMismatch = errors.New("the issued certificate does not carry this appliance's mTLS key")
)

// Loads an existing local certificate, or makes ONE bootstrap attempt if none is present, without blocking for issuance (see EnsureUntilInstalled).
func (m *Manager) Ensure(ctx context.Context) error {
	_, err := m.ensure(ctx)
	return err
}

// ensure is Ensure that also says whether Central was consulted. Loading a certificate that is already on disk is
// NOT an exchange with Central: it must never be reported as one (it used to be, so after every restart the
// "last contact with Central" jumped to the restart time while the network was cut).
func (m *Manager) ensure(ctx context.Context) (contactedCentral bool, err error) {
	if err := m.ensureMTLSKey(); err != nil {
		return false, err
	}
	if err := m.loadLocal(); err == nil {
		return false, nil
	}
	return true, m.bootstrapOnce(ctx)
}

// EnsureUntilInstalled keeps trying until the certificate is installed, the service is shutting down, or Central reports something genuinely terminal.
// A single fixed window used to end the certificate lifecycle when an operator activated just after it closed, stranding the mTLS-only assignment and
// licence channels until a restart; a waiting appliance is not a broken one.
func (m *Manager) EnsureUntilInstalled(ctx context.Context) error {
	const (
		first = 15 * time.Second
		cap   = 5 * time.Minute
	)
	delay := first
	attempt := 0
	for {
		attempt++
		contacted, err := m.ensure(ctx)
		if m.onAttempt != nil && contacted {
			m.onAttempt(err)
		}
		if err == nil {
			if attempt > 1 {
				slog.Info("appliancecert: certificate acquired after waiting", "attempts", attempt)
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// TERMINAL: retrying cannot advance a rejected or revoked request, and polling would bury the reason. Stop and surface it.
		if errors.Is(err, ErrCertTerminal) {
			slog.Error("appliancecert: certificate bootstrap cannot continue", "err", err,
				"note", "this needs an operator in the control panel; the appliance will not retry")
			return err
		}
		// Log the first few attempts, then only as the backoff lengthens, so a week-long wait is not a log line every fifteen seconds.
		if attempt <= 3 || delay >= cap {
			switch {
			case errors.Is(err, ErrCertPending):
				slog.Info("appliancecert: waiting for the certificate to be issued",
					"attempt", attempt, "retry_in", delay.String(),
					"note", "issued when an operator activates this appliance in the control panel")
			case errors.Is(err, ErrCertUnknownState):
				// Loud: this appliance and Central disagree about the protocol. No CSR was sent.
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
		case <-m.kick:
			delay = first // an operator asked, or activation just happened: start the short backoff again
			continue
		}
		if delay < cap {
			delay *= 2
			if delay > cap {
				delay = cap
			}
		}
	}
}

// bootstrapOnce COLLECTS BEFORE IT SUBMITS: SubmitCSR inserts a new request row on every call, so the existing state decides. issued: collect and
// install, no CSR. pending: wait. none: the only case that warrants a CSR. The local mTLS private key is never regenerated, so an earlier CSR still matches.
func (m *Manager) bootstrapOnce(ctx context.Context) error {
	status, certPEM, caPEM, err := m.collect(ctx)
	if err != nil {
		return err
	}
	maySubmit, err := m.actOnStatus(ctx, status, certPEM, caPEM)
	if errors.Is(err, ErrCertKeyMismatch) {
		_, err = m.requestOwnCertificate(ctx, fprOfPEM(certPEM), m.submitCSR, m.collect)
		return err
	}
	if err != nil || !maySubmit {
		return err
	}
	// Only "none" reaches here: Central has no active certificate and no request on file.
	if err := m.submitCSR(ctx); err != nil {
		return err
	}
	// One immediate re-collect: an activated appliance is auto-issued as the CSR lands, so this usually completes the bootstrap.
	status, certPEM, caPEM, err = m.collect(ctx)
	if err != nil {
		return err
	}
	maySubmit, err = m.actOnStatus(ctx, status, certPEM, caPEM)
	if errors.Is(err, ErrCertKeyMismatch) {
		_, err = m.requestOwnCertificate(ctx, fprOfPEM(certPEM), m.submitCSR, m.collect)
		return err
	}
	if err != nil {
		return err
	}
	if maySubmit {
		// Still "none" right after a successful submit: do not submit again this attempt; let the next retry look.
		return ErrCertPending
	}
	return nil
}

// The ONE place a certificate status is interpreted; returns (maySubmitCSR, err). No fall-through: an unrecognised status must never be mistaken for
// "none", the single state that authorises posting a CSR.
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

// requestOwnCertificate answers an issued certificate that carries another key. Nothing is written. ONE CSR for the local key
// is filed per foreign certificate per rotationResubmitAfter (the on-disk marker), so a Central that cannot sign it yet is not
// flooded; an activated appliance's request is signed on arrival, so the re-collect usually installs the right certificate.
// The request is signed by the identity key like every other CSR: the appliance gains nothing it could not already ask for.
func (m *Manager) requestOwnCertificate(ctx context.Context, foreignFpr string, submit func(context.Context) error,
	collect func(context.Context) (string, string, string, error)) (bool, error) {
	if t := m.loadRotationPending(foreignFpr); t.IsZero() || time.Since(t) >= rotationResubmitAfter {
		slog.Warn("appliancecert: Central issued a certificate for a key this appliance does not hold; requesting its own",
			"foreign_fpr", foreignFpr, "note", "a certificate request from an earlier installation of this hardware was signed")
		if err := submit(ctx); err != nil {
			return false, err
		}
		m.markRotationPending(foreignFpr)
	}
	status, certPEM, caPEM, err := collect(ctx)
	if err != nil {
		return false, err
	}
	if status == statusIssued && certPEM != "" {
		err := m.store(ctx, []byte(certPEM), []byte(caPEM))
		if err == nil {
			m.clearRotationPending()
			slog.Info("appliancecert: certificate for this appliance's own key installed", "fpr", m.currentFpr())
			return true, nil
		}
		if !errors.Is(err, ErrCertKeyMismatch) {
			return false, err
		}
	}
	return false, fmt.Errorf("%w: Central has not yet signed this appliance's own request (status %q)", ErrCertPending, status)
}

// matchesLocalKey refuses a certificate whose public key is not this appliance's mTLS key, before anything is written.
func (m *Manager) matchesLocalKey(certPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return errors.New("certificate PEM decode failed")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("certificate parse: %w", err)
	}
	if pub, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(m.mtlsPriv.Public()) {
		return fmt.Errorf("%w (certificate %s)", ErrCertKeyMismatch, x509sha256(leaf.Raw))
	}
	return nil
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

// Bounds how long a rotation request is assumed outstanding: long enough to avoid a stream of duplicates, short enough that a CSR lost in transit is
// replaced well inside the fourteen-day rotation window.
const rotationResubmitAfter = 24 * time.Hour

// Renews an EXISTING certificate before expiry. It must submit a CSR and deliberately does not collect first: collecting would return the certificate
// being replaced and rotation would silently do nothing.
func (m *Manager) rotateCert(ctx context.Context) error {
	m.mu.RLock()
	currentFpr := m.fpr
	m.mu.RUnlock()
	outstanding := m.loadRotationPending(currentFpr)

	// A rotation already in flight is not a reason to start another: MaybeRotate runs every six hours for the last fourteen days, and a suspended,
	// revoked or decommissioned appliance is NOT auto-issued, so unconditional submits would file ~56 duplicate requests. FetchCertificate reports
	// "issued" for the certificate being replaced, so only a DIFFERENT fingerprint means the new one has arrived.
	if !outstanding.IsZero() && time.Since(outstanding) < rotationResubmitAfter {
		status, certPEM, caPEM, err := m.collect(ctx)
		if err != nil {
			return err
		}
		if status == statusIssued && certPEM != "" && fprOfPEM(certPEM) != currentFpr {
			if err := m.installVerified(ctx, certPEM, caPEM); err != nil {
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
	if err := m.installVerified(ctx, certPEM, caPEM); err != nil {
		return err
	}
	m.clearRotationPending()
	return nil
}

// installVerified proves a rotated certificate with an mTLS hello from a candidate client BEFORE anything is stored or switched. A failed hello leaves
// the current certificate in use and the pending marker in place, so the next tick collects and tries the same certificate again without a new CSR.
func (m *Manager) installVerified(ctx context.Context, certPEM, caPEM string) error {
	if err := m.ensureMTLSKey(); err != nil {
		return err
	}
	if err := m.matchesLocalKey([]byte(certPEM)); err != nil {
		return err
	}
	cl, _, err := m.buildClient([]byte(certPEM), []byte(caPEM))
	if err != nil {
		return err
	}
	if _, err := m.signedDoWith(ctx, cl, m.mtlsBase, http.MethodGet, "/v1/appliance/hello", nil, nil); err != nil {
		return fmt.Errorf("rotation verify failed (current certificate kept): %w", err)
	}
	return m.installCollected(ctx, certPEM, caPEM)
}

// THE PENDING-ROTATION MARKER IS ON DISK, NOT IN MEMORY: scd restarts on assignment changes, deploys, crashes and reboots, so an in-process guard would
// file a fresh rotation CSR on each restart. A small file beside the certificate holds a timestamp and the fingerprint of the certificate being
// replaced (both public); no key material.
type rotationMarker struct {
	SubmittedAt    time.Time `json:"submitted_at"`
	ForFingerprint string    `json:"for_fingerprint"`
}

func (m *Manager) rotationMarkerPath() string { return filepath.Join(m.dir, "rotation-pending.json") }

// Returns when the outstanding rotation CSR was filed, or the zero time if none applies. SELF-INVALIDATING: the marker names the certificate it was
// filed against, and if the installed one differs the rotation is done (here, earlier, or by Ensure at startup), so the marker is stale and removed.
func (m *Manager) loadRotationPending(currentFpr string) time.Time {
	raw, err := os.ReadFile(m.rotationMarkerPath())
	if err != nil {
		return time.Time{}
	}
	var mk rotationMarker
	if err := json.Unmarshal(raw, &mk); err != nil || mk.SubmittedAt.IsZero() {
		// A corrupt marker must not block rotation forever: a silently expiring certificate is far worse than one duplicate CSR.
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
		// Non-fatal: at worst the suppression is lost and one extra CSR is filed; the submitted rotation must not be undone by bookkeeping.
		slog.Warn("appliancecert: could not record the pending rotation", "err", err)
	}
}

func (m *Manager) clearRotationPending() {
	if err := os.Remove(m.rotationMarkerPath()); err != nil && !os.IsNotExist(err) {
		slog.Warn("appliancecert: could not clear the pending-rotation marker", "err", err)
	}
}

// The same fingerprint installPEM records, so a collected certificate can be compared without installing it.
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
	// Prefer the mTLS transport when a cert exists (rotation); the first bootstrap CSR falls back to the HTTPS ingress.
	base, cl := m.apiBase()
	body, _ := json.Marshal(map[string]string{"csr_pem": string(csrPEM)})
	if _, err := m.signedDoWith(ctx, cl, base, http.MethodPost, "/v1/appliance/csr", body, nil); err != nil {
		return fmt.Errorf("csr submit: %w", err)
	}
	slog.Info("appliancecert: CSR submitted")
	return nil
}

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
	adopt, err := m.checkAgainstPin(certPEM, caPEM, time.Time{}, nil)
	if err != nil {
		return err
	}
	if err := m.installPEM(certPEM, caPEM); err != nil {
		return err
	}
	m.writePin(adopt, "installed CA bundle")
	return nil
}

func (m *Manager) buildClient(certPEM, caPEM []byte) (*http.Client, *x509.Certificate, error) {
	keyDER, err := x509.MarshalPKCS8PrivateKey(m.mtlsPriv)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("client keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, nil, errors.New("ca bundle parse failed")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("certificate parse: %w", err)
	}
	cl := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	return cl, leaf, nil
}

func (m *Manager) installPEM(certPEM, caPEM []byte) error {
	cl, leaf, err := m.buildClient(certPEM, caPEM)
	if err != nil {
		return err
	}
	sum := x509sha256(leaf.Raw)
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
	if err := m.ensureMTLSKey(); err != nil {
		return err
	}
	if err := m.matchesLocalKey(certPEM); err != nil {
		return err
	}
	// The pair must load before anything is written, so a bad CA bundle never leaves an unusable pair on disk.
	if _, _, err := m.buildClient(certPEM, caPEM); err != nil {
		return err
	}
	// AD-2: the bundle must chain to the pinned appliance Root CA (the offline package's bundle seeds the pin on an
	// appliance that has none).
	offlineBundle, _ := os.ReadFile(filepath.Join(m.dir, "ca-bundle.crt"))
	adopt, err := m.checkAgainstPin(certPEM, caPEM, time.Now(), offlineBundle)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	oldCert, oldErr := os.ReadFile(m.certPath())
	if err := writeAtomic(m.certPath(), certPEM, 0o644); err != nil {
		return err
	}
	if err := writeAtomic(m.caPath(), caPEM, 0o644); err != nil {
		// Put the previous certificate back so the files on disk stay one matching pair.
		if oldErr == nil {
			if rerr := writeAtomic(m.certPath(), oldCert, 0o644); rerr != nil {
				slog.Error("appliancecert: could not restore the previous certificate after a failed CA write", "err", rerr)
			}
		}
		return err
	}
	if err := m.installPEM(certPEM, caPEM); err != nil {
		return err
	}
	m.writePin(adopt, "first CA bundle")
	return nil
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// A signed-auth + mTLS call proving the client cert is accepted; returns the server-echoed identity.
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

// Rotates when within `within` of expiry. rotateCert proves the new certificate with an mTLS hello before it is stored, so a failed hello never
// switches certificates and needs no rollback.
func (m *Manager) MaybeRotate(ctx context.Context, within time.Duration) error {
	m.mu.RLock()
	na, ready := m.notAfter, m.ready
	m.mu.RUnlock()
	if !ready || time.Until(na) > within {
		return nil
	}
	slog.Info("appliancecert: rotating certificate before expiry", "not_after", na)
	if err := m.rotateCert(ctx); err != nil {
		return err
	}
	slog.Info("appliancecert: rotation complete", "fpr", m.currentFpr())
	return nil
}

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
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		if len(b) > maxErrorBody {
			b = b[:maxErrorBody]
		}
		return resp.StatusCode, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode, nil
}

type ReconcileOutcome string

const (
	ReconcileCurrent     ReconcileOutcome = "current"   // Central honours the installed certificate
	ReconcileInstalled   ReconcileOutcome = "installed" // Central had signed a newer certificate; installed
	ReconcileReissued    ReconcileOutcome = "reissued"  // Central held none; a CSR was sent and the result installed
	ReconcileWaiting     ReconcileOutcome = "waiting"   // a request is on file; Central has not signed it yet
	ReconcileNotYetReady ReconcileOutcome = "not_ready" // no certificate is installed; the bootstrap owns this
)

// Checks that the installed certificate is still the one Central honours and repairs it when not. Central can withdraw a certificate itself ("Reissue
// certificate" revokes the active one) and an appliance holding a certificate FILE never re-enters the bootstrap, so it would present the revoked one
// forever. It asks over the SIGNED HTTPS INGRESS, never mTLS (the refused channel), with the bootstrap status contract: issued with the same fingerprint
// is nothing to do, issued with another is installed, pending waits, none sends ONE CSR and installs the result. "none" is the only state that sends
// a CSR, so a reconcile never files a second while one is pending. The mTLS private key is kept.
func (m *Manager) Reconcile(ctx context.Context) (ReconcileOutcome, error) {
	if !m.Ready() {
		return ReconcileNotYetReady, nil
	}
	status, certPEM, caPEM, err := m.collectIngress(ctx)
	if err != nil {
		return "", err
	}
	switch status {
	case statusIssued:
		if certPEM == "" || fprOfPEM(certPEM) == m.currentFpr() {
			return ReconcileCurrent, nil
		}
		if err := m.store(ctx, []byte(certPEM), []byte(caPEM)); errors.Is(err, ErrCertKeyMismatch) {
			installed, err := m.requestOwnCertificate(ctx, fprOfPEM(certPEM), m.submitCSRIngress, m.collectIngress)
			if installed {
				return ReconcileReissued, nil
			}
			if errors.Is(err, ErrCertPending) {
				return ReconcileWaiting, nil
			}
			return "", err
		} else if err != nil {
			return "", err
		}
		slog.Warn("appliancecert: Central holds a newer certificate for this appliance; installed it", "fpr", m.currentFpr())
		return ReconcileInstalled, nil
	case statusPending:
		return ReconcileWaiting, nil
	case statusNone:
		slog.Warn("appliancecert: Central no longer honours the installed certificate; requesting a new one")
		if err := m.submitCSRIngress(ctx); err != nil {
			return "", err
		}
		status, certPEM, caPEM, err = m.collectIngress(ctx)
		if err != nil {
			return "", err
		}
		if status == statusIssued && certPEM != "" {
			if err := m.store(ctx, []byte(certPEM), []byte(caPEM)); err != nil {
				return "", err
			}
			slog.Info("appliancecert: new certificate installed after reissue", "fpr", m.currentFpr())
			return ReconcileReissued, nil
		}
		return ReconcileWaiting, nil
	}
	if reason, ok := terminalStatuses[status]; ok {
		return "", fmt.Errorf("%w: %s (status %q)", ErrCertTerminal, reason, status)
	}
	return "", fmt.Errorf("%w: %q", ErrCertUnknownState, status)
}

func (m *Manager) currentFpr() string { m.mu.RLock(); defer m.mu.RUnlock(); return m.fpr }

func (m *Manager) collectIngress(ctx context.Context) (status, certPEM, caPEM string, err error) {
	var out struct {
		Status         string `json:"status"`
		CertificatePEM string `json:"certificate_pem"`
		CAChain        string `json:"ca_chain"`
	}
	if _, err := m.signedDoWith(ctx, ingressClient, m.ctrlBase, http.MethodGet, "/v1/appliance/certificate", nil, &out); err != nil {
		return "", "", "", fmt.Errorf("cert fetch: %w", err)
	}
	return out.Status, out.CertificatePEM, out.CAChain, nil
}

func (m *Manager) submitCSRIngress(ctx context.Context) error {
	csrPEM, err := m.makeCSR()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"csr_pem": string(csrPEM)})
	if _, err := m.signedDoWith(ctx, ingressClient, m.ctrlBase, http.MethodPost, "/v1/appliance/csr", body, nil); err != nil {
		return fmt.Errorf("csr submit: %w", err)
	}
	slog.Info("appliancecert: CSR submitted (reconcile)")
	return nil
}
