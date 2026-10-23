package payment

// Provider secrets are sealed with AES-256-GCM under the appliance payment key and stored only as ciphertext. The AAD
// binds each ciphertext to tenant, site, account and generation, so a copied row fails authentication. Plaintext exists
// only in memory for one provider call and is never logged, returned by an API or sent to Central.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const CredentialCipherVersion = 1

// ErrCredentialSeal is the single, deliberately undetailed sealing/opening failure.
var ErrCredentialSeal = errors.New("payment: credential seal/open failed")

// The key id derives from the key itself, so a replaced key file is detected rather than silently used on old rows.
type PaymentKey struct {
	ID  string
	key []byte
}

func NewPaymentKey(k []byte) (PaymentKey, error) {
	if len(k) != 32 {
		return PaymentKey{}, ErrCredentialSeal
	}
	sum := sha256.Sum256(append([]byte("payment-dek-id:"), k...))
	return PaymentKey{ID: "pdek-" + hex.EncodeToString(sum[:8]), key: append([]byte(nil), k...)}, nil
}

func (k PaymentKey) Present() bool { return len(k.key) == 32 }

type SealedCredentials struct {
	GenerationID  string
	Ciphertext    []byte
	Nonce         []byte
	KeyID         string
	CipherVersion int
}

func credentialAAD(tenantID, siteID, accountID, generationID string) []byte {
	var b []byte
	add := func(s string) {
		b = append(b, byte(len(s)>>8), byte(len(s)))
		b = append(b, s...)
		b = append(b, 0x1f)
	}
	add("payment-secret-aead:v1")
	add(tenantID)
	add(siteID)
	add(accountID)
	add(generationID)
	return b
}

// generationID must be the id the row is stored under.
func SealCredentials(k PaymentKey, tenantID, siteID, accountID, generationID string, c Credentials) (SealedCredentials, error) {
	if !k.Present() || generationID == "" || accountID == "" {
		return SealedCredentials{}, ErrCredentialSeal
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return SealedCredentials{}, ErrCredentialSeal
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return SealedCredentials{}, ErrCredentialSeal
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return SealedCredentials{}, ErrCredentialSeal
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SealedCredentials{}, ErrCredentialSeal
	}
	ct := gcm.Seal(nil, nonce, plain, credentialAAD(tenantID, siteID, accountID, generationID))
	for i := range plain {
		plain[i] = 0
	}
	return SealedCredentials{GenerationID: generationID, Ciphertext: ct, Nonce: nonce, KeyID: k.ID,
		CipherVersion: CredentialCipherVersion}, nil
}

// A key-id mismatch, a moved row or tampering all fail the same.
func OpenCredentials(k PaymentKey, tenantID, siteID, accountID, generationID, keyID string, nonce, ciphertext []byte) (Credentials, error) {
	if !k.Present() || keyID != k.ID {
		return nil, ErrCredentialSeal
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, ErrCredentialSeal
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, ErrCredentialSeal
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, credentialAAD(tenantID, siteID, accountID, generationID))
	if err != nil {
		return nil, ErrCredentialSeal
	}
	var c Credentials
	err = json.Unmarshal(plain, &c)
	for i := range plain {
		plain[i] = 0
	}
	if err != nil {
		return nil, ErrCredentialSeal
	}
	return c, nil
}
