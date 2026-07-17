package localkeys

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestKeyBootstrapAndRuntimeLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "throttle.key") // parent dir created by bootstrap
	// bootstrap creates one key
	k1, err := CreateKeyIfAbsent(p)
	if err != nil || len(k1) < MinKeyLen {
		t.Fatalf("bootstrap: len=%d err=%v", len(k1), err)
	}
	// repeated bootstrap returns the SAME key (never overwrites)
	k2, err := CreateKeyIfAbsent(p)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatal("repeated bootstrap must return the same key")
	}
	// runtime load-only returns the same key
	k3, err := LoadExistingKey(p)
	if err != nil || !bytes.Equal(k1, k3) {
		t.Fatalf("runtime load must return the bootstrapped key: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key perms %o, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(p))
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("key dir perms %o, want 0700", di.Mode().Perm())
		}
	}
}

func TestRuntimeLoadMissingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// runtime load of an absent key must FAIL (never regenerate — would reset throttle enforcement)
	if _, err := LoadExistingKey(filepath.Join(dir, "absent.key")); err == nil {
		t.Fatal("runtime load of absent key must fail closed")
	}
}

func TestLoadExistingKeyRejectsShort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "short.key")
	if err := os.WriteFile(p, []byte("tooshort"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExistingKey(p); err == nil {
		t.Fatal("short key must be rejected")
	}
}

func TestCreateKeyIfAbsentConcurrentSingleWinner(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "throttle.key")
	const n = 20
	keys := make([][]byte, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := CreateKeyIfAbsent(p)
			if err != nil {
				t.Errorf("g%d: %v", i, err)
				return
			}
			keys[i] = k
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if keys[i] == nil || !bytes.Equal(keys[0], keys[i]) {
			t.Fatalf("concurrent bootstrap produced different keys (i=%d)", i)
		}
	}
}

func TestInsecurePermsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX perms on windows (enforced on the Linux appliance)")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "open.key")
	if err := os.WriteFile(p, make([]byte, MinKeyLen), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExistingKey(p); err == nil {
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

func TestEnsureGenerationConcurrentSingleWinner(t *testing.T) {
	dir := t.TempDir()
	const n = 20
	keys := make([][]byte, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := EnsureGeneration(dir, "otp_hmac", 1)
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			keys[i] = k
		}(i)
	}
	wg.Wait()
	// every caller must retain the SAME single persisted winner
	for i := 1; i < n; i++ {
		if keys[i] == nil || !bytes.Equal(keys[0], keys[i]) {
			t.Fatalf("concurrent EnsureGeneration returned different keys (i=%d)", i)
		}
	}
}

func TestEnsureGenerationRejectsShortExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "otp_hmac_1.key")
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGeneration(dir, "otp_hmac", 1); err == nil {
		t.Fatal("existing short generation key must be rejected")
	}
}

func TestEnsureGenerationRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink perms differ on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, make([]byte, MinKeyLen), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "otp_hmac_1.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGeneration(dir, "otp_hmac", 1); err == nil {
		t.Fatal("symlinked generation key must be refused")
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
