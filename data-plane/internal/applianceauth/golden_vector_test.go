package applianceauth

import (
	"crypto/ed25519"
	"testing"
)

// AD-61: this file is byte-identical in control-plane/internal/applianceauth and data-plane/internal/applianceauth.
// The key id (hex of the first 8 bytes of SHA-256 of the raw key) is what both sides put in the `kid` claim.

func goldenKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestGoldenVectorKeyID(t *testing.T) {
	if got := KeyID(goldenKey().Public().(ed25519.PublicKey)); got != goldenKeyID {
		t.Fatalf("key id = %s, want %s", got, goldenKeyID)
	}
}
