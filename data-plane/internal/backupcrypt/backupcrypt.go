// Package backupcrypt encrypts backups with age to every registered key holder's public key. The appliance
// holds only public keys, so it can write backups but never read them; a private key is used in memory for one
// request and never persisted.
package backupcrypt

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"filippo.io/age"
)

const Suffix = ".age"

var (
	ErrNoRecipients = errors.New("no backup key holder is registered")
	ErrNoIdentity   = errors.New("no backup key was found in what was supplied")
	ErrWrongKey     = errors.New("this key does not open this backup")
)

// ParseRecipients fails the whole list on any malformed key: a backup must never be encrypted to fewer holders
// than are registered.
func ParseRecipients(keys []string) ([]age.Recipient, error) {
	if len(keys) == 0 {
		return nil, ErrNoRecipients
	}
	out := make([]age.Recipient, 0, len(keys))
	for _, k := range keys {
		r, err := age.ParseX25519Recipient(strings.TrimSpace(k))
		if err != nil {
			return nil, fmt.Errorf("a registered backup key is not a valid age public key: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Encrypt returns a writer to dst; Close it to finish the file.
func Encrypt(dst io.Writer, recipients []age.Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	return age.Encrypt(dst, recipients...)
}

// ParseIdentity accepts this package's key file, an age-keygen file, or a bare AGE-SECRET-KEY-1... line.
func ParseIdentity(text string) (*age.X25519Identity, error) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(strings.ToUpper(line), "AGE-SECRET-KEY-1") {
			id, err := age.ParseX25519Identity(strings.ToUpper(line))
			if err != nil {
				return nil, fmt.Errorf("the backup key is damaged: %w", err)
			}
			return id, nil
		}
	}
	// A line longer than the scanner's buffer stops the scan; that is a damaged file, not a file without a key.
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("the backup key file could not be read: %w", err)
	}
	return nil, ErrNoIdentity
}

// Decrypt returns ErrWrongKey for a key outside the recipients, so callers do not report a corrupt file.
func Decrypt(src io.Reader, id age.Identity) (io.Reader, error) {
	r, err := age.Decrypt(src, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrWrongKey
		}
		return nil, fmt.Errorf("the backup could not be opened: %w", err)
	}
	return r, nil
}

// GenerateKey: only the public key is kept; the secret goes to the hotel once.
func GenerateKey() (secret, public string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// checkAlphabet has no 0/O, 1/I/L or 5/S, so the code survives being read off paper.
const checkAlphabet = "ABCDEFGHJKMNPQRTUVWXYZ2346789"

// NewCheckCode is printed only in the key file and recovery sheet; typing it back proves the holder saved one.
func NewCheckCode() (string, error) {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(checkAlphabet))))
		if err != nil {
			return "", err
		}
		if i == 4 {
			b.WriteByte('-')
		}
		b.WriteByte(checkAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// CheckCodeHash is what the appliance stores; case, spaces and dashes are ignored. The check code is NOT a secret
// (about 39 bits, printed beside the key): it only proves the holder saved the file, so an unsalted digest is enough
// and nothing may treat it as a credential.
func CheckCodeHash(code string) string {
	norm := strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 32
		}
		return r
	}, code)
	sum := sha256.Sum256([]byte("stayconnect-backup-check-code:" + norm))
	return hex.EncodeToString(sum[:])
}

// KeyFile is an age identity file (usable with `age -d -i`) carrying holder, public key and check code as comments.
func KeyFile(holder, site, secret, public, checkCode string, created time.Time) string {
	return strings.Join([]string{
		"# OneGate backup key -- KEEP THIS FILE SECRET AND SAFE.",
		"# Anyone with this file can read this site's backups. Without it (or another holder's key) they",
		"# cannot be read by anyone, including the appliance and its supplier.",
		"# holder:     " + oneLine(holder),
		"# site:       " + oneLine(site),
		"# created:    " + created.UTC().Format(time.RFC3339),
		"# public key: " + public,
		"# check code: " + checkCode,
		secret,
		"",
	}, "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
