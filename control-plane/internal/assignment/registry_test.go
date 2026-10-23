package assignment

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func trusted(pub ed25519.PublicKey, state string) TrustedKey {
	return TrustedKey{
		KeyID:     KeyID(pub),
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		State:     state,
	}
}

// Only a key in the assignment trust registry is accepted; the license, command and update keys are rejected as unknown signers.
func TestKeySeparation(t *testing.T) {
	now := time.Now()
	assignPub, assignPriv, _ := ed25519.GenerateKey(rand.Reader)
	licPub, licPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, cmdPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, updPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, unknownPriv, _ := ed25519.GenerateKey(rand.Reader)

	reg := &Registry{}
	reg.AddOrRotate(trusted(assignPub, KeyActive))

	sign := func(priv ed25519.PrivateKey) *Document { d := mkDoc(); Sign(priv, d); return d }

	if r := AcceptForRegistry(reg, sign(assignPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("dedicated key rejected: %s", r)
	}
	for name, priv := range map[string]ed25519.PrivateKey{
		"license": licPriv, "command": cmdPriv, "update": updPriv, "unknown": unknownPriv,
	} {
		if r := AcceptForRegistry(reg, sign(priv), "app-1", "SN-1", "fpr-1", 0, now); r == "" {
			t.Fatalf("assignment signed by the %s key was ACCEPTED — key separation broken", name)
		}
	}
	if !Verify(licPub, sign(licPriv)) {
		t.Fatal("license-signed doc failed its own signature check — test is wrong")
	}
}

func TestKeyLifecycleStates(t *testing.T) {
	now := time.Now()
	oldPub, oldPriv, _ := ed25519.GenerateKey(rand.Reader)
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)

	reg := &Registry{}
	reg.AddOrRotate(trusted(oldPub, KeyActive))
	sign := func(priv ed25519.PrivateKey) *Document { d := mkDoc(); Sign(priv, d); return d }

	if !CanSign(KeyActive) || !CanVerify(KeyActive) {
		t.Fatal("active must sign and verify")
	}
	if CanSign(KeyVerifyOnly) {
		t.Fatal("verify_only must NOT be allowed to sign")
	}
	if !CanVerify(KeyVerifyOnly) {
		t.Fatal("verify_only must still verify")
	}
	if CanSign(KeyRevoked) || CanVerify(KeyRevoked) {
		t.Fatal("revoked must neither sign nor verify")
	}

	reg.AddOrRotate(trusted(newPub, KeyActive))
	if r := AcceptForRegistry(reg, sign(oldPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("old key rejected during overlap: %s", r)
	}

	if !reg.VerifyOnly(KeyID(oldPub)) {
		t.Fatal("VerifyOnly failed")
	}
	if r := AcceptForRegistry(reg, sign(oldPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("verify_only key must still verify an existing assignment, got: %s", r)
	}
	if r := AcceptForRegistry(reg, sign(newPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("new active key rejected: %s", r)
	}

	if !reg.Revoke(KeyID(oldPub)) {
		t.Fatal("Revoke failed")
	}
	if r := AcceptForRegistry(reg, sign(oldPriv), "app-1", "SN-1", "fpr-1", 0, now); r == "" {
		t.Fatal("revoked signer was ACCEPTED")
	}
	if r := AcceptForRegistry(reg, sign(newPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("active key rejected after revoking the old one: %s", r)
	}

	if r := AcceptForRegistry(&Registry{}, sign(newPriv), "app-1", "SN-1", "fpr-1", 0, now); r == "" {
		t.Fatal("empty registry accepted an assignment")
	}
}
