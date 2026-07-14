// Package auth — password hashing + verification with argon2id.
//
// Encoded form:
//
//	$argon2id$v=19$m=<mem>,t=<time>,p=<threads>$<b64salt>$<b64hash>
//
// Parameters (OWASP 2023 second-preference row, fine for web-admin logins):
//
//	m = 64 MiB, t = 1, p = 4, salt = 16 B, hash = 32 B
package auth

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
	pwMemoryKiB  uint32 = 64 * 1024
	pwIterations uint32 = 1
	pwThreads    uint8  = 4
	pwSaltLen           = 16
	pwHashLen    uint32 = 32
)

var (
	ErrInvalidHash = errors.New("invalid argon2id encoded hash")
	ErrMismatch    = errors.New("password mismatch")
)

// HashPassword returns the encoded argon2id hash of password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, pwSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, pwIterations, pwMemoryKiB, pwThreads, pwHashLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, pwMemoryKiB, pwIterations, pwThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks password against encoded. Returns nil on match.
func VerifyPassword(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return ErrInvalidHash
	}
	var mem, iter uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &threads); err != nil {
		return ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return ErrInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, iter, mem, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return ErrMismatch
	}
	return nil
}
