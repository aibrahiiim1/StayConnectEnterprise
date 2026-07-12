package assignment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"time"
)

// SignedRegistry is a versioned, signed envelope around the assignment trust
// registry. The plain JSON registry was unauthenticated — anyone who could write
// the file could add a key and forge assignments. This envelope binds the key
// list to a monotonic registry_version and validity window, and is signed by a
// registry ROOT-OF-TRUST key whose public half is baked into the appliance at
// manufacture (a trust anchor, distinct from the assignment-signing keys it
// authorises).
type SignedRegistry struct {
	RegistryVersion int64        `json:"registry_version"`
	IssuedAt        int64        `json:"issued_at"`
	NotBefore       int64        `json:"not_before"`
	NotAfter        int64        `json:"not_after"` // 0 = no upper bound
	Keys            []TrustedKey `json:"keys"`
	SignerKeyID     string       `json:"signer_key_id"`
	Signature       string       `json:"signature"`
}

type regSignView struct {
	RegistryVersion int64        `json:"registry_version"`
	IssuedAt        int64        `json:"issued_at"`
	NotBefore       int64        `json:"not_before"`
	NotAfter        int64        `json:"not_after"`
	Keys            []TrustedKey `json:"keys"`
	SignerKeyID     string       `json:"signer_key_id"`
}

func regSigningBytes(sr *SignedRegistry) []byte {
	b, _ := json.Marshal(regSignView{
		sr.RegistryVersion, sr.IssuedAt, sr.NotBefore, sr.NotAfter, sr.Keys, sr.SignerKeyID,
	})
	return b
}

// SignRegistry signs the envelope with the registry root key.
func SignRegistry(priv ed25519.PrivateKey, sr *SignedRegistry) {
	sr.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	sr.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, regSigningBytes(sr)))
}

// VerifyRegistry checks the envelope signature against the registry root public
// key and its validity window.
func VerifyRegistry(rootPub ed25519.PublicKey, sr *SignedRegistry, now time.Time) string {
	if sr == nil {
		return "no registry"
	}
	if sr.SignerKeyID != KeyID(rootPub) {
		return "registry signed by an unknown root key"
	}
	sig, err := base64.StdEncoding.DecodeString(sr.Signature)
	if err != nil || !ed25519.Verify(rootPub, regSigningBytes(sr), sig) {
		return "registry signature invalid (modified or wrong signer)"
	}
	if sr.NotBefore != 0 && now.Unix() < sr.NotBefore {
		return "registry not yet valid"
	}
	if sr.NotAfter != 0 && now.Unix() > sr.NotAfter {
		return "registry expired"
	}
	return ""
}

// Registry returns the inner key registry (for AcceptForRegistry).
func (sr *SignedRegistry) Registry() *Registry {
	return &Registry{Keys: sr.Keys}
}

// LoadRootPub reads the registry root public key (raw 32-byte Ed25519) trust
// anchor baked into the appliance.
func LoadRootPub(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, os.ErrInvalid
	}
	return ed25519.PublicKey(raw), nil
}
