package assignment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Key lifecycle states in the appliance-side trust registry.
const (
	KeyActive  = "active"  // may sign assignments the appliance will adopt
	KeyRetired = "retired" // rotated out — assignments signed by it are REJECTED
)

// TrustedKey is one assignment-signing public key the appliance trusts.
type TrustedKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"` // base64 (std) raw Ed25519 public key
	State     string `json:"state"`      // active | retired
	AddedAt   string `json:"added_at"`
	RetiredAt string `json:"retired_at,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Registry is the appliance's LOCAL trusted-key list for assignment documents.
//
// It is deliberately local (a file shipped/rotated by the vendor) and never
// auto-populated from Central: the appliance decides what it trusts. It contains
// ONLY assignment-signing keys — the license, command, update, CA and Auth-Callout
// keys are absent, so a document signed by any of those fails as "unknown signer".
type Registry struct {
	Keys []TrustedKey `json:"keys"`
}

// LoadRegistry reads the trust registry from disk.
func LoadRegistry(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("assignment trust registry %s: %w", path, err)
	}
	return &r, nil
}

// Save writes the registry (used by provisioning/rotation tooling).
func (r *Registry) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Lookup returns the trusted key with this id, if present.
func (r *Registry) Lookup(keyID string) (TrustedKey, bool) {
	for _, k := range r.Keys {
		if k.KeyID == keyID {
			return k, true
		}
	}
	return TrustedKey{}, false
}

// PublicKeyOf decodes a trusted key's public key.
func PublicKeyOf(k TrustedKey) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(k.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("trusted key %s: bad public key", k.KeyID)
	}
	return ed25519.PublicKey(raw), nil
}

// AddOrRotate inserts/updates a key as ACTIVE. Rotation procedure:
//  1. distribute the registry with the NEW key active (old one still active),
//  2. switch Central to sign with the new key,
//  3. Retire() the old key and distribute again.
//
// Both keys are active during the overlap, so no assignment is ever unverifiable.
func (r *Registry) AddOrRotate(k TrustedKey) {
	k.State = KeyActive
	if k.AddedAt == "" {
		k.AddedAt = time.Now().UTC().Format(time.RFC3339)
	}
	for i := range r.Keys {
		if r.Keys[i].KeyID == k.KeyID {
			r.Keys[i] = k
			return
		}
	}
	r.Keys = append(r.Keys, k)
}

// Retire marks a key retired: assignments signed by it are rejected from then on.
func (r *Registry) Retire(keyID string) bool {
	for i := range r.Keys {
		if r.Keys[i].KeyID == keyID {
			r.Keys[i].State = KeyRetired
			r.Keys[i].RetiredAt = time.Now().UTC().Format(time.RFC3339)
			return true
		}
	}
	return false
}

// AcceptForRegistry is the production acceptance check: it resolves the document's
// signer_key_id against the LOCAL trust registry (rejecting unknown or retired
// signers) and then applies the full binding/version checks.
//
// Because the registry holds only assignment-signing keys, a document signed with
// the license / command / update / CA / auth-callout key is rejected as an unknown
// signer — key separation is enforced by construction, not by convention.
func AcceptForRegistry(reg *Registry, d *Document, applianceID, serial, identityFpr string, haveVersion int64, now time.Time) string {
	if reg == nil || len(reg.Keys) == 0 {
		return "no assignment trust registry on this appliance"
	}
	k, ok := reg.Lookup(d.SignerKeyID)
	if !ok {
		return "unknown assignment signer (key not in the appliance trust registry)"
	}
	if k.State != KeyActive {
		return "assignment signed by a " + k.State + " key (rotation policy: retired keys are not accepted)"
	}
	pub, err := PublicKeyOf(k)
	if err != nil {
		return "trusted key unusable: " + err.Error()
	}
	return AcceptFor(pub, d, applianceID, serial, identityFpr, haveVersion, now)
}
