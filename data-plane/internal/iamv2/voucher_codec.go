package iamv2

// Voucher code cryptography: per-generation HMAC blind index plus AEAD-encrypted recoverable code.
// The DEK only encrypts and the per-generation HMAC key (sealed under the DEK) only indexes; never use one key
// for both.
// Every seal binds AAD built from the owner tuple, so a ciphertext copied across owners fails authentication.
// The plaintext code exists only in memory during issuance; recovery is the separate audited reveal/export
// action.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Callers must fail closed on it: refusing to issue is correct, storing a plaintext code is not.
var ErrVoucherKeyUnavailable = errors.New("iamv2: voucher encryption key unavailable")

// The ciphertext belongs to a different owner tuple or generation, or the DEK was replaced. Treat as
// unrecoverable, never as "retry without AAD".
var ErrVoucherKeyMismatch = errors.New("iamv2: voucher key material failed authentication")

// Implementations must never log or export key material.
type VoucherKeyring interface {
	Key(keyID string) ([]byte, bool)
}

type MapVoucherKeyring map[string][]byte

func (m MapVoucherKeyring) Key(id string) ([]byte, bool) { k, ok := m[id]; return k, ok }

func generationAAD(tenantID, siteID string, generationNo int) []byte {
	return []byte(fmt.Sprintf("iam_v2.voucher_code_key_generations|%s|%s|%d", tenantID, siteID, generationNo))
}

func voucherCodeAAD(tenantID, siteID, voucherID, generationID string) []byte {
	return []byte(fmt.Sprintf("iam_v2.vouchers|%s|%s|%s|%s", tenantID, siteID, voucherID, generationID))
}

func seal(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
	if len(key) != 32 {
		return nil, nil, ErrVoucherKeyUnavailable
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return g.Seal(nil, nonce, plaintext, aad), nonce, nil
}

func open(key, ciphertext, nonce, aad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, ErrVoucherKeyUnavailable
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrVoucherKeyMismatch
	}
	return pt, nil
}

// The clear key is returned so the caller can index immediately; it must not be persisted.
func NewVoucherHMACKey(kr VoucherKeyring, keyID, tenantID, siteID string, generationNo int) (clear, sealed, nonce []byte, err error) {
	dek, ok := kr.Key(keyID)
	if !ok {
		return nil, nil, nil, ErrVoucherKeyUnavailable
	}
	clear = make([]byte, 32)
	if _, err := rand.Read(clear); err != nil {
		return nil, nil, nil, err
	}
	sealed, nonce, err = seal(dek, clear, generationAAD(tenantID, siteID, generationNo))
	if err != nil {
		return nil, nil, nil, err
	}
	return clear, sealed, nonce, nil
}

func OpenVoucherHMACKey(kr VoucherKeyring, keyID, tenantID, siteID string, generationNo int, sealed, nonce []byte) ([]byte, error) {
	dek, ok := kr.Key(keyID)
	if !ok {
		return nil, ErrVoucherKeyUnavailable
	}
	return open(dek, sealed, nonce, generationAAD(tenantID, siteID, generationNo))
}

// Domain-separated by tenant and site so an index cannot be replayed across sites.
func VoucherCodeHMAC(hmacKey []byte, tenantID, siteID, code string) []byte {
	m := hmac.New(sha256.New, hmacKey)
	m.Write([]byte(tenantID))
	m.Write([]byte{0})
	m.Write([]byte(siteID))
	m.Write([]byte{0})
	m.Write([]byte(code))
	return m.Sum(nil)
}

func SealVoucherCode(kr VoucherKeyring, keyID, tenantID, siteID, voucherID, generationID, code string) (ciphertext, nonce []byte, err error) {
	dek, ok := kr.Key(keyID)
	if !ok {
		return nil, nil, ErrVoucherKeyUnavailable
	}
	return seal(dek, []byte(code), voucherCodeAAD(tenantID, siteID, voucherID, generationID))
}

// Not called during authentication, which matches the blind index and never needs the plaintext.
func OpenVoucherCode(kr VoucherKeyring, keyID, tenantID, siteID, voucherID, generationID string, ciphertext, nonce []byte) (string, error) {
	dek, ok := kr.Key(keyID)
	if !ok {
		return "", ErrVoucherKeyUnavailable
	}
	pt, err := open(dek, ciphertext, nonce, voucherCodeAAD(tenantID, siteID, voucherID, generationID))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// Short codes return what they have rather than padding.
func Last4(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[len(code)-4:]
}
