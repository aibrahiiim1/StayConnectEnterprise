package offline

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func mkPkg(pub ed25519.PublicKey) *Package {
	return &Package{
		PackageID: "pkg-1", ApplianceID: "app-1", Serial: "SER-1",
		IdentityKeyFpr: "idfpr", MTLSKeyFpr: "mtfpr", TenantID: "t", SiteID: "s",
		LicenseEnvelope: json.RawMessage(`{"lic":1}`), Entitlements: json.RawMessage(`{}`),
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(), Nonce: "n1",
	}
}

func TestOfflineAcceptMatrix(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()

	// intended appliance accepts
	p := mkPkg(pub)
	Sign(priv, p)
	if r := AcceptFor(pub, p, "app-1", "SER-1", "idfpr", "mtfpr", now); r != "" {
		t.Fatalf("intended appliance rejected: %s", r)
	}

	// different appliance rejects
	if r := AcceptFor(pub, p, "app-2", "SER-1", "idfpr", "mtfpr", now); r == "" {
		t.Fatal("different appliance was accepted")
	}

	// serial / identity / mtls mismatch reject
	if AcceptFor(pub, p, "app-1", "OTHER", "idfpr", "mtfpr", now) == "" {
		t.Fatal("serial mismatch accepted")
	}
	if AcceptFor(pub, p, "app-1", "SER-1", "WRONG", "mtfpr", now) == "" {
		t.Fatal("identity fpr mismatch accepted")
	}

	// modified package rejects (tamper after signing)
	p2 := mkPkg(pub)
	Sign(priv, p2)
	p2.TenantID = "tamper"
	if AcceptFor(pub, p2, "app-1", "SER-1", "idfpr", "mtfpr", now) == "" {
		t.Fatal("modified package accepted")
	}

	// expired rejects
	p3 := mkPkg(pub)
	p3.ExpiresAt = now.Add(-time.Hour).Unix()
	Sign(priv, p3)
	if AcceptFor(pub, p3, "app-1", "SER-1", "idfpr", "mtfpr", now) == "" {
		t.Fatal("expired package accepted")
	}

	// wrong signer rejects
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	p4 := mkPkg(pub)
	Sign(priv2, p4)
	if AcceptFor(pub, p4, "app-1", "SER-1", "idfpr", "mtfpr", now) == "" {
		t.Fatal("wrong-signer package accepted")
	}
}
