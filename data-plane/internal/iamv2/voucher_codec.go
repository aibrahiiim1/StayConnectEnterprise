package iamv2

// VOUCHER CODE CRYPTOGRAPHY: key generations, blind index, recoverable ciphertext.
//
// WHAT THE ACCEPTED DESIGN SAYS
// -----------------------------
// FINAL contract, key hierarchy: "appliance KEK (hardware-bound identity store) -> per-tenant DEKs
// (AES-256-GCM, versioned) -> PMS secret generations AND VOUCHER CODE KEYS, with AAD bound to owner tuples
// (ciphertext copied across owners fails authentication) ... Key loss => affected secrets unrecoverable by
// design (voucher batches -> reissue)." And: "Voucher codes: HMAC-SHA256 lookup index + AEAD-encrypted
// recoverable value + last4 display hint."
//
// The schema matches that exactly: iam_v2.voucher_code_key_generations holds a per-generation
// hmac_key_ciphertext plus the encryption_key_id that sealed it and a generation_no with superseded_at;
// iam_v2.vouchers holds code_hmac (the lookup index), code_ciphertext + code_nonce (the recoverable value)
// pinned to code_key_generation_id, and code_last4.
//
// TWO KEYS, TWO ROLES -- NOT ONE KEY DOING BOTH
// ---------------------------------------------
// An earlier checkpoint proposed using the appliance-local file /etc/stayconnect/secrets/voucher_hmac.key
// directly as the blind-index HMAC key. That is wrong twice over: it skips the generation record the schema
// requires (so nothing can rotate or be superseded, and no voucher can name the key that indexed it), and it
// collapses two distinct roles onto one physical key.
//
// The roles are kept separate here:
//
//   DEK        AES-256-GCM encryption key, identified by encryption_key_id, resolved from the appliance
//              secret store. It ONLY encrypts. It never computes a blind index.
//   HMAC key   Per-generation, randomly generated at generation creation, never stored in the clear --
//              sealed under the DEK as hmac_key_ciphertext. It ONLY indexes. It never encrypts.
//
// This mirrors what the sibling consumer in the same contract clause (PMS secret generations) already does
// in internal/pmsd/secret.go, which is the proven AES-GCM/AAD implementation in this codebase. Reusing that
// PATTERN is deliberate; reusing the same physical key material for both roles would not be.
//
// AAD BINDS THE OWNER
// -------------------
// Every seal binds Additional Authenticated Data built from the owner tuple, so a ciphertext lifted from one
// tenant/site/generation fails authentication rather than decrypting into someone else's context -- the
// "ciphertext copied across owners fails authentication" property the contract requires.
//
// PLAINTEXT NEVER PERSISTS
// ------------------------
// The plaintext code exists only in memory during issuance and is returned to the caller once. It is never
// logged, never written to a column, and never part of an audit payload. Recovering it later is the distinct
// reveal/export action the contract puts behind operator re-authentication and audit -- deliberately NOT
// something this file exposes as a side effect of issuance.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// ErrVoucherKeyUnavailable is returned when the DEK needed to seal or open voucher material is absent.
// Callers must fail closed on it: refusing to issue is correct, storing a plaintext code is not.
var ErrVoucherKeyUnavailable = errors.New("iamv2: voucher encryption key unavailable")

// ErrVoucherKeyMismatch is returned when sealed material does not authenticate under the supplied key and
// AAD. In practice this means the ciphertext belongs to a different owner tuple or generation, or the DEK
// has been replaced -- all of which must be treated as "unrecoverable", never as "retry without AAD".
var ErrVoucherKeyMismatch = errors.New("iamv2: voucher key material failed authentication")

// VoucherKeyring resolves a DEK by its encryption-key id. Implementations must never log or export key
// material. It is the same shape as the proven pmsd.Keyring, kept local so this package does not depend on
// the PMS connector.
type VoucherKeyring interface {
	Key(keyID string) ([]byte, bool)
}

// MapVoucherKeyring is an in-memory keyring populated by the daemon from the appliance secret store.
type MapVoucherKeyring map[string][]byte

func (m MapVoucherKeyring) Key(id string) ([]byte, bool) { k, ok := m[id]; return k, ok }

// generationAAD binds a sealed HMAC key to the generation that owns it. A generation's key cannot be
// replayed into another tenant, site or generation number.
func generationAAD(tenantID, siteID string, generationNo int) []byte {
	return []byte(fmt.Sprintf("iam_v2.voucher_code_key_generations|%s|%s|%d", tenantID, siteID, generationNo))
}

// voucherCodeAAD binds a sealed voucher code to the voucher row and generation that own it.
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

// NewVoucherHMACKey mints a fresh per-generation blind-index key and returns it sealed under the DEK.
// The clear key is returned too so the caller can index immediately; it must not be persisted.
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

// OpenVoucherHMACKey recovers a generation's blind-index key. Used by the authenticator to compute the
// lookup index for a submitted code, and by reissue paths. Never logs the key.
func OpenVoucherHMACKey(kr VoucherKeyring, keyID, tenantID, siteID string, generationNo int, sealed, nonce []byte) ([]byte, error) {
	dek, ok := kr.Key(keyID)
	if !ok {
		return nil, ErrVoucherKeyUnavailable
	}
	return open(dek, sealed, nonce, generationAAD(tenantID, siteID, generationNo))
}

// VoucherCodeHMAC computes the lookup index for a code under a generation's HMAC key.
//
// Domain-separated by tenant and site so the same code issued at two sites yields two different indexes,
// and so an index cannot be replayed across sites.
func VoucherCodeHMAC(hmacKey []byte, tenantID, siteID, code string) []byte {
	m := hmac.New(sha256.New, hmacKey)
	m.Write([]byte(tenantID))
	m.Write([]byte{0})
	m.Write([]byte(siteID))
	m.Write([]byte{0})
	m.Write([]byte(code))
	return m.Sum(nil)
}

// SealVoucherCode encrypts the recoverable code value for one voucher row.
func SealVoucherCode(kr VoucherKeyring, keyID, tenantID, siteID, voucherID, generationID, code string) (ciphertext, nonce []byte, err error) {
	dek, ok := kr.Key(keyID)
	if !ok {
		return nil, nil, ErrVoucherKeyUnavailable
	}
	return seal(dek, []byte(code), voucherCodeAAD(tenantID, siteID, voucherID, generationID))
}

// OpenVoucherCode recovers a code for the audited reveal/export action. It is NOT called during
// authentication -- authentication matches the blind index and never needs the plaintext.
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

// Last4 is the display hint stored alongside the ciphertext. Short codes return what they have rather than
// padding, so the hint never implies more code than exists.
func Last4(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[len(code)-4:]
}
