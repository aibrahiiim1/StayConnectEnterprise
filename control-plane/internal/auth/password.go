// Package auth: passwords use argon2id (OWASP 2023 second-preference parameters), encoded as
// $argon2id$v=19$m=<mem>,t=<time>,p=<threads>$<b64salt>$<b64hash>.
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

// Memory is bounded by concurrency, not the rate limit: the per-address limit does not cap parallel evaluations
// across addresses, so at most argonSlots run at once and the rest wait up to argonWait, then fail with ErrBusy.
// A stored hash asking for more than maxHashMemoryKiB is refused outright.
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
	decoyMu   sync.Mutex
	decoyHash string
)

// SpendPasswordCheck does the work of a check that cannot match, so a sign-in for an unknown name takes as long as a
// wrong password for a real one and timing does not reveal which names have an account. It returns ErrBusy exactly
// when a real check would have.
func SpendPasswordCheck(password string) error {
	decoyMu.Lock()
	if decoyHash == "" {
		decoyHash, _ = HashPassword("decoy-" + time.Now().String())
	}
	h := decoyHash
	decoyMu.Unlock()
	if h == "" {
		return nil
	}
	if err := VerifyPassword(h, password); errors.Is(err, ErrBusy) {
		return err
	}
	return nil
}
