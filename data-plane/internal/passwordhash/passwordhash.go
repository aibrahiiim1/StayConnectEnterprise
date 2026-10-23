// Package passwordhash is the one argon2id implementation for client-account passwords, shared by edged and scd
// (hashing) and IAM-v2 (verifying) so format, parameters and verifier bounds never diverge. Format: PHC
// $argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>, 16-byte salt, 32-byte digest, raw standard base64.
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

const (
	Memory  = 64 * 1024
	Time    = 1
	Threads = 4
	SaltLen = 16
	KeyLen  = 32
)

// Verifier bounds stop a malformed or hostile hash exhausting memory or CPU, or overflowing the uint8 thread count.
const (
	minMem    = 8
	maxMem    = 262144
	minT      = 1
	maxT      = 10
	minP      = 1
	maxP      = 16
	minSalt   = 8
	maxSalt   = 64
	minDigest = 16
	maxDigest = 64
)

// ErrFormat is a hash this verifier will not evaluate; never a statement about the password.
var ErrFormat = errors.New("passwordhash: unusable hash")

func Hash(pw string) (string, error) {
	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, Time, Memory, Threads, KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", Memory, Time, Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify is constant-time over the digest and bounds every parameter before argon2 runs.
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

var dummy = func() string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("x"), salt, Time, Memory, Threads, KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", Memory, Time, Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}()

func Dummy() string { return dummy }

// BurnVerify makes unknown or disabled accounts cost the same as a wrong password, so timing does not reveal which
// accounts exist.
func BurnVerify(pw string) { _, _ = Verify(pw, dummy) }
