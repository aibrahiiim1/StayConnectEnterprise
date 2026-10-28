package assignment

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// P2 M12: a clock that drifts behind the current registry's NotBefore keeps the current registry; trust never moves back to the previous one, which
// may still list a key the current one revoked.
func TestTrustedKeepsCurrentWhenClockIsBehind(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	revokedPub, _, _ := ed25519.GenerateKey(rand.Reader)
	keptPub, _, _ := ed25519.GenerateKey(rand.Reader)
	s := &RegistryStore{Path: filepath.Join(dir, "reg.json"), RootPub: rootPub}
	now := time.Now()

	v2 := &SignedRegistry{RegistryVersion: 2, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Hour).Unix(),
		Keys: []TrustedKey{trusted(revokedPub, KeyActive), trusted(keptPub, KeyActive)}}
	SignRegistry(rootPriv, v2)
	if ok, r := s.Adopt(v2, now); !ok {
		t.Fatalf("v2: %s", r)
	}
	// v3 revokes a key; it was issued "an hour from now" by an appliance clock that has since drifted back.
	future := now.Add(time.Hour)
	v3 := &SignedRegistry{RegistryVersion: 3, IssuedAt: future.Unix(), NotBefore: future.Add(-5 * time.Minute).Unix(),
		Keys: []TrustedKey{trusted(revokedPub, KeyRevoked), trusted(keptPub, KeyActive)}}
	SignRegistry(rootPriv, v3)
	if ok, r := s.Adopt(v3, future); !ok {
		t.Fatalf("v3: %s", r)
	}

	reg := s.Trusted()
	if reg == nil {
		t.Fatal("Trusted returned nothing with a validly signed current registry")
	}
	for _, k := range reg.Keys {
		if k.KeyID == KeyID(revokedPub) && k.State != KeyRevoked {
			t.Fatal("a key the current registry revoked is trusted again through the previous registry")
		}
	}
}

// P2 M12: an expired current registry does not fall back to the (older) previous one either.
func TestTrustedNoFallbackOnExpiredCurrent(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	s := &RegistryStore{Path: filepath.Join(dir, "reg.json"), RootPub: rootPub}
	now := time.Now()
	v2 := &SignedRegistry{RegistryVersion: 2, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Hour).Unix()}
	SignRegistry(rootPriv, v2)
	s.Adopt(v2, now)
	v3 := &SignedRegistry{RegistryVersion: 3, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Hour).Unix(), NotAfter: now.Add(-time.Minute).Unix()}
	SignRegistry(rootPriv, v3)
	if ok, r := s.Adopt(v3, now.Add(-30*time.Minute)); !ok {
		t.Fatalf("v3: %s", r)
	}
	if s.Trusted() != nil {
		t.Fatal("an expired current registry must not fall back to the previous one")
	}
}

// P2 L17: once a signed registry was adopted, a missing registry file never re-enables the unsigned plain trust file.
func TestPlainTrustRefusedAfterSignedRegistryAdopted(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	signerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	p := Paths{
		Dir: dir, RegistryPath: filepath.Join(dir, "registry.json"),
		RegistryRootPath: filepath.Join(dir, "root.pub"), TrustPath: filepath.Join(dir, "trust.json"),
	}
	if err := os.WriteFile(p.RegistryRootPath, rootPub, 0o644); err != nil {
		t.Fatal(err)
	}
	plain := &Registry{Keys: []TrustedKey{trusted(signerPub, KeyActive)}}
	if err := plain.Save(p.TrustPath); err != nil {
		t.Fatal(err)
	}
	if _, src := TrustedRegistry(p, nil); src != "legacy-plain" {
		t.Fatalf("pre-rollout: want legacy-plain, got %s", src)
	}
	now := time.Now()
	sr := &SignedRegistry{RegistryVersion: 1, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Minute).Unix(),
		Keys: []TrustedKey{trusted(signerPub, KeyActive)}}
	SignRegistry(rootPriv, sr)
	if ok, r := (&RegistryStore{Path: p.RegistryPath, RootPub: rootPub}).Adopt(sr, now); !ok {
		t.Fatal(r)
	}
	if err := os.Remove(p.RegistryPath); err != nil {
		t.Fatal(err)
	}
	if reg, src := TrustedRegistry(p, nil); reg != nil || src != "none" {
		t.Fatalf("after adoption and loss of the signed file: want none, got %s", src)
	}
}
