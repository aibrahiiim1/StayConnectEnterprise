package assignment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Key lifecycle states: "stop signing with it" and "stop trusting it" are distinct; collapsing them strands appliances holding an assignment signed by
// the old key that must still reboot and re-verify. So retirement is TWO steps.
const (
	KeyActive = "active"
	// KeyVerifyOnly must NOT sign new assignments but still verifies documents issued under it, until every appliance has adopted one signed by the new key.
	KeyVerifyOnly = "verify_only"
	// KeyRevoked is rejected for ALL verification: confirmed compromise, or once the whole fleet has migrated.
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

// The appliance's LOCAL trusted-key list: a vendor-shipped file, never auto-populated from Central (the appliance decides what it trusts). ONLY
// assignment-signing keys; licence, command, update, CA and Auth-Callout keys are absent, so documents they sign fail as unknown signer.
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

// Inserts or updates a key as ACTIVE. Rotation (each step safe to stop at): register the new key ACTIVE, distribute the registry (old key still ACTIVE),
// confirm appliances acknowledge it, switch Central signing, re-sign every current assignment at a HIGHER version, confirm adoption and that none
// references the old signer, then VerifyOnly() the old key; Revoke() only on confirmed compromise or after the retention period.
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

// Stops a key signing new assignments while KEEPING it trusted for documents already issued under it, which makes rotation non-stranding.
func (r *Registry) VerifyOnly(keyID string) bool {
	return r.setState(keyID, KeyVerifyOnly)
}

// Removes all trust in a key; only for confirmed compromise or once the fleet has migrated.
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

// The production acceptance check: resolves the document signer against the LOCAL trust registry, then applies the full binding/version checks. A
// verify_only signer is ACCEPTED (a rotated-out key must still let an appliance boot on its held assignment); only an unknown or REVOKED signer is
// refused. Keys of other purposes are absent, so documents they sign are rejected as unknown signer by construction.
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
