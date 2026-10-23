// Package localkeys manages appliance-local HMAC secrets for the durable throttle and keyed-HMAC OTP. Keys are
// crypto/rand (>= 32 bytes) and persisted 0600 so they are stable across restarts (a new key per start would orphan
// buckets and reset enforcement); they never enter source control, the database, logs or arguments. Writes are atomic.
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

const MinKeyLen = 32

// checkFile refuses a key path that is not a regular file and, on POSIX, one that is group/world accessible.
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

// createExclKey atomically creates path (O_EXCL, 0600) with a fresh key and fsyncs it and its directory; if another
// writer won the race the persisted winner is reloaded so no two callers hold different keys.
func createExclKey(path string) (bool, []byte, error) {
	key := make([]byte, MinKeyLen)
	if _, err := rand.Read(key); err != nil {
		return false, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another caller won the O_EXCL create and may still be mid-write: retry-read until the key is complete (~5s, as the
			// winner can be scheduling-starved under CI load).
			for i := 0; i < 1000; i++ {
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
				time.Sleep(5 * time.Millisecond)
			}
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

// LoadExistingKey returns the key from path for normal runtime and never creates one: an absent file is a startup
// failure so a service cannot mint a replacement and reset enforcement.
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

// CreateKeyIfAbsent is the bootstrap operation: it returns the existing key or creates one (race-safe, never
// overwrites). Never use it on the normal service-start path.
func CreateKeyIfAbsent(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) >= MinKeyLen {
			if perr := checkFile(path); perr != nil {
				return nil, perr
			}
			return b, nil
		}
		// The file exists but is short: a concurrent bootstrap won the O_EXCL create and is still writing. Not corrupt; fall
		// through to the race-safe create.
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, key, err := createExclKey(path)
	return key, err
}

var genFileRe = regexp.MustCompile(`^(.+?)_(\d+)\.key$`)

// LoadGenerationKeys reads the generation key files from dir into a generation->key map; each key must be
// >= MinKeyLen and 0600; a missing dir yields an empty map.
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

// EnsureGeneration creates the key for generation gen if absent, for atomic rotation (create before flipping the DB active flag).
func EnsureGeneration(dir, prefix string, gen int) ([]byte, error) {
	if gen < 1 {
		return nil, fmt.Errorf("localkeys: invalid generation %d", gen)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%d.key", prefix, gen))
	b, err := os.ReadFile(path)
	if err == nil && len(b) >= MinKeyLen {
		if perr := checkFile(path); perr != nil {
			return nil, perr
		}
		return b, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// Absent or present-but-short (a concurrent bootstrapper is mid-write): fall through to the race-safe no-overwrite
	// create, which retry-reads the winner.
	_, key, cerr := createExclKey(path)
	return key, cerr
}

// ValidateOTPGenerations fails closed when keys and DB metadata disagree: the active generation and every generation
// referenced by an unexpired OTP must have key material.
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

func SortedGenerations(keys map[int][]byte) []int {
	gs := make([]int, 0, len(keys))
	for g := range keys {
		gs = append(gs, g)
	}
	sort.Ints(gs)
	return gs
}
