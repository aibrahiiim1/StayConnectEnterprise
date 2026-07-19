package pmsd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
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
		block, err := aes.NewCipher(key)
		if err != nil {
			return SecretMaterial{}, coded(CodeSecretDecryptFailed, err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil || len(nonce) != gcm.NonceSize() {
			return SecretMaterial{}, coded(CodeSecretDecryptFailed, ErrSecretDecrypt)
		}
		plain, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return SecretMaterial{}, coded(CodeSecretDecryptFailed, ErrSecretDecrypt)
		}
		return NewSecretMaterial(plain), nil
	}
}
