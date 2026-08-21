// Package identity manages this appliance's persistent cryptographic
// identity and its mapping to the control-plane appliance row.
//
// On first boot (no identity file present) scd generates an Ed25519 keypair
// and — if SCD_BOOTSTRAP_TOKEN is set — POSTs the public key to
// /v1/appliances/enroll, saving the returned appliance_id/tenant_id/site_id
// to identity.json alongside the key.
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
// the control plane. All fields are populated once Enroll returns.
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

// LoadOrEnroll returns the existing identity if one is persisted, otherwise
// runs the enrollment flow against ctrlBase using the provided bootstrap
// token + serial. On success the identity is written to disk and returned.
//
// Returns (nil, nil) if no identity is present AND no bootstrapToken was
// given — scd can choose whether that's a soft or hard failure.
func (s *Store) LoadOrEnroll(ctx context.Context, ctrlBase, bootstrapToken, serial string, autoRegister bool) (*Identity, error) {
	// AN UNBOUND KEYPAIR IS NOT AN ENROLLED IDENTITY.
	//
	// EnsureLocalKeypair (offline first activation) writes an identity.json holding a keypair and no
	// appliance id -- deliberately, because a factory-clean appliance has no id to claim yet. But `load`
	// succeeds on that file, so returning it here meant: download one offline activation request, and this
	// appliance can never self-register online again. It would sit at "awaiting enrollment" forever, having
	// silently lost the path it was actually going to be activated by, and the only visible symptom is an
	// appliance that never appears in the control panel.
	//
	// An identity counts only once it carries the appliance id Central minted for it.
	if id, err := s.load(); err == nil {
		if id.ApplianceID != "" {
			return id, nil
		}
		// Keypair present but unbound: fall through and let self-registration bind it. register() reuses
		// this exact key rather than minting a second one, so an offline activation request an operator is
		// already carrying stays valid.
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("identity load: %w", err)
	}
	if bootstrapToken == "" {
		// Token-less activation: a factory-clean appliance with Central
		// connectivity generates its identity and self-registers with a
		// cryptographically signed request (manufacturing/identity trust). It
		// appears as Pending Activation; no token is required for the normal
		// online flow. Falls through to awaiting-enrollment if registration
		// can't complete (e.g. no Central reachability yet) — retried next boot
		// or by the periodic re-register loop.
		if autoRegister && ctrlBase != "" {
			if id, err := s.register(ctx, ctrlBase); err != nil {
				return nil, err
			} else if id != nil {
				return id, nil
			}
		}
		return nil, nil // caller decides whether to fail hard
	}
	if ctrlBase == "" {
		return nil, errors.New("identity: no control-plane base URL")
	}

	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", s.Dir, err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	// The appliance's serial is its stable hardware-derived StayConnect serial;
	// send the permanent WAN/LAN MACs + hardware fingerprint + model/hostname so
	// Central can bind the license to this exact device (and detect clones).
	hw := hwid.Detect()
	if hw.Serial != "" {
		serial = hw.Serial
	}
	body, _ := json.Marshal(map[string]string{
		"bootstrap_token":      bootstrapToken,
		"serial":               serial,
		"public_key":           pubB64,
		"wan_mac":              hw.WANMAC,
		"lan_mac":              hw.LANMAC,
		"hardware_fingerprint": hw.Fingerprint,
		"hostname":             hw.Hostname,
		"model":                hw.Model,
	})

	ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ectx, http.MethodPost,
		ctrlBase+"/v1/appliances/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enroll POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b := make([]byte, 512)
		n, _ := resp.Body.Read(b)
		return nil, fmt.Errorf("enroll status=%d body=%s", resp.StatusCode, string(b[:n]))
	}
	var r struct {
		ApplianceID string `json:"appliance_id"`
		TenantID    string `json:"tenant_id"`
		SiteID      string `json:"site_id"`
		Serial      string `json:"serial"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("enroll decode: %w", err)
	}

	id := &Identity{
		ApplianceID:  r.ApplianceID,
		TenantID:     r.TenantID,
		SiteID:       r.SiteID,
		Serial:       r.Serial,
		PublicKeyB64: pubB64,
		privKey:      priv,
	}
	// Write key first (600), then identity.json (644) so a crash between
	// the two leaves us recoverable: re-running Enroll with the same token
	// would fail (consumed), but the admin can mint a new token. Both writes
	// are atomic (temp file + fsync + rename) so a crash never leaves a
	// truncated private key on disk.
	if err := writeFileAtomic(s.keyPath(), priv, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	js, _ := json.MarshalIndent(id, "", "  ")
	if err := writeFileAtomic(s.idPath(), js, 0o644); err != nil {
		return nil, fmt.Errorf("write identity.json: %w", err)
	}
	return id, nil
}

// register performs token-less self-registration: it generates the appliance's
// Ed25519 identity keypair, signs a registration request WITH that key (proving
// key possession — trust-on-first-use), and POSTs the hardware facts to Central.
// On success Central creates a Pending Activation appliance row and returns its
// id, which is persisted alongside the key. Returns (nil,nil) if Central is
// unreachable so the caller can retry rather than fail hard.
func (s *Store) register(ctx context.Context, ctrlBase string) (*Identity, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", s.Dir, err)
	}
	// REUSE AN EXISTING UNBOUND KEYPAIR. An appliance has ONE identity key. If an operator has already
	// downloaded an offline activation request, that request names this key and is signed by it; minting a
	// second key here would invalidate the file they are carrying, with no explanation beyond a refusal at
	// the end of the round trip. Only generate when there is genuinely nothing to reuse.
	var pub ed25519.PublicKey
	var priv ed25519.PrivateKey
	if existing, err := s.load(); err == nil && existing.ApplianceID == "" && existing.privKey != nil {
		priv = existing.privKey
		pub = priv.Public().(ed25519.PublicKey)
	} else {
		var genErr error
		pub, priv, genErr = ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, fmt.Errorf("generate key: %w", genErr)
		}
	}
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	hw := hwid.Detect()
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Central unreachable — NOT FATAL, and retried on a later boot/loop. A hotel's uplink being down
		// is not a reason for the appliance to refuse to start.
		//
		// BUT IT MUST NOT BE SILENT. This returned nil,nil with nothing written anywhere, so the only trace
		// left was the caller's "awaiting enrollment: no appliance identity; run the local setup wizard" --
		// which points the operator at a wizard when the actual cause is that the appliance cannot verify
		// Central's certificate, or cannot reach it at all. On a live Production appliance that read as a
		// setup problem for a full day while the appliance had simply never trusted Central's CA.
		slog.Warn("appliance self-registration could not reach Central; will retry",
			"endpoint", ctrlBase+"/v1/appliances/register", "err", err,
			"hint", "TLS verification failure? the appliance must trust Central's CA — "+
				"deploy/scripts/install-central-trust.sh")
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b := make([]byte, 512)
		n, _ := resp.Body.Read(b)
		return nil, fmt.Errorf("register status=%d body=%s", resp.StatusCode, string(b[:n]))
	}
	var r struct {
		ApplianceID string `json:"appliance_id"`
		TenantID    string `json:"tenant_id"`
		SiteID      string `json:"site_id"`
		Serial      string `json:"serial"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("register decode: %w", err)
	}
	id := &Identity{
		ApplianceID:  r.ApplianceID,
		TenantID:     r.TenantID,
		SiteID:       r.SiteID,
		Serial:       hw.Serial,
		PublicKeyB64: pubB64,
		privKey:      priv,
	}
	if err := writeFileAtomic(s.keyPath(), priv, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	js, _ := json.MarshalIndent(id, "", "  ")
	if err := writeFileAtomic(s.idPath(), js, 0o644); err != nil {
		return nil, fmt.Errorf("write identity.json: %w", err)
	}
	return id, nil
}

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
	hw := hwid.Detect()
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
