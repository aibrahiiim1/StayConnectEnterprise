// Package backupcrypt encrypts the appliance's backups to the hotel's own keys (GDPR remediation plan C5,
// Product-Owner decision D-C5).
//
// The format is age (https://age-encryption.org), X25519 recipients. A backup is encrypted to the public key of
// every registered key holder, so any ONE holder can open it alone, and the appliance -- which holds only public
// keys -- can write backups but never read them back. A private key reaches the appliance only inside a single
// request (verify, restore, the restore test), is used in memory and is never written anywhere.
//
// The same code serves scd (on-demand dumps, verify, restore, the restore test), edged (generating a holder's
// key) and the nightly shell backup (through `scd backup-crypt`), so there is one implementation of each step.
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

// Suffix marks an encrypted backup file.
const Suffix = ".age"

var (
	ErrNoRecipients = errors.New("no backup key holder is registered")
	ErrNoIdentity   = errors.New("no backup key was found in what was supplied")
	ErrWrongKey     = errors.New("this key does not open this backup")
)

// ParseRecipients turns the holders' public keys into age recipients. Any malformed key fails the whole list:
// a backup encrypted to fewer holders than the hotel registered is not the backup the hotel was promised.
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

// Encrypt returns a writer that encrypts to every recipient into dst. Close it to finish the file.
func Encrypt(dst io.Writer, recipients []age.Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	return age.Encrypt(dst, recipients...)
}

// ParseIdentity reads a private key from what an operator pasted or uploaded: the key file this package
// produces (comments included), an age-keygen file, or the bare AGE-SECRET-KEY-1... line.
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
	return nil, ErrNoIdentity
}

// Decrypt opens an encrypted backup with one identity. A key that is not among the file's recipients is
// ErrWrongKey, so the screen can say so rather than report a corrupt file.
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

// GenerateKey makes a new holder key pair. The secret goes to the hotel once; only the public key is kept.
func GenerateKey() (secret, public string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// checkAlphabet has no 0/O, 1/I/L or 5/S, so the code survives being read off paper.
const checkAlphabet = "ABCDEFGHJKMNPQRTUVWXYZ2346789"

// NewCheckCode is the short code printed only in the key file and on the recovery sheet. Typing it back proves
// the holder saved one of them before encryption depends on the key.
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

// CheckCodeHash is what the appliance stores: the code is normalised (case, spaces and dashes ignored).
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

// KeyFile is the downloadable private key: an age identity file (age -d -i <file> opens a backup with it),
// with the holder, the public key and the check code as comments.
func KeyFile(holder, site, secret, public, checkCode string, created time.Time) string {
	return strings.Join([]string{
		"# StayConnect backup key -- KEEP THIS FILE SECRET AND SAFE.",
		"# Anyone with this file can read this hotel's backups. Without it (or another holder's key) they",
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
