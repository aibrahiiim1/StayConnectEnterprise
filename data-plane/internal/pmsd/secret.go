package pmsd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Keyring resolves an AEAD key by its encryption-key id. Keys are never logged, exported, or embedded in an
// Event; only the key id / generation is recorded elsewhere.
type Keyring interface {
	Key(keyID string) ([]byte, bool)
}

// MapKeyring is a simple in-memory keyring (env-populated by the daemon).
type MapKeyring map[string][]byte

func (m MapKeyring) Key(keyID string) ([]byte, bool) { k, ok := m[keyID]; return k, ok }

var ErrSecretDecrypt = errors.New("pmsd: secret decrypt failed")

// NewPgSecretDecryptor builds a Deps.DecryptSecret that reads the ciphertext/nonce for the ACTIVE secret
// generation from iam_v2.pms_interface_secret_generations and AES-256-GCM-decrypts it with the keyring key.
// It runs ONLY after ownership + generation allocation (a lock loser never calls it). Plaintext is returned
// as transient SecretMaterial (zeroed by the worker after dial) and is never logged.
func NewPgSecretDecryptor(pool *pgxpool.Pool, kr Keyring) func(context.Context, Interface, Revision, SecretGeneration) (SecretMaterial, error) {
	return func(ctx context.Context, iface Interface, rev Revision, sg SecretGeneration) (SecretMaterial, error) {
		var ciphertext, nonce []byte
		var keyID string
		err := pool.QueryRow(ctx, `SELECT ciphertext, nonce, encryption_key_id::text
			FROM iam_v2.pms_interface_secret_generations
			WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND id=$4 AND superseded_at IS NULL`,
			iface.TenantID, iface.SiteID, iface.ID, sg.ID).Scan(&ciphertext, &nonce, &keyID)
		if err != nil {
			return SecretMaterial{}, coded(CodeSecretMissing, err)
		}
		key, ok := kr.Key(keyID)
		if !ok || len(key) != 32 {
			return SecretMaterial{}, coded(CodeSecretDecryptFailed, ErrSecretDecrypt)
		}
		plain, err := aeadOpen(key, nonce, ciphertext, ownerAAD(iface, sg))
		if err != nil {
			return SecretMaterial{}, coded(CodeSecretDecryptFailed, ErrSecretDecrypt)
		}
		return NewSecretMaterial(plain), nil
	}
}

// ownerAAD binds a secret ciphertext to its EXACT owner (tenant / site / interface / secret-generation) via
// AES-GCM additional authenticated data, so a ciphertext provisioned for one interface or generation cannot
// be decrypted in a different context (a swapped/replayed ciphertext row fails authentication). The
// provisioning (encrypt) side MUST use the identical AAD. Deterministic + length-prefixed so no field
// boundary is ambiguous.
func ownerAAD(iface Interface, sg SecretGeneration) []byte {
	var b []byte
	add := func(s string) {
		b = append(b, byte(len(s)>>8), byte(len(s)))
		b = append(b, s...)
		b = append(b, 0x1f)
	}
	add("pms-secret-aead:v1")
	add(iface.TenantID)
	add(iface.SiteID)
	add(iface.ID)
	add(sg.ID)
	return b
}

// aeadOpen AES-256-GCM-opens ciphertext with the given nonce + owner-bound AAD. Isolated + pure so the
// owner-binding is unit-testable without a database.
func aeadOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, ErrSecretDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, ErrSecretDecrypt
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

// ---------- provisioning (the encrypt side) ----------

// ErrSecretEncrypt is the single failure the provisioning side reports. It is deliberately not detailed: the
// caller is an admin endpoint, and "which part of encrypting your credential failed" is not something an
// operator can act on and not something worth putting in a response body.
var ErrSecretEncrypt = errors.New("pmsd: secret encrypt failed")

// SealedSecret is a credential ready to be stored as one generation row. It carries no plaintext.
type SealedSecret struct {
	GenerationID  string // the row id, chosen BEFORE sealing because it is part of the AAD
	Ciphertext    []byte
	Nonce         []byte
	EncryptionKey string
	CipherVersion int
}

// SealSecret encrypts a credential for exactly one (tenant, site, interface, generation).
//
// The AAD is produced by the SAME ownerAAD the decrypt path uses — not a copy of it. That is the whole point
// of the binding: a ciphertext provisioned for one interface or one generation must fail authentication
// anywhere else, and two hand-written AAD builders would drift until it didn't.
//
// generationID must be the id the row will actually be stored under. Sealing under one id and inserting under
// another produces a row that decrypts nowhere, and the failure surfaces later as an unreachable PMS rather
// than as a rejected rotation.
func SealSecret(kr Keyring, keyID string, iface Interface, generationID string, plaintext []byte) (SealedSecret, error) {
	key, ok := kr.Key(keyID)
	if !ok || len(key) != 32 {
		return SealedSecret{}, ErrSecretEncrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return SealedSecret{}, ErrSecretEncrypt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return SealedSecret{}, ErrSecretEncrypt
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SealedSecret{}, ErrSecretEncrypt
	}
	aad := ownerAAD(iface, SecretGeneration{ID: generationID})
	return SealedSecret{
		GenerationID:  generationID,
		Ciphertext:    gcm.Seal(nil, nonce, plaintext, aad),
		Nonce:         nonce,
		EncryptionKey: keyID,
		CipherVersion: secretCipherVersion,
	}, nil
}

// secretCipherVersion is the on-disk format marker for AES-256-GCM with the v1 owner AAD. It is stored with
// every row so a future format change can be rolled out without guessing what an existing row is.
const secretCipherVersion = 1
