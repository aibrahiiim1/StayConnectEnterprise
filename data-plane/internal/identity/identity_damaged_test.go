package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// P2 M6: with identity.json present and the key removed, nothing regenerates the identity and nothing is written.
func TestMissingKeyIsDamageNotFirstBoot(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if _, err := s.EnsureLocalKeypair(); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptApplianceID("44444444-4444-4444-4444-444444444444"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err := os.Remove(filepath.Join(dir, "ed25519.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadBound(); !errors.Is(err, ErrIdentityDamaged) {
		t.Fatalf("LoadBound: want ErrIdentityDamaged, got %v", err)
	}
	if _, err := s.EnsureLocalKeypair(); !errors.Is(err, ErrIdentityDamaged) {
		t.Fatalf("EnsureLocalKeypair: want ErrIdentityDamaged, got %v", err)
	}
	fc := &fakeCentral{resp: answer(201, `{"appliance_id":"55555555-5555-5555-5555-555555555555"}`)}
	if _, err := s.Register(context.Background(), "https://central.invalid", fc); !errors.Is(err, ErrIdentityDamaged) {
		t.Fatalf("Register: want ErrIdentityDamaged, got %v", err)
	}
	if fc.calls != 0 {
		t.Fatal("a damaged identity must not contact Central")
	}
	if _, err := os.Stat(filepath.Join(dir, "ed25519.key")); !os.IsNotExist(err) {
		t.Fatal("no new key may be written")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "identity.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("identity.json must not be rewritten")
	}
}

// P2 M6: a key that does not match the recorded public key is refused.
func TestMismatchedKeyIsRefused(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if _, err := s.EnsureLocalKeypair(); err != nil {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.WriteFile(filepath.Join(dir, "ed25519.key"), other, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureLocalKeypair(); !errors.Is(err, ErrIdentityDamaged) {
		t.Fatalf("want ErrIdentityDamaged, got %v", err)
	}
}

// P2 L20: a long error body is read whole up to the cap, not with one Read call.
func TestRegisterErrorBodyCapped(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	long := bytes.Repeat([]byte("y"), 4096)
	fc := &fakeCentral{resp: answer(409, string(long))}
	_, err := s.Register(context.Background(), "https://central.invalid", fc)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("want StatusError, got %v", err)
	}
	if len(se.Body) != 512 {
		t.Fatalf("error body %d bytes, want 512", len(se.Body))
	}
}
