package sealbox

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestSealIsBoundToKeyDomainAndOwner(t *testing.T) {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	k, err := NewKey("notify-secret-aead:v1", raw)
	if err != nil {
		t.Fatal(err)
	}
	s, err := k.Seal([]byte("hunter2"), "tenant", "provider", "1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Open(s, "tenant", "provider", "1")
	if err != nil || !bytes.Equal(got, []byte("hunter2")) {
		t.Fatalf("round trip: %v %q", err, got)
	}
	if _, err := k.Open(s, "tenant", "provider", "2"); err == nil {
		t.Fatal("a row moved to another generation must not open")
	}
	other, _ := NewKey("payment-secret-aead:v1", raw)
	if _, err := other.Open(s, "tenant", "provider", "1"); err == nil {
		t.Fatal("the same key bytes under another domain must not open")
	}
	s.Ciphertext[0] ^= 1
	if _, err := k.Open(s, "tenant", "provider", "1"); err == nil {
		t.Fatal("tampering must not open")
	}
	if _, err := NewKey("x", raw[:16]); err == nil {
		t.Fatal("a short key must be refused")
	}
}
