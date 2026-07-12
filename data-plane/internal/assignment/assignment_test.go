package assignment

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func mkDoc() *Document {
	return &Document{
		AssignmentID: "a1", ApplianceID: "app-1", IdentityKeyFpr: "fpr-1", Serial: "SN-1",
		TenantID: "ten-1", SiteID: "site-1", TenantName: "T", SiteName: "S",
		Version: 2, State: StateAssigned, IssuedAt: time.Now().Unix(), ExpiresAt: 0,
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := mkDoc()
	Sign(priv, d)
	if d.SignerKeyID != KeyID(pub) {
		t.Fatal("signer key id mismatch")
	}
	if !Verify(pub, d) {
		t.Fatal("valid signature rejected")
	}
	// tamper
	d.SiteID = "site-2"
	if Verify(pub, d) {
		t.Fatal("modified document verified")
	}
}

func TestAcceptForMatrix(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()

	base := func() *Document { d := mkDoc(); Sign(priv, d); return d }

	if r := AcceptFor(pub, base(), "app-1", "SN-1", "fpr-1", 1, now); r != "" {
		t.Fatalf("valid assignment rejected: %s", r)
	}
	if r := AcceptFor(otherPub, base(), "app-1", "SN-1", "fpr-1", 1, now); r == "" {
		t.Fatal("unknown signer accepted")
	}
	if r := AcceptFor(pub, base(), "app-OTHER", "SN-1", "fpr-1", 1, now); r == "" {
		t.Fatal("wrong appliance accepted")
	}
	if r := AcceptFor(pub, base(), "app-1", "SN-1", "fpr-OTHER", 1, now); r == "" {
		t.Fatal("wrong identity fpr accepted")
	}
	// replay/superseded: haveVersion >= doc version
	if r := AcceptFor(pub, base(), "app-1", "SN-1", "fpr-1", 2, now); r == "" {
		t.Fatal("superseded (equal) version accepted")
	}
	if r := AcceptFor(pub, base(), "app-1", "SN-1", "fpr-1", 5, now); r == "" {
		t.Fatal("older version accepted")
	}
	// expired
	exp := mkDoc()
	exp.ExpiresAt = now.Add(-time.Hour).Unix()
	Sign(priv, exp)
	if r := AcceptFor(pub, exp, "app-1", "SN-1", "fpr-1", 1, now); r == "" {
		t.Fatal("expired assignment accepted")
	}
	// assigned without tenant/site
	bad := mkDoc()
	bad.TenantID = ""
	Sign(priv, bad)
	if r := AcceptFor(pub, bad, "app-1", "SN-1", "fpr-1", 1, now); r == "" {
		t.Fatal("assigned doc missing tenant accepted")
	}
}
