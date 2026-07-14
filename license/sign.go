package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Signer holds the vendor private key. It lives ONLY in the cloud control
// plane; appliances never see it.
type Signer struct {
	priv  ed25519.PrivateKey
	keyID string
}

// KeyIDFor derives a stable key identifier from a public key (first 8 bytes
// of its SHA-256, hex). Lets appliances pick the right key during rotations.
func KeyIDFor(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

func NewSigner(priv ed25519.PrivateKey) *Signer {
	return &Signer{priv: priv, keyID: KeyIDFor(priv.Public().(ed25519.PublicKey))}
}

// GenerateVendorKey creates a fresh vendor keypair and writes the private
// key (seed||pub, Go's 64-byte form) to path with 0600. Returns the public
// key for distribution to appliances.
func GenerateVendorKey(path string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, priv, 0o600); err != nil {
		return nil, err
	}
	return pub, nil
}

// LoadSigner reads a 64-byte Ed25519 private key written by GenerateVendorKey.
func LoadSigner(path string) (*Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("vendor key %s: want %d bytes, got %d", path, ed25519.PrivateKeySize, len(raw))
	}
	return NewSigner(ed25519.PrivateKey(raw)), nil
}

func (s *Signer) KeyID() string                { return s.keyID }
func (s *Signer) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

// Sign validates and signs a Document, returning the wire Envelope.
func (s *Signer) Sign(d *Document) (*Envelope, error) {
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("refusing to sign invalid document: %w", err)
	}
	payload, err := d.Marshal()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(s.priv, payload)
	return &Envelope{
		PayloadB64: base64.StdEncoding.EncodeToString(payload),
		SigB64:     base64.StdEncoding.EncodeToString(sig),
		KeyID:      s.keyID,
	}, nil
}

// Encode renders an Envelope as JSON for storage/transport.
func (e *Envelope) Encode() ([]byte, error) { return json.Marshal(e) }

func DecodeEnvelope(raw []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("malformed license envelope: %w", err)
	}
	return &e, nil
}
