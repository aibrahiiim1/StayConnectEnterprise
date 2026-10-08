// Package sealbox seals a short secret with AES-256-GCM under an appliance-local key, binding the ciphertext
// to its exact owner through the additional authenticated data. It is the pattern of internal/payment/sealing.go
// and internal/pmsd/secret.go, generalised so a third kind of secret (notification sender credentials) does not
// grow a third copy. Each caller names its own domain string, so a row sealed for one purpose never opens
// under another.
//
// Plaintext exists only in memory for the duration of one use and is never logged or returned by an API.
package sealbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// CipherVersion is the only sealing format.
const CipherVersion = 1

// ErrSeal is the single, deliberately undetailed failure.
var ErrSeal = errors.New("sealbox: seal/open failed")

// Key is a 32-byte key with a stable id derived from the key itself, so a replaced key file is detected
// rather than silently used on rows sealed under the old one.
type Key struct {
	ID     string
	domain string
	key    []byte
}

// NewKey wraps a 32-byte key for one domain (for example "notify-secret-aead:v1").
func NewKey(domain string, k []byte) (Key, error) {
	if len(k) != 32 || domain == "" {
		return Key{}, ErrSeal
	}
	sum := sha256.Sum256(append([]byte(domain+"|id:"), k...))
	return Key{ID: hex.EncodeToString(sum[:8]), domain: domain, key: append([]byte(nil), k...)}, nil
}

// Present reports whether a usable key is loaded.
func (k Key) Present() bool { return len(k.key) == 32 }

// Sealed is one ciphertext ready to be stored.
type Sealed struct {
	Ciphertext    []byte
	Nonce         []byte
	KeyID         string
	CipherVersion int
}

func (k Key) aad(parts []string) []byte {
	var b []byte
	add := func(s string) {
		b = append(b, byte(len(s)>>8), byte(len(s)))
		b = append(b, s...)
		b = append(b, 0x1f)
	}
	add(k.domain)
	for _, p := range parts {
		add(p)
	}
	return b
}

// Seal encrypts plain for exactly the owner named by parts (tenant, provider, generation ...).
func (k Key) Seal(plain []byte, parts ...string) (Sealed, error) {
	if !k.Present() || len(parts) == 0 {
		return Sealed{}, ErrSeal
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return Sealed{}, ErrSeal
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Sealed{}, ErrSeal
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, ErrSeal
	}
	ct := gcm.Seal(nil, nonce, plain, k.aad(parts))
	return Sealed{Ciphertext: ct, Nonce: nonce, KeyID: k.ID, CipherVersion: CipherVersion}, nil
}

// Open decrypts one stored ciphertext. A key-id mismatch, a moved row or tampering all fail the same way.
func (k Key) Open(s Sealed, parts ...string) ([]byte, error) {
	if !k.Present() || s.KeyID != k.ID || s.CipherVersion != CipherVersion {
		return nil, ErrSeal
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, ErrSeal
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(s.Nonce) != gcm.NonceSize() {
		return nil, ErrSeal
	}
	plain, err := gcm.Open(nil, s.Nonce, s.Ciphertext, k.aad(parts))
	if err != nil {
		return nil, ErrSeal
	}
	return plain, nil
}
