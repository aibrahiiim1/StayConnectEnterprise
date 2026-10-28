package localkeys

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// P2 L3: a key that is not exactly 32 bytes (a trailing newline) is refused at load.
func TestKeyMustBeExactly32Bytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "k.key")
	if err := os.WriteFile(p, append(make([]byte, 32), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExistingKey(p); err == nil || !strings.Contains(err.Error(), "exactly 32") {
		t.Fatalf("33-byte key: %v", err)
	}
}

// P2 L5: two files for one generation, or generation 0, are refused.
func TestDuplicateOrZeroGenerationRefused(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"otp_hmac_01.key", "otp_hmac_1.key"} {
		if err := os.WriteFile(filepath.Join(dir, n), make([]byte, 32), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadGenerationKeys(dir, "otp_hmac"); err == nil {
		t.Fatal("duplicate generation accepted")
	}
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "otp_hmac_0.key"), make([]byte, 32), 0o600)
	if _, err := LoadGenerationKeys(dir2, "otp_hmac"); err == nil {
		t.Fatal("generation 0 accepted")
	}
}

// P2 L4: a symlink is refused without being followed.
func TestSymlinkRefusedBeforeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	_ = os.WriteFile(target, make([]byte, 32), 0o600)
	link := filepath.Join(dir, "k.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExistingKey(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}
}

// P2 L2: concurrent creators agree on one key and leave no temporary or short file behind.
func TestConcurrentCreateLeavesOneCompleteKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "k.key")
	var wg sync.WaitGroup
	keys := make([][]byte, 16)
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := CreateKeyIfAbsent(p)
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		}(i)
	}
	wg.Wait()
	for _, k := range keys {
		if string(k) != string(keys[0]) || len(k) != 32 {
			t.Fatal("creators disagree on the key")
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("stray files left: %d entries", len(ents))
	}
}
