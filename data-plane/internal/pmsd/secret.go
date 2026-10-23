package pmsd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Keyring resolves an AEAD key by id. Keys are never logged, exported or embedded in an Event.
type Keyring interface {
	Key(keyID string) ([]byte, bool)
}

type MapKeyring map[string][]byte

func (m MapKeyring) Key(keyID string) ([]byte, bool) { k, ok := m[keyID]; return k, ok }

var ErrSecretDecrypt = errors.New("pmsd: secret decrypt failed")

// NewPgSecretDecryptor runs only after ownership and generation allocation (a lock loser never calls it).
// Plaintext is transient SecretMaterial, zeroed by the worker after dial and never logged.
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

// ownerAAD binds a ciphertext to its exact tenant/site/interface/generation so a swapped or replayed row fails
// authentication. Encryption must use this same AAD. Length-prefixed so no field boundary is ambiguous.
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

// ErrSecretEncrypt is deliberately undetailed: nothing about why encryption failed belongs in a response body.
var ErrSecretEncrypt = errors.New("pmsd: secret encrypt failed")

type SealedSecret struct {
	GenerationID  string // the row id, chosen BEFORE sealing because it is part of the AAD
	Ciphertext    []byte
	Nonce         []byte
	EncryptionKey string
	CipherVersion int
}

// SealSecret uses the same ownerAAD as decryption (two builders would drift). generationID must be the id the
// row is stored under, else it decrypts nowhere and surfaces later as an unreachable PMS.
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

// secretCipherVersion marks the on-disk format (AES-256-GCM, v1 owner AAD) stored with every row.
const secretCipherVersion = 1
