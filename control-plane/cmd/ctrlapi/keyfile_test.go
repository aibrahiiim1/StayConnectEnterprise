package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Audit Phase 1 M17: key files are checked for mode and consistency; roles never share a key; --force keeps a backup.
func TestLoadEd25519FileChecks(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.key")
	if err := os.WriteFile(good, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := loadEd25519File(good); err != nil || !k.Equal(priv) {
		t.Fatalf("a 0600 consistent key must load: %v", err)
	}
	if runtime.GOOS == "linux" {
		wide := filepath.Join(dir, "wide.key")
		_ = os.WriteFile(wide, priv, 0o600)
		_ = os.Chmod(wide, 0o644)
		if _, err := loadEd25519File(wide); err == nil || !strings.Contains(err.Error(), "readable by other users") {
			t.Fatalf("a world-readable key must be refused: %v", err)
		}
	}
	bad := append([]byte{}, priv...)
	bad[40] ^= 0xff // public half no longer matches the seed
	badPath := filepath.Join(dir, "bad.key")
	_ = os.WriteFile(badPath, bad, 0o600)
	if _, err := loadEd25519File(badPath); err == nil || !strings.Contains(err.Error(), "not a consistent") {
		t.Fatalf("an inconsistent key must be refused: %v", err)
	}
	short := filepath.Join(dir, "short.key")
	_ = os.WriteFile(short, priv[:32], 0o600)
	if _, err := loadEd25519File(short); err == nil {
		t.Fatal("a 32-byte file must be refused")
	}
}

func TestDistinctKeys(t *testing.T) {
	_, a, _ := ed25519.GenerateKey(rand.Reader)
	_, b, _ := ed25519.GenerateKey(rand.Reader)
	if err := distinctKeys(map[string]ed25519.PrivateKey{"vendor licence": a, "assignment signing": b}); err != nil {
		t.Fatal(err)
	}
	if err := distinctKeys(map[string]ed25519.PrivateKey{"assignment signing": a, "registry root": a}); err == nil {
		t.Fatal("assignment and registry root sharing a key must be refused")
	}
	if err := distinctKeys(map[string]ed25519.PrivateKey{"vendor licence": b, "registry root": b, "assignment signing": nil}); err == nil {
		t.Fatal("vendor and registry root sharing a key must be refused")
	}
}

func TestWriteNewKeyFileKeepsBackupOnForce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.key")
	if err := writeNewKeyFile(p, []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeNewKeyFile(p, []byte("new"), false); err == nil {
		t.Fatal("without force an existing key must not be replaced")
	}
	if err := writeNewKeyFile(p, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatal("force must write the new key")
	}
	baks, _ := filepath.Glob(p + ".bak-*")
	if len(baks) != 1 {
		t.Fatalf("force must keep exactly one backup, got %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != "old" {
		t.Fatal("the backup must hold the old key")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new key mode %v, want 0600", fi.Mode().Perm())
	}
}
