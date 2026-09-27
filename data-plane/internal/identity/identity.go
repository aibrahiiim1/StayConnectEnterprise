// Package identity manages this appliance's persistent cryptographic
// identity and its mapping to the control-plane appliance row.
//
// On first boot (no identity file present) scd generates an Ed25519 keypair
// and registers it with Central, token-less, with a request signed by that key
// (POST /v1/appliances/register). The returned appliance_id is saved to
// identity.json alongside the key. Enrollment tokens no longer exist.
//
// On subsequent boots the files are loaded as-is; scd uses the private key
// to sign outbound control-plane calls (see applianceauth.Sign).
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/hwid"
)

// Identity is the fully-resolved set of facts scd needs to operate against
// the control plane. ApplianceID is populated once Central has registered this appliance.
// TenantID/SiteID are legacy fields that are never read for authority: the signed assignment is the only source
// of tenant and site.
type Identity struct {
	ApplianceID  string             `json:"appliance_id"`
	TenantID     string             `json:"tenant_id"`
	SiteID       string             `json:"site_id"`
	Serial       string             `json:"serial"`
	PublicKeyB64 string             `json:"public_key"` // base64-raw
	privKey      ed25519.PrivateKey // not persisted in JSON; lives in key file
}

func (i *Identity) PrivateKey() ed25519.PrivateKey { return i.privKey }

// Store is the on-disk directory layout:
//
//	<Dir>/identity.json  — appliance_id, tenant_id, site_id, serial, public key
//	<Dir>/ed25519.key    — 64-byte raw Ed25519 private seed+public (Go's format)
type Store struct {
	Dir string
}

func (s *Store) idPath() string  { return filepath.Join(s.Dir, "identity.json") }
func (s *Store) keyPath() string { return filepath.Join(s.Dir, "ed25519.key") }

// ErrCentralUnreachable wraps a registration attempt that never got an HTTP answer from Central (DNS, TCP,
// TLS or timeout). It is the retryable case: the appliance keeps its keypair and tries again later.
var ErrCentralUnreachable = errors.New("central unreachable")

// LoadBound returns the persisted identity ONLY when Central has bound an appliance id to it.
//
// AN UNBOUND KEYPAIR IS NOT AN ENROLLED IDENTITY. EnsureLocalKeypair (offline first activation) writes an
// identity.json holding a keypair and no appliance id -- deliberately, because a factory-clean appliance has
// no id to claim yet. Counting that file as an identity would mean: download one offline activation request
// and this appliance can never self-register online again.
//
// Returns (nil, nil) for a factory-clean or not-yet-registered appliance.
func (s *Store) LoadBound() (*Identity, error) {
	id, err := s.load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("identity load: %w", err)
	}
	if id.ApplianceID == "" {
		return nil, nil
	}
	return id, nil
}

