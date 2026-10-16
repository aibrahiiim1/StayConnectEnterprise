// Package passwordhash is the ONE argon2id implementation for client-account passwords: hashing (edged for
// staff-created Personal accounts, scd for self-registered Email accounts and password resets) and the bounded
// verifier IAM-v2 authenticates with.
//
// WHY ONE PACKAGE. Personal accounts are hashed by edged and verified by IAM-v2; Email accounts are now hashed by
// scd and verified by the same IAM-v2 path. Two copies of "the" parameters is how a hash one side writes becomes a
// hash the other side cannot read -- or reads with a weaker bound -- so the format, the parameters and the
// verifier's resource limits live here, once.
//
// Format: PHC "$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>", 16-byte salt, 32-byte digest, raw standard base64
// -- the format every existing account hash already has.
package passwordhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The issuing parameters (the same as the cloud control plane and every account hash stored so far).
const (
	Memory  = 64 * 1024 // KiB
	Time    = 1
	Threads = 4
	SaltLen = 16
	KeyLen  = 32
)

// Verifier bounds, sized around the parameters above with headroom and capped so a malformed or hostile hash
// cannot exhaust memory or CPU, or overflow the uint8 thread count.
const (
	minMem    = 8      // KiB
	maxMem    = 262144 // 256 MiB
	minT      = 1
	maxT      = 10
	minP      = 1
	maxP      = 16
	minSalt   = 8
	maxSalt   = 64
	minDigest = 16
	maxDigest = 64
)

// ErrFormat reports a stored hash this verifier will not evaluate (wrong scheme, version or out-of-bounds
// parameters). It is never a statement about the password.
var ErrFormat = errors.New("passwordhash: unusable hash")

// Hash returns a new argon2id hash of pw with a fresh random salt.
func Hash(pw string) (string, error) {
	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, Time, Memory, Threads, KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", Memory, Time, Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether pw matches encoded, in constant time over the digest. Every parameter is bounded
// BEFORE argon2 runs; an unusable hash is ErrFormat (wrapped with the reason).
func Verify(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("%w: format", ErrFormat)
	}
	if parts[2] != "v=19" {
		return false, fmt.Errorf("%w: version", ErrFormat)
	}
	var m, t, p uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 {
		return false, fmt.Errorf("%w: params", ErrFormat)
	}
	if m < minMem || m > maxMem || t < minT || t > maxT || p < minP || p > maxP {
		return false, fmt.Errorf("%w: params out of range", ErrFormat)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < minSalt || len(salt) > maxSalt {
		return false, fmt.Errorf("%w: salt", ErrFormat)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < minDigest || len(want) > maxDigest {
		return false, fmt.Errorf("%w: digest", ErrFormat)
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummy is a valid hash at the issuing parameters, so BurnVerify costs what a real verification costs.
var dummy = func() string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("x"), salt, Time, Memory, Threads, KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", Memory, Time, Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}()

// Dummy returns the constant-work hash (for callers that must verify "something" when no account exists).
func Dummy() string { return dummy }

// BurnVerify performs one verification against the dummy hash and discards the answer. An unknown account, a
// disabled one or a method that is switched off must cost the same as a wrong password, or the response time
// says which accounts exist.
func BurnVerify(pw string) { _, _ = Verify(pw, dummy) }
