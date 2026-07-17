package localkeys

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateKeyStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "throttle.key")
	k1, err := LoadOrCreateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) < MinKeyLen {
		t.Fatalf("key too short: %d", len(k1))
	}
	// "restart": load again -> SAME key (no regeneration)
	k2, err := LoadOrCreateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("key must be stable across restart (not regenerated)")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file perms %o, want 0600", fi.Mode().Perm())
		}
	}
}

func TestLoadOrCreateKeyRejectsShort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "short.key")
	if err := os.WriteFile(p, []byte("tooshort"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(p); err == nil {
		t.Fatal("short key must be rejected")
	}
}

func TestInsecurePermsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX perms on windows")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "open.key")
	if err := os.WriteFile(p, make([]byte, MinKeyLen), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(p); err == nil {
		t.Fatal("group/world-readable key must be rejected")
	}
}

func TestGenerationKeysAndRotation(t *testing.T) {
	dir := t.TempDir()
	// ensure gen 1, then gen 2 (rotation): both files exist, distinct keys, stable
	k1, err := EnsureGeneration(dir, "otp_hmac", 1)
	if err != nil {
		t.Fatal(err)
	}
	k1again, _ := EnsureGeneration(dir, "otp_hmac", 1)
	if !bytes.Equal(k1, k1again) {
		t.Fatal("EnsureGeneration must be stable for an existing generation")
	}
	k2, err := EnsureGeneration(dir, "otp_hmac", 2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k1, k2) {
		t.Fatal("distinct generations must have distinct keys")
	}
	keys, err := LoadGenerationKeys(dir, "otp_hmac")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !bytes.Equal(keys[1], k1) || !bytes.Equal(keys[2], k2) {
		t.Fatalf("LoadGenerationKeys mismatch: %d gens", len(keys))
	}
}

func TestValidateOTPGenerationsFailClosed(t *testing.T) {
	keys := map[int][]byte{1: make([]byte, MinKeyLen), 2: make([]byte, MinKeyLen)}
	if err := ValidateOTPGenerations(keys, 2, []int{1, 2}); err != nil {
		t.Fatalf("valid config should pass: %v", err)
	}
	// active generation with no key -> refuse
	if err := ValidateOTPGenerations(keys, 3, nil); err == nil {
		t.Fatal("missing active-generation key must fail closed")
	}
	// a referenced (unexpired-OTP) generation with no key -> refuse
	if err := ValidateOTPGenerations(keys, 2, []int{1, 9}); err == nil {
		t.Fatal("missing referenced-generation key must fail closed")
	}
	// no keys at all -> refuse
	if err := ValidateOTPGenerations(map[int][]byte{}, 1, nil); err == nil {
		t.Fatal("no key material must fail closed")
	}
}
