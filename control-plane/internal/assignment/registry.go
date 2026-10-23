package assignment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// "Stop signing with a key" and "stop trusting it" are separate steps: an appliance holding an assignment signed
// by the old key must still reboot and re-verify it while the fleet migrates.
const (
	KeyActive = "active" // may sign new assignments and verify existing ones
	// KeyVerifyOnly keeps verifying documents already issued; where a rotated-out key lives until the fleet migrates.
	KeyVerifyOnly = "verify_only"
	// KeyRevoked is rejected for all verification: confirmed compromise, or the fleet has fully migrated.
	KeyRevoked = "revoked"
)

func CanSign(state string) bool { return state == KeyActive }

func CanVerify(state string) bool { return state == KeyActive || state == KeyVerifyOnly }

type TrustedKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"` // base64 (std) raw Ed25519 public key
	State     string `json:"state"`      // active | retired
	AddedAt   string `json:"added_at"`
	RetiredAt string `json:"retired_at,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Registry is the appliance's local trusted-key list. It is never auto-populated from Central (the appliance
// decides what it trusts) and holds only assignment-signing keys, so a document signed by any other key fails
// as an unknown signer.
type Registry struct {
	Keys []TrustedKey `json:"keys"`
}

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

func (r *Registry) Lookup(keyID string) (TrustedKey, bool) {
	for _, k := range r.Keys {
		if k.KeyID == keyID {
			return k, true
		}
	}
	return TrustedKey{}, false
}

func PublicKeyOf(k TrustedKey) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(k.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("trusted key %s: bad public key", k.KeyID)
	}
	return ed25519.PublicKey(raw), nil
}

// AddOrRotate inserts or updates a key as active.
//
// Rotation, each step safe to stop at: add the new key and distribute the registry (the old key stays active);
// confirm appliances have it; switch Central signing; re-sign every current assignment at a higher version;
// confirm all adopted it and none reference the old signer; VerifyOnly the old key (stragglers can still boot);
// Revoke only on confirmed compromise or after the retention period.
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

// VerifyOnly stops a key signing while keeping it trusted for documents already issued, so rotation never strands anyone.
func (r *Registry) VerifyOnly(keyID string) bool {
	return r.setState(keyID, KeyVerifyOnly)
}

// Revoke removes all trust in a key; only for confirmed compromise or a fully migrated fleet.
func (r *Registry) Revoke(keyID string) bool {
	return r.setState(keyID, KeyRevoked)
}

func (r *Registry) setState(keyID, state string) bool {
	for i := range r.Keys {
		if r.Keys[i].KeyID == keyID {
			r.Keys[i].State = state
			if state != KeyActive {
				r.Keys[i].RetiredAt = time.Now().UTC().Format(time.RFC3339)
			}
			return true
		}
	}
	return false
}

// AcceptForRegistry resolves the signer against the local trust registry, then applies the binding and version
// checks. A verify_only signer is accepted (a rotated-out key must still let an appliance boot on what it holds);
// only an unknown or revoked signer is refused, which enforces key separation by construction.
func AcceptForRegistry(reg *Registry, d *Document, applianceID, serial, identityFpr string, haveVersion int64, now time.Time) string {
	if reg == nil || len(reg.Keys) == 0 {
		return "no assignment trust registry on this appliance"
	}
	k, ok := reg.Lookup(d.SignerKeyID)
	if !ok {
		return "unknown assignment signer (key not in the appliance trust registry)"
	}
	if !CanVerify(k.State) {
		return "assignment signed by a " + k.State + " key (revoked signers are never trusted)"
	}
	pub, err := PublicKeyOf(k)
	if err != nil {
		return "trusted key unusable: " + err.Error()
	}
	return AcceptFor(pub, d, applianceID, serial, identityFpr, haveVersion, now)
}
