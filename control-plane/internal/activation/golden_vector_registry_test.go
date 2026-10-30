package activation

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// This file and golden_vector_registry_values_test.go are byte-identical in control-plane/internal/activation and
// data-plane/internal/activation. They pin the signed bytes of a package that CARRIES the optional assignment_registry field; a package without
// it must still sign to exactly the pre-field bytes (TestGoldenVectorRequestAndPackage pins those, unchanged).

func goldenRegistryPackage() *Package {
	return &Package{SchemaVersion: SchemaVersion, PackageID: "pkg-1", RequestID: "req-1", RequestNonce: "n", ApplianceID: "app-1",
		Serial: "SC-1", IdentityKeyFpr: "aa", TenantID: "t", SiteID: "s", Assignment: json.RawMessage(`{"a":1}`),
		LicenseEnvelope: json.RawMessage(`{"l":1}`), Entitlements: json.RawMessage(`{}`), CABundlePEM: "CA", CentralBase: "https://c",
		IssuedAt: 100, ExpiresAt: 200, Nonce: "pn",
		AssignmentRegistry: json.RawMessage(`{"registry_version":3,"keys":[{"key_id":"k","public_key":"p","state":"active","added_at":"x"}],"signer_key_id":"r","signature":"s"}`)}
}

func TestGoldenVectorPackageWithRegistry(t *testing.T) {
	priv := goldenKey()
	pub := priv.Public().(ed25519.PublicKey)
	p := goldenRegistryPackage()
	SignPackage(priv, p)
	if got := string(packageSigningBytes(p)); got != goldenRegistryPackageBytes {
		t.Fatalf("registry package bytes drifted:\n got %s\nwant %s", got, goldenRegistryPackageBytes)
	}
	if p.Signature != goldenRegistryPackageSignature {
		t.Fatalf("registry package signature = %s, want %s", p.Signature, goldenRegistryPackageSignature)
	}
	if !VerifyPackage(pub, p) {
		t.Fatal("the golden registry package must verify")
	}
	// Editing one byte of the carried registry breaks the vendor signature.
	p.AssignmentRegistry = json.RawMessage(strings.Replace(string(p.AssignmentRegistry), `"registry_version":3`, `"registry_version":4`, 1))
	if VerifyPackage(pub, p) {
		t.Fatal("a registry edited after signing must not verify")
	}
	// Without the field the signed bytes contain no trace of it.
	q := goldenRegistryPackage()
	q.AssignmentRegistry = nil
	if strings.Contains(string(packageSigningBytes(q)), "assignment_registry") {
		t.Fatal("an absent registry must be omitted from the signed bytes")
	}
}
