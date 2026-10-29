package activation

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// AD-61: this file is byte-identical in control-plane/internal/activation and data-plane/internal/activation. The
// request and package signed bytes, the key id encoding (base64url of the first 8 bytes of SHA-256, unlike the hex
// used elsewhere) and the signatures are pinned.

func goldenKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestGoldenVectorRequestAndPackage(t *testing.T) {
	priv := goldenKey()
	pub := priv.Public().(ed25519.PublicKey)
	if got := KeyID(pub); got != goldenKeyID {
		t.Fatalf("key id = %s, want %s", got, goldenKeyID)
	}
	req := &Request{SchemaVersion: SchemaVersion, RequestID: "req-1", Serial: "SC-1", PublicKey: base64.RawStdEncoding.EncodeToString(pub), WANMAC: "00:11:22:33:44:55",
		LANMAC: "00:11:22:33:44:66", HardwareFpr: "abcdef", Hostname: "onegate", Model: "m", CreatedAt: 100, Nonce: "n",
		HoldsCustomerID: "c1"}
	SignRequest(priv, req)
	if got := string(requestSigningBytes(req)); got != goldenRequestBytes {
		t.Fatalf("request bytes drifted:\n got %s\nwant %s", got, goldenRequestBytes)
	}
	if req.Signature != goldenRequestSignature {
		t.Fatalf("request signature = %s, want %s", req.Signature, goldenRequestSignature)
	}
	if !VerifyRequest(req) {
		t.Fatal("the golden request must verify")
	}
	p := &Package{SchemaVersion: SchemaVersion, PackageID: "pkg-1", RequestID: "req-1", RequestNonce: "n", ApplianceID: "app-1",
		Serial: "SC-1", IdentityKeyFpr: "aa", TenantID: "t", SiteID: "s", Assignment: json.RawMessage(`{"a":1}`),
		LicenseEnvelope: json.RawMessage(`{"l":1}`), Entitlements: json.RawMessage(`{}`), CABundlePEM: "CA", CentralBase: "https://c",
		IssuedAt: 100, ExpiresAt: 200, Nonce: "pn"}
	SignPackage(priv, p)
	if got := string(packageSigningBytes(p)); got != goldenPackageBytes {
		t.Fatalf("package bytes drifted:\n got %s\nwant %s", got, goldenPackageBytes)
	}
	if p.Signature != goldenPackageSignature {
		t.Fatalf("package signature = %s, want %s", p.Signature, goldenPackageSignature)
	}
	if !VerifyPackage(pub, p) {
		t.Fatal("the golden package must verify")
	}
}
