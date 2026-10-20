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
	"sync"
	"time"

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
	// ErrBusy means every password-hashing slot stayed taken for argonWait: the caller answers 503.
	ErrBusy = errors.New("password check busy")
)

// MEMORY IS BOUNDED BY CONCURRENCY, NOT BY THE RATE LIMIT. Each argon2id evaluation allocates m (64 MiB, or
// whatever the stored hash says). The per-address login limit does not cap how many run at once across
// addresses, so at most argonSlots run concurrently (256 MiB with the default parameters) and the rest wait
// up to argonWait, then fail with ErrBusy. A stored hash asking for more than maxHashMemoryKiB is refused
// outright rather than evaluated.
const (
	argonSlots       = 4
	maxHashMemoryKiB = 256 * 1024
)

var argonWait = 5 * time.Second

var argonSem = make(chan struct{}, argonSlots)

func acquireArgon() error {
	t := time.NewTimer(argonWait)
	defer t.Stop()
	select {
	case argonSem <- struct{}{}:
		return nil
	case <-t.C:
		return ErrBusy
	}
}

func releaseArgon() { <-argonSem }

// HashPassword returns the encoded argon2id hash of password.
func HashPassword(password string) (string, error) {
	if err := acquireArgon(); err != nil {
		return "", err
	}
	defer releaseArgon()
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
	if mem == 0 || mem > maxHashMemoryKiB || iter == 0 || iter > 16 || threads == 0 || len(want) == 0 || len(want) > 64 {
		return ErrInvalidHash
	}
	if err := acquireArgon(); err != nil {
		return err
	}
	defer releaseArgon()
	got := argon2.IDKey([]byte(password), salt, iter, mem, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return ErrMismatch
	}
	return nil
}

var (
	decoyOnce sync.Once
	decoyHash string
)

// SpendPasswordCheck does the work of checking a password that cannot match, so a sign-in for a name Central does not
// know (or an account that cannot sign in) takes as long as a wrong password for a real one: the response time must not
// say which names have an account. Errors are ignored; the caller answers "invalid credentials" either way.
func SpendPasswordCheck(password string) {
	decoyOnce.Do(func() { decoyHash, _ = HashPassword("decoy-" + time.Now().String()) })
	if decoyHash != "" {
		_ = VerifyPassword(decoyHash, password)
	}
}