// HTTPDoer is the one method Register needs from an HTTP client, so a test can answer for Central.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Register performs TOKEN-LESS self-registration, the only way an appliance obtains an appliance id online.
// Enrollment tokens no longer exist (docs/CENTRAL_CONTROL_PLANE.md section 4).
//
// It signs the registration WITH the appliance's identity key (proof of possession) and POSTs the hardware
// facts to Central, which creates a waiting-for-activation appliance row and returns its id; the id is then
// persisted beside the key.
//
// An existing UNBOUND keypair is reused: an appliance has ONE identity key, and an offline activation request
// the operator is already carrying names it. A new key is generated only when there is nothing to reuse, and
// it is written to disk BEFORE the request goes out, so every retry of an unanswered registration presents the
// same key rather than minting a new one per attempt.
//
// A network-level failure returns an error wrapping ErrCentralUnreachable; a refusal from Central returns a
// *StatusError. Neither is fatal to the caller, which retries with backoff.
func (s *Store) Register(ctx context.Context, ctrlBase string, client HTTPDoer) (*Identity, error) {
	if ctrlBase == "" {
		return nil, errors.New("identity: no Central endpoint configured")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if id, err := s.LoadBound(); err != nil {
		return nil, err
	} else if id != nil {
		return id, nil // already registered; nothing to do
	}
	local, err := s.EnsureLocalKeypair()
	if err != nil {
		return nil, err
	}
	priv := local.privKey
	pub := priv.Public().(ed25519.PublicKey)
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	hw := detectHW()
	body, _ := json.Marshal(map[string]string{
		"serial":               hw.Serial,
		"wan_mac":              hw.WANMAC,
		"lan_mac":              hw.LANMAC,
		"hardware_fingerprint": hw.Fingerprint,
		"hostname":             hw.Hostname,
		"model":                hw.Model,
		"public_key":           pubB64,
	})
	kid := applianceauth.KeyID(pub)
	tok, err := applianceauth.SignRequest(priv, kid, http.MethodPost, "/v1/appliances/register", body)
	if err != nil {
		return nil, fmt.Errorf("sign register: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, ctrlBase+"/v1/appliances/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := client.Do(req)
	if err != nil {
		// NOT FATAL, BUT NEVER SILENT. The most common cause on a real appliance has been that it does not
		// trust Central's CA; the hint points there instead of at a setup screen.
		slog.Warn("appliance self-registration could not reach Central; will retry",
			"endpoint", ctrlBase+"/v1/appliances/register", "err", err,
			"hint", "TLS verification failure? the appliance must trust Central's CA — "+
				"deploy/scripts/install-central-trust.sh")
		return nil, fmt.Errorf("%w: %v", ErrCentralUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b := make([]byte, 512)
		n, _ := resp.Body.Read(b)
		return nil, &StatusError{Code: resp.StatusCode, Body: string(b[:n])}
	}
	var r struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("register decode: %w", err)
	}
	if r.ApplianceID == "" {
		return nil, errors.New("register: Central returned no appliance id")
	}
	// Tenant and site are NEVER taken from the registration answer: the signed assignment is the only
	// authority for them (docs/CENTRAL_CONTROL_PLANE.md section 5).
	if local.Serial == "" {
		local.Serial = hw.Serial
	}
	local.ApplianceID = r.ApplianceID
	js, _ := json.MarshalIndent(local, "", "  ")
	if err := writeFileAtomic(s.idPath(), js, 0o644); err != nil {
		return nil, fmt.Errorf("write identity.json: %w", err)
	}
	return local, nil
}

// StatusError is Central answering a registration with something other than success.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("register status=%d body=%s", e.Code, e.Body)
}

// detectHW is hwid.Detect, replaceable in tests.
var detectHW = hwid.Detect

// writeFileAtomic writes data to a temp file in the same directory, fsyncs it,
// then renames it over the destination — so readers never observe a partial
// file and a crash mid-write cannot corrupt an existing key.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
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

func (s *Store) load() (*Identity, error) {
	ij, err := os.ReadFile(s.idPath())
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(ij, &id); err != nil {
		return nil, fmt.Errorf("parse identity.json: %w", err)
	}
	priv, err := os.ReadFile(s.keyPath())
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519.key wrong size: %d", len(priv))
	}
	id.privKey = ed25519.PrivateKey(priv)
	return &id, nil
}

// LoadPublic returns the appliance's PUBLIC identity — appliance id, serial and public key — without reading
// the private key file at all.
//
// It exists for readers that need to know WHICH appliance this is but have no business holding its signing
// key. pmsd is the case in point: it verifies a Central-signed assignment against this appliance's identity
// and never signs anything, so requiring the private key would give a PMS connector the appliance's key for
// no reason — and, because ed25519.key is 0600 root-only while identity.json is world-readable, it also
// simply fails for any daemon running under its own service account.
//
// Returns (nil, nil) when no identity.json exists: a factory-clean appliance has no identity, which is an
// answer rather than an error.
func (s *Store) LoadPublic() (*Identity, error) {
	ij, err := os.ReadFile(s.idPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(ij, &id); err != nil {
		return nil, fmt.Errorf("parse identity.json: %w", err)
	}
	return &id, nil
}

// EnsureLocalKeypair creates and persists an identity keypair WITHOUT contacting Central, and returns the
// identity. If one already exists it is returned unchanged.
//
// This exists for OFFLINE FIRST ACTIVATION: a factory-clean appliance with no route to Central still has to
// prove possession of a key before Central will bind an activation package to it. The key is generated here,
// on the appliance, and the private half never leaves — the activation request carries only the public half
// and a signature made with the private one.
//
// ApplianceID is deliberately left EMPTY. Only Central mints appliance ids, and only the signed assignment
// carries tenant and site; generating a keypair locally does not make an appliance identity, it makes the
// evidence that one can be issued.
func (s *Store) EnsureLocalKeypair() (*Identity, error) {
	if id, err := s.load(); err == nil {
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("identity load: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", s.Dir, err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	hw := detectHW()
	id := &Identity{Serial: hw.Serial, PublicKeyB64: base64.RawStdEncoding.EncodeToString(pub), privKey: priv}
	// Key first (0600), then identity.json, so a crash between the two leaves a key with no claim rather
	// than a claim with no key.
	if err := writeFileAtomic(s.keyPath(), priv, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	b, _ := json.Marshal(id)
	if err := writeFileAtomic(s.idPath(), b, 0o644); err != nil {
		return nil, fmt.Errorf("write identity.json: %w", err)
	}
	return id, nil
}

// AdoptApplianceID records the appliance id Central minted for an offline first activation. It refuses to
// overwrite an existing, different id: an appliance has one identity, and silently repointing it is exactly
// the confusion this whole subsystem exists to prevent.
func (s *Store) AdoptApplianceID(applianceID string) error {
	id, err := s.load()
	if err != nil {
		return err
	}
	if id.ApplianceID != "" && id.ApplianceID != applianceID {
		return fmt.Errorf("identity already bound to appliance %s", id.ApplianceID)
	}
	if id.ApplianceID == applianceID {
		return nil
	}
	id.ApplianceID = applianceID
	b, _ := json.Marshal(id)
	return writeFileAtomic(s.idPath(), b, 0o644)
}
