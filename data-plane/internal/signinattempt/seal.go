package signinattempt

// The guest's credential material (what they typed and the values the PMS would accept) is sealed with
// AES-256-GCM under an appliance-local 0600 key file outside PostgreSQL, so a database copy yields nothing;
// authorised operators see it unmasked. One function builds the AAD for both sides so they cannot drift.
// The submitted room number and verifier kind stay in clear: the room is the column operators filter by and
// is already shown on every stay screen, and the kind is a three-valued label.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
)

// CipherVersion marks the on-disk format of every sealed row.
const CipherVersion = 1

var (
	// ErrKeyUnavailable: callers must fail towards recording the attempt without its sensitive half, never
	// towards writing plaintext.
	ErrKeyUnavailable = errors.New("signinattempt: sealing key unavailable")
	// ErrOpen is deliberately one opaque error for wrong key, wrong owner and corruption.
	ErrOpen = errors.New("signinattempt: sealed credential failed authentication")
)

// Keyring resolves a key by id; implementations must never log or export key material.
type Keyring interface {
	Key(keyID string) ([]byte, bool)
}

type MapKeyring map[string][]byte

func (m MapKeyring) Key(id string) ([]byte, bool) { k, ok := m[id]; return k, ok }

// Sensitive is the guest credential material of one attempt. It exists only in memory and in the sealed
// blob, never in a log, journal, metric, CI artifact or export.
type Sensitive struct {
	// SubmittedVerifier is the raw typed value; a trailing space and a wrong name look identical once trimmed.
	SubmittedVerifier string `json:"submitted_verifier"`
	// NormalizedVerifier is what the comparison used; the difference from the raw value is often the answer.
	NormalizedVerifier string `json:"normalized_verifier"`
	// Accepted values from the mirror for the matched or candidate stay; empty when there was no candidate,
	// rather than inventing an expected value.
	AcceptedFirstName         string `json:"accepted_first_name,omitempty"`
	AcceptedFamilyName        string `json:"accepted_family_name,omitempty"`
	AcceptedReservationNumber string `json:"accepted_reservation_number,omitempty"`
	// AdditionalAcceptedGuests counts sharers whose names would also be accepted, so an operator does not
	// send a legitimate sharer away.
	AdditionalAcceptedGuests int `json:"additional_accepted_guests,omitempty"`
}

func (s Sensitive) Empty() bool {
	return s.SubmittedVerifier == "" && s.NormalizedVerifier == "" &&
		s.AcceptedFirstName == "" && s.AcceptedFamilyName == "" && s.AcceptedReservationNumber == ""
}

type Sealed struct {
	Ciphertext    []byte
	Nonce         []byte
	EncryptionKey string
	CipherVersion int
}

// attemptAAD binds a blob to its exact attempt row so a ciphertext moved to another row fails
// authentication. It is length-prefixed so ("ab","c") and ("a","bc") do not collide.
func attemptAAD(tenantID, siteID, attemptID string) []byte {
	var b []byte
	add := func(s string) {
		b = append(b, byte(len(s)>>8), byte(len(s)))
		b = append(b, s...)
		b = append(b, 0x1f)
	}
	add("signin-attempt-aead:v1")
	add(tenantID)
	add(siteID)
	add(attemptID)
	return b
}

// Seal binds to (tenant, site, attempt); attemptID must be the id the row is stored under, or it decrypts nowhere.
func Seal(kr Keyring, keyID, tenantID, siteID, attemptID string, s Sensitive) (Sealed, error) {
	if kr == nil {
		return Sealed{}, ErrKeyUnavailable
	}
	key, ok := kr.Key(keyID)
	if !ok || len(key) != 32 {
		return Sealed{}, ErrKeyUnavailable
	}
	plain, err := json.Marshal(s)
	if err != nil {
		return Sealed{}, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, err
	}
	return Sealed{
		Ciphertext:    gcm.Seal(nil, nonce, plain, attemptAAD(tenantID, siteID, attemptID)),
		Nonce:         nonce,
		EncryptionKey: keyID,
		CipherVersion: CipherVersion,
	}, nil
}

// Open returns ErrOpen for every authentication failure, including a row moved between attempts or sites.
func Open(kr Keyring, sealed Sealed, tenantID, siteID, attemptID string) (Sensitive, error) {
	var out Sensitive
	if kr == nil {
		return out, ErrKeyUnavailable
	}
	key, ok := kr.Key(sealed.EncryptionKey)
	if !ok || len(key) != 32 {
		return out, ErrKeyUnavailable
	}
	if sealed.CipherVersion != CipherVersion {
		return out, fmt.Errorf("%w: unknown cipher version %d", ErrOpen, sealed.CipherVersion)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return out, ErrKeyUnavailable
	}
	if len(sealed.Nonce) != gcm.NonceSize() {
		return out, ErrOpen
	}
	plain, err := gcm.Open(nil, sealed.Nonce, sealed.Ciphertext, attemptAAD(tenantID, siteID, attemptID))
	if err != nil {
		return out, ErrOpen
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return out, ErrOpen
	}
	return out, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}
