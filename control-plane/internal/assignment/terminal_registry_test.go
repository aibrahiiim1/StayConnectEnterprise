package assignment

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func TestAckRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	a := &Ack{ApplianceID: "app-1", Version: 7, TerminalState: StateRevoked, Fingerprint: "fp", AdoptedAt: 123}
	SignAck(priv, a)
	if !VerifyAck(pub, a) {
		t.Fatal("valid ack rejected")
	}
	if VerifyAck(other, a) {
		t.Fatal("ack verified under the wrong key")
	}
	a.Version = 8 // tamper
	if VerifyAck(pub, a) {
		t.Fatal("tampered ack verified")
	}
}

func TestSignedRegistryVerify(t *testing.T) {
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	badPub, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	kpub, _, _ := ed25519.GenerateKey(rand.Reader)
	sr := &SignedRegistry{
		RegistryVersion: 3, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Minute).Unix(),
		Keys: []TrustedKey{{KeyID: KeyID(kpub), PublicKey: base64.StdEncoding.EncodeToString(kpub), State: KeyActive}},
	}
	SignRegistry(rootPriv, sr)
	if r := VerifyRegistry(rootPub, sr, now); r != "" {
		t.Fatalf("valid registry rejected: %s", r)
	}
	if r := VerifyRegistry(badPub, sr, now); r == "" {
		t.Fatal("registry verified under an unknown root")
	}
	sr.RegistryVersion = 4 // tamper after signing
	if r := VerifyRegistry(rootPub, sr, now); r == "" {
		t.Fatal("modified registry verified")
	}
}

func TestRegistryStoreRollback(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	badRootPub, badRootPriv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	s := &RegistryStore{Path: dir + "/reg.json", RootPub: rootPub}

	mk := func(ver int64, signer ed25519.PrivateKey) *SignedRegistry {
		sr := &SignedRegistry{RegistryVersion: ver, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Minute).Unix()}
		SignRegistry(signer, sr)
		return sr
	}

	if ok, r := s.Adopt(mk(2, rootPriv), now); !ok || r != "" {
		t.Fatalf("v2 not adopted: ok=%v r=%s", ok, r)
	}
	if ok, _ := s.Adopt(mk(3, rootPriv), now); !ok {
		t.Fatal("v3 not adopted")
	}
	if s.CurrentVersion() != 3 {
		t.Fatalf("want current v3, got %d", s.CurrentVersion())
	}
	if ok, r := s.Adopt(mk(2, rootPriv), now); ok || r == "" {
		t.Fatal("rollback to v2 accepted")
	}
	if s.CurrentVersion() != 3 {
		t.Fatal("rollback replaced current registry")
	}
	if ok, r := s.Adopt(mk(4, badRootPriv), now); ok || r == "" {
		t.Fatalf("registry from unknown signer accepted: ok=%v r=%s", ok, r)
	}
	_ = badRootPub
	if s.CurrentVersion() != 3 {
		t.Fatal("bad registry replaced current")
	}
	s2 := &RegistryStore{Path: dir + "/reg.json", RootPub: rootPub}
	if s2.Current() == nil || s2.CurrentVersion() != 3 {
		t.Fatal("last-known-good registry not persisted across reload")
	}
}

func TestRegistryTrustedFallback(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	s := &RegistryStore{Path: dir + "/reg.json", RootPub: rootPub}
	mk := func(ver int64) *SignedRegistry {
		sr := &SignedRegistry{RegistryVersion: ver, IssuedAt: now.Unix(), NotBefore: now.Add(-time.Minute).Unix()}
		SignRegistry(rootPriv, sr)
		return sr
	}
	if s.FileExists() {
		t.Fatal("FileExists true before any write")
	}
	s.Adopt(mk(2), now)
	s.Adopt(mk(3), now) // current=3, previous=2
	if !s.FileExists() {
		t.Fatal("FileExists false after write")
	}
	if s.Trusted() == nil {
		t.Fatal("Trusted nil with valid current")
	}
	flip := func(sig string) string {
		if sig == "" {
			return "A"
		}
		c := byte('A')
		if sig[0] == 'A' {
			c = 'B'
		}
		return string(c) + sig[1:]
	}
	rec := s.Load()
	rec.Current.Signature = flip(rec.Current.Signature)
	if err := s.save(rec); err != nil {
		t.Fatal(err)
	}
	if VerifyRegistry(rootPub, rec.Current, now) == "" {
		t.Fatal("tampered current still verifies")
	}
	if s.Trusted() == nil {
		t.Fatal("Trusted did not fall back to previous last-known-good")
	}
	rec = s.Load()
	rec.Previous.Signature = flip(rec.Previous.Signature)
	s.save(rec)
	if s.Trusted() != nil {
		t.Fatal("Trusted returned a registry when neither current nor previous verifies")
	}
	if !s.FileExists() {
		t.Fatal("file vanished")
	}
}
