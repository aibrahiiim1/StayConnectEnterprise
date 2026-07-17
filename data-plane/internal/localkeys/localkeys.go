// Package localkeys manages appliance-local HMAC secret material for the durable throttle (D4) and
// keyed-HMAC OTP (D7). Keys are:
//
//   - generated with crypto/rand (>= 32 bytes);
//   - persisted to protected 0600 files so they are STABLE across service restart and appliance
//     reboot (a service must not mint a new key on every start, which would orphan existing buckets
//     and reset enforcement);
//   - never placed in Git, the database, logs, command arguments, evidence, or reports.
//
// The write path is atomic (temp + fsync + rename + chmod 0600), matching the appliancecert pattern.
package localkeys

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
)

// MinKeyLen is the minimum accepted key length in bytes.
const MinKeyLen = 32

// writeAtomic writes data to path with mode, atomically (temp in same dir, fsync, rename, chmod).
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".key-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// checkPerms refuses a key file that is group/world accessible (skipped on Windows, which has no
// POSIX bits; production is Linux).
func checkPerms(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("localkeys: %s has insecure permissions %o (want 0600)", path, fi.Mode().Perm())
	}
	return nil
}

// LoadOrCreateKey returns a stable key from path, creating a fresh crypto-random key (>= MinKeyLen,
// 0600) if the file does not yet exist. The same key is returned on every subsequent call/restart.
func LoadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if perr := checkPerms(path); perr != nil {
			return nil, perr
		}
		if len(b) < MinKeyLen {
			return nil, fmt.Errorf("localkeys: %s too short (%d < %d)", path, len(b), MinKeyLen)
		}
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, MinKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := writeAtomic(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

var genFileRe = regexp.MustCompile(`^(.+?)_(\d+)\.key$`)

// LoadGenerationKeys reads all generation key files "<prefix>_<gen>.key" from dir into a
// generation->key map. Every key must be >= MinKeyLen and 0600. Missing dir is not an error (empty).
func LoadGenerationKeys(dir, prefix string) (map[int][]byte, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[int][]byte{}, nil
		}
		return nil, err
	}
	out := make(map[int][]byte)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		m := genFileRe.FindStringSubmatch(e.Name())
		if m == nil || m[1] != prefix {
			continue
		}
		gen, _ := strconv.Atoi(m[2])
		p := filepath.Join(dir, e.Name())
		if err := checkPerms(p); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if len(b) < MinKeyLen {
			return nil, fmt.Errorf("localkeys: %s too short (%d < %d)", p, len(b), MinKeyLen)
		}
		out[gen] = b
	}
	return out, nil
}

// EnsureGeneration creates the key file for generation gen if absent (crypto-random, 0600). Returns
// the key. Used for atomic rotation (create the new generation before flipping the DB active flag).
func EnsureGeneration(dir, prefix string, gen int) ([]byte, error) {
	if gen < 1 {
		return nil, fmt.Errorf("localkeys: invalid generation %d", gen)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%d.key", prefix, gen))
	if b, err := os.ReadFile(path); err == nil {
		if perr := checkPerms(path); perr != nil {
			return nil, perr
		}
		return b, nil
	}
	key := make([]byte, MinKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := writeAtomic(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// ValidateOTPGenerations fails closed when key material and database metadata disagree:
//   - the active generation (from DB) must have key material present;
//   - every generation still referenced by an unexpired OTP (referenced, from DB) must have key
//     material present (a missing referenced key means those OTPs can never verify -> refuse startup).
func ValidateOTPGenerations(keys map[int][]byte, activeGen int, referenced []int) error {
	if len(keys) == 0 {
		return fmt.Errorf("localkeys: no OTP key generations present")
	}
	if _, ok := keys[activeGen]; !ok {
		return fmt.Errorf("localkeys: active OTP generation %d has no key material", activeGen)
	}
	for _, g := range referenced {
		if _, ok := keys[g]; !ok {
			return fmt.Errorf("localkeys: OTP generation %d is referenced by unexpired OTPs but its key is missing", g)
		}
	}
	return nil
}

// SortedGenerations returns the generations present in keys, ascending.
func SortedGenerations(keys map[int][]byte) []int {
	gs := make([]int, 0, len(keys))
	for g := range keys {
		gs = append(gs, g)
	}
	sort.Ints(gs)
	return gs
}
