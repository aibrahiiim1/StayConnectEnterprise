package pmsd

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

// TestAEAD_OwnerBoundAAD proves the AES-256-GCM secret decryption is bound to the EXACT owner via AAD: a
// ciphertext sealed for one (tenant,site,interface,generation) fails authentication under any other owner's
// AAD, and the nil/empty AAD used before is no longer accepted.
func TestAEAD_OwnerBoundAAD(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	iface := Interface{TenantID: "11111111-0000-4000-8000-000000000001", SiteID: "22222222-0000-4000-8000-000000000001", ID: "33333333-0000-4000-8000-000000000001"}
	sg := SecretGeneration{ID: "44444444-0000-4000-8000-000000000001"}
	aad := ownerAAD(iface, sg)

	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	plaintext := []byte("connector-secret")
	ct := gcm.Seal(nil, nonce, plaintext, aad)

	// correct owner AAD → opens
	got, err := aeadOpen(key, nonce, ct, aad)
	if err != nil || string(got) != string(plaintext) {
		t.Fatalf("owner-bound open failed: %v got=%q", err, got)
	}
	// a DIFFERENT interface's AAD → authentication failure
	other := iface
	other.ID = "33333333-0000-4000-8000-000000000009"
	if _, err := aeadOpen(key, nonce, ct, ownerAAD(other, sg)); err == nil {
		t.Fatal("a different interface AAD must fail authentication")
	}
	// a DIFFERENT secret generation's AAD → authentication failure
	otherSg := SecretGeneration{ID: "44444444-0000-4000-8000-000000000009"}
	if _, err := aeadOpen(key, nonce, ct, ownerAAD(iface, otherSg)); err == nil {
		t.Fatal("a different secret-generation AAD must fail authentication")
	}
	// the previously-used nil AAD → authentication failure (no longer accepted)
	if _, err := aeadOpen(key, nonce, ct, nil); err == nil {
		t.Fatal("nil AAD must no longer authenticate an owner-bound ciphertext")
	}
}

// TestOwnerAAD_Deterministic proves the AAD is stable and distinguishes each identity field (length-prefixed,
// no boundary ambiguity between adjacent fields).
func TestOwnerAAD_Deterministic(t *testing.T) {
	a := Interface{TenantID: "t", SiteID: "s", ID: "i"}
	sg := SecretGeneration{ID: "g"}
	if string(ownerAAD(a, sg)) != string(ownerAAD(a, sg)) {
		t.Fatal("AAD must be deterministic")
	}
	// field-boundary ambiguity check: ("ts","","i") must differ from ("t","s","i")
	amb := Interface{TenantID: "ts", SiteID: "", ID: "i"}
	if string(ownerAAD(a, sg)) == string(ownerAAD(amb, sg)) {
		t.Fatal("AAD must not be ambiguous across field boundaries")
	}
}
