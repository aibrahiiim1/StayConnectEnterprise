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
	"time"
)

// MinKeyLen is the minimum accepted key length in bytes.
const MinKeyLen = 32

// checkFile refuses a key path that is not a regular file (e.g. a symlink or device) and, on POSIX,
// that is group/world accessible.
func checkFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("localkeys: %s is a symlink (refused)", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("localkeys: %s is not a regular file (refused)", path)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("localkeys: %s has insecure permissions %o (want 0600)", path, fi.Mode().Perm())
	}
	return nil
}

// fsyncDir fsyncs a directory so a create/rename is durable across power loss / reboot.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck
	if err := d.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

// createExclKey atomically creates path (O_CREATE|O_EXCL, 0600) with a fresh crypto-random key, then
// fsyncs the file and its parent directory. Returns (created=true, key) on success, (false, existing)
// if another writer won the race (EEXIST) — in which case the single persisted winner is reloaded so
// no two callers ever retain different keys for the same path.
func createExclKey(path string) (bool, []byte, error) {
	key := make([]byte, MinKeyLen)
	if _, err := rand.Read(key); err != nil {
		return false, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another caller won the O_EXCL create. It may still be mid-write, so retry-read until the
			// file is complete (>= MinKeyLen) rather than returning a partial/empty key.
			for i := 0; i < 100; i++ {
				b, rerr := os.ReadFile(path)
				if rerr == nil && len(b) >= MinKeyLen {
					if perr := checkFile(path); perr != nil {
						return false, nil, perr
					}
					return false, b, nil
				}
				if rerr != nil && !os.IsNotExist(rerr) {
					return false, nil, rerr
				}
				time.Sleep(2 * time.Millisecond)
			}
			// If it exists but is genuinely short (not a mid-write), surface that.
			if b, rerr := os.ReadFile(path); rerr == nil && len(b) < MinKeyLen {
				return false, nil, fmt.Errorf("localkeys: %s too short (%d < %d)", path, len(b), MinKeyLen)
			}
			return false, nil, fmt.Errorf("localkeys: %s did not become readable", path)
		}
		return false, nil, err
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.Write(key); err != nil {
		return false, nil, err
	}
	if err := f.Sync(); err != nil {
		return false, nil, err
	}
	if err := fsyncDir(filepath.Dir(path)); err != nil {
		return false, nil, err
	}
	return true, key, nil
}

// LoadExistingKey returns a stable key from path for NORMAL RUNTIME. It NEVER creates a key: an
// absent file is a startup failure (so a service can never silently mint a replacement and reset
// enforcement). It rejects symlinks/non-regular files, insecure permissions and short keys.
func LoadExistingKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("localkeys: key %s is absent (runtime is load-only; bootstrap it during deployment)", path)
		}
		return nil, err
	}
	if perr := checkFile(path); perr != nil {
		return nil, perr
	}
	if len(b) < MinKeyLen {
		return nil, fmt.Errorf("localkeys: %s too short (%d < %d)", path, len(b), MinKeyLen)
	}
	return b, nil
}

// CreateKeyIfAbsent is the DEPLOYMENT/BOOTSTRAP operation: it returns the existing key or, if absent,
// creates a fresh crypto-random key (>= MinKeyLen, 0600, parent dir 0700, fsync'd, race-safe
// single-winner). It never overwrites an existing key. Use it only from a controlled bootstrap
// helper — never on the normal service-start path.
func CreateKeyIfAbsent(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if perr := checkFile(path); perr != nil {
			return nil, perr
		}
		if len(b) < MinKeyLen {
			return nil, fmt.Errorf("localkeys: %s too short (%d < %d)", path, len(b), MinKeyLen)
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, key, err := createExclKey(path) // race-safe: returns the single persisted winner
	return key, err
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
		if err := checkFile(p); err != nil {
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
	b, err := os.ReadFile(path)
	if err == nil {
		if perr := checkFile(path); perr != nil {
			return nil, perr
		}
		if len(b) < MinKeyLen {
			return nil, fmt.Errorf("localkeys: %s too short (%d < %d)", path, len(b), MinKeyLen)
		}
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, err // surface every read error other than not-exist
	}
	// Race-safe, no-overwrite creation: if a concurrent caller wins, reload and return the winner so
	// no two callers ever retain different keys for the same generation.
	_, key, cerr := createExclKey(path)
	return key, cerr
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
