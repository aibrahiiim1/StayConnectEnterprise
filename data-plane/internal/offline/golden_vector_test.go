package offline

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
)

// AD-61: this file is byte-identical in control-plane/internal/offline and data-plane/internal/offline. The signed
// bytes, the key id encoding (hex of the first 8 bytes of SHA-256) and the signature (Ed25519 is deterministic) are
// pinned, so a one-sided change fails here on the side that did not change.

func goldenKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestGoldenVectorSignedBytesAndSignature(t *testing.T) {
	priv := goldenKey()
	p := &Package{PackageID: "pkg-1", ApplianceID: "app-1", Serial: "SC-1", IdentityKeyFpr: "aa", MTLSKeyFpr: "bb",
		TenantID: "t", SiteID: "s", LicenseEnvelope: json.RawMessage(`{"x":1}`), Entitlements: json.RawMessage(`{}`),
		CABundlePEM: "CA", IssuedAt: 100, ExpiresAt: 200, Nonce: "n"}
	Sign(priv, p)
	if got := KeyID(priv.Public().(ed25519.PublicKey)); got != goldenKeyID {
		t.Fatalf("key id = %s, want %s", got, goldenKeyID)
	}
	if got := string(signingBytes(p)); got != goldenSigningBytes {
		t.Fatalf("signed bytes drifted:\n got %s\nwant %s", got, goldenSigningBytes)
	}
	if p.Signature != goldenSignature {
		t.Fatalf("signature = %s, want %s", p.Signature, goldenSignature)
	}
	if !Verify(priv.Public().(ed25519.PublicKey), p) {
		t.Fatal("the golden package must verify")
	}
}
