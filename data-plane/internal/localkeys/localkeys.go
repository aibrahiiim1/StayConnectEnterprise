// Package localkeys manages appliance-local HMAC secrets for the durable throttle and keyed-HMAC OTP. Keys are
// crypto/rand (exactly 32 bytes) and persisted 0600 so they are stable across restarts (a new key per start would orphan
// buckets and reset enforcement); they never enter source control, the database, logs or arguments. Writes are atomic.
package localkeys

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
)

// MinKeyLen is the one key length: every consumer (sealbox, sign-in attempt seals, OTP HMAC, throttle) uses 32-byte
// keys, so a key of any other length (a 33-byte file with a trailing newline) is refused at startup, not at first use.
const MinKeyLen = 32

func checkLen(path string, b []byte) error {
	if len(b) != MinKeyLen {
		return fmt.Errorf("localkeys: %s must be exactly %d bytes (got %d)", path, MinKeyLen, len(b))
	}
	return nil
}

// readKeyFile checks the path (no symlink, regular, 0600, owned by root or this process) BEFORE opening it, and opens
// it without following a symlink swapped in meanwhile.
func readKeyFile(path string) ([]byte, error) {
	if err := checkFile(path); err != nil {
		return nil, err
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	b := make([]byte, MinKeyLen+1)
	n, err := io.ReadFull(f, b)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b[:n], nil
}

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
	if !ownerOK(fi) {
		return fmt.Errorf("localkeys: %s is not owned by root or this service (refused)", path)
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

// createExclKey writes a fresh key to a temporary file (0600, fsynced) and hard-links it into place: link never
// overwrites and is atomic, so a reader sees the whole key or no file, and a failed create leaves no short key behind.
// If another writer won, the persisted winner is returned so no two callers hold different keys.
func createExclKey(path string) (bool, []byte, error) {
	key := make([]byte, MinKeyLen)
	if _, err := rand.Read(key); err != nil {
		return false, nil, err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close() //nolint:errcheck
		return false, nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close() //nolint:errcheck
		return false, nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close() //nolint:errcheck
		return false, nil, err
	}
	if err := f.Close(); err != nil {
		return false, nil, err
	}
	if err := os.Link(tmp, path); err != nil {
		if os.IsExist(err) {
			b, rerr := readKeyFile(path)
			if rerr != nil {
				return false, nil, rerr
			}
			if lerr := checkLen(path, b); lerr != nil {
				return false, nil, lerr
			}
			return false, b, nil
		}
		return false, nil, err
	}
	if err := fsyncDir(dir); err != nil {
		return false, nil, err
	}
	return true, key, nil
}

// LoadExistingKey returns the key from path for normal runtime and never creates one: an absent file is a startup
// failure so a service cannot mint a replacement and reset enforcement.
func LoadExistingKey(path string) ([]byte, error) {
	b, err := readKeyFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("localkeys: key %s is absent (runtime is load-only; bootstrap it during deployment)", path)
		}
		return nil, err
	}
	if err := checkLen(path, b); err != nil {
		return nil, err
	}
	return b, nil
}

// CreateKeyIfAbsent is the bootstrap operation: it returns the existing key or creates one (race-safe, never
// overwrites). Never use it on the normal service-start path.
func CreateKeyIfAbsent(path string) ([]byte, error) {
	if b, err := readKeyFile(path); err == nil {
		if err := checkLen(path, b); err != nil {
			return nil, err
		}
		return b, nil
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
		p := filepath.Join(dir, e.Name())
		gen, err := strconv.Atoi(m[2])
		if err != nil || gen < 1 {
			return nil, fmt.Errorf("localkeys: %s names an invalid generation", p)
		}
		if _, dup := out[gen]; dup {
			// otp_hmac_01.key and otp_hmac_1.key both claim generation 1; which one wins must not depend on directory order.
			return nil, fmt.Errorf("localkeys: generation %d has more than one key file (%s)", gen, p)
		}
		b, err := readKeyFile(p)
		if err != nil {
			return nil, err
		}
		if err := checkLen(p, b); err != nil {
			return nil, err
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
	b, err := readKeyFile(path)
	if err == nil {
		if lerr := checkLen(path, b); lerr != nil {
			return nil, lerr
		}
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
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
