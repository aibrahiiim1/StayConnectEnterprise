package assignment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Key lifecycle states.
//
// The critical distinction is between "stop signing with it" and "stop trusting
// it". Collapsing those into one flag strands appliances: an appliance holding an
// assignment signed by the old key must still be able to reboot and re-verify it
// while the fleet migrates. So retirement is a TWO-STEP process.
const (
	// KeyActive may sign NEW assignments and verify existing ones.
	KeyActive = "active"
	// KeyVerifyOnly must NOT sign new assignments, but still verifies documents
	// already issued under it. This is where a rotated-out key lives until every
	// appliance has adopted an assignment signed by the new key.
	KeyVerifyOnly = "verify_only"
	// KeyRevoked is rejected for ALL verification. Only for confirmed key
	// compromise, or after the whole fleet has migrated off the key.
	KeyRevoked = "revoked"
)

// CanSign — only an active key may produce new assignments.
func CanSign(state string) bool { return state == KeyActive }

// CanVerify — active and verify_only both verify; revoked never does.
func CanVerify(state string) bool { return state == KeyActive || state == KeyVerifyOnly }

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

// AddOrRotate inserts/updates a key as ACTIVE.
//
// Rotation sequence (each step is safe to stop at):
//  1. generate + register the new key, add it here as ACTIVE,
//  2. distribute the registry (old key still ACTIVE — nothing breaks),
//  3. confirm appliances acknowledge the new registry,
//  4. switch Central signing to the new key,
//  5. re-sign every current assignment at a HIGHER version,
//  6. confirm every appliance adopted the new assignment,
//  7. verify no current assignment references the old signer,
//  8. VerifyOnly() the old key — it can no longer sign, but any straggler holding
//     an old document can still boot and verify it,
//  9. Revoke() only on confirmed compromise or after the retention period.
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

// VerifyOnly stops a key signing new assignments while KEEPING it trusted for
// documents already issued under it. This is what makes rotation non-stranding.
func (r *Registry) VerifyOnly(keyID string) bool {
	return r.setState(keyID, KeyVerifyOnly)
}

// Revoke removes all trust in a key: documents signed by it stop verifying.
// Only for confirmed compromise, or once the fleet has fully migrated.
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

// AcceptForRegistry is the production acceptance check: it resolves the document's
// signer_key_id against the LOCAL trust registry and then applies the full
// binding/version checks.
//
// A verify_only signer is ACCEPTED (that is the whole point — a rotated-out key
// must still let an appliance boot on the assignment it already holds). Only an
// unknown or REVOKED signer is refused.
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
	if !CanVerify(k.State) {
		return "assignment signed by a " + k.State + " key (revoked signers are never trusted)"
	}
	pub, err := PublicKeyOf(k)
	if err != nil {
		return "trusted key unusable: " + err.Error()
	}
	return AcceptFor(pub, d, applianceID, serial, identityFpr, haveVersion, now)
}
