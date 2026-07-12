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

// Key separation: an assignment must be accepted ONLY when signed by a key in the
// appliance's assignment trust registry. The license / command / update / CA keys
// are never in that registry, so documents they sign are rejected as unknown signer.
func TestKeySeparationAndRotation(t *testing.T) {
	now := time.Now()

	assignPub, assignPriv, _ := ed25519.GenerateKey(rand.Reader) // dedicated assignment key
	licPub, licPriv, _ := ed25519.GenerateKey(rand.Reader)       // vendor LICENSE key
	_, cmdPriv, _ := ed25519.GenerateKey(rand.Reader)            // COMMAND key
	_, updPriv, _ := ed25519.GenerateKey(rand.Reader)            // UPDATE key
	_, unknownPriv, _ := ed25519.GenerateKey(rand.Reader)        // attacker

	reg := &Registry{}
	reg.AddOrRotate(trusted(assignPub, KeyActive))

	sign := func(priv ed25519.PrivateKey) *Document {
		d := mkDoc()
		Sign(priv, d)
		return d
	}

	// 1. dedicated assignment key -> ACCEPTED
	if r := AcceptForRegistry(reg, sign(assignPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("assignment signed by the dedicated key was rejected: %s", r)
	}
	// 2. license / command / update keys -> REJECTED (not in the registry)
	for name, priv := range map[string]ed25519.PrivateKey{
		"license": licPriv, "command": cmdPriv, "update": updPriv, "unknown": unknownPriv,
	} {
		if r := AcceptForRegistry(reg, sign(priv), "app-1", "SN-1", "fpr-1", 0, now); r == "" {
			t.Fatalf("assignment signed by the %s key was ACCEPTED — key separation broken", name)
		}
	}
	// Sanity: the license key really is a valid signer for its own docs, i.e. the
	// rejection above is about the registry, not a broken signature.
	d := sign(licPriv)
	if !Verify(licPub, d) {
		t.Fatal("license-signed doc failed its own signature check — test is wrong")
	}

	// 3. rotation: add a second active key, both accepted during overlap
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)
	reg.AddOrRotate(trusted(newPub, KeyActive))
	if r := AcceptForRegistry(reg, sign(assignPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("old key rejected during rotation overlap: %s", r)
	}
	if r := AcceptForRegistry(reg, sign(newPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("new key rejected during rotation overlap: %s", r)
	}
	// 4. retire the old key -> its assignments are now REJECTED, new key still works
	if !reg.Retire(KeyID(assignPub)) {
		t.Fatal("retire failed")
	}
	if r := AcceptForRegistry(reg, sign(assignPriv), "app-1", "SN-1", "fpr-1", 0, now); r == "" {
		t.Fatal("retired key was ACCEPTED — rotation policy broken")
	}
	if r := AcceptForRegistry(reg, sign(newPriv), "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("active key rejected after retiring the old one: %s", r)
	}
	// 5. empty registry -> nothing is trusted
	if r := AcceptForRegistry(&Registry{}, sign(newPriv), "app-1", "SN-1", "fpr-1", 0, now); r == "" {
		t.Fatal("empty registry accepted an assignment")
	}
}
