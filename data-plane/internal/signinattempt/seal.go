package signinattempt

// THE SENSITIVE HALF OF AN ATTEMPT, SEALED.
//
// An attempt record is two different kinds of thing in one row. Most of it — the room, the result, the mirror
// age, the latency — is operational diagnostics that any authorised operator may read. A small part of it is
// the guest's own credential material: what they typed, and the values the PMS would have accepted. The
// Product Owner's decision is that authorised Hotel Admin and Reception users see those in full, unmasked,
// because they are the same people who already hold the guest's room number, stay and access credentials.
//
// "Unmasked to an authorised operator" is not the same as "lying around in the database". The sensitive half
// is sealed with AES-256-GCM under an appliance-local key that lives in a 0600 file outside PostgreSQL, so a
// copy of the database — a backup, a replica, a stolen disk — yields nothing. This is the construction the
// appliance already uses for PMS connector secrets and voucher codes; it is reused deliberately rather than
// re-invented, and the AAD is built by ONE function that both sides call, because two hand-written AAD
// builders drift until the day they don't match.
//
// WHAT IS NOT SEALED, and why. The submitted ROOM NUMBER is stored in clear: it is the primary column an
// operator filters and scans by, it is visible on every other screen that shows a stay, and encrypting it
// would make the feature unusable while protecting something the rest of the product already discloses to the
// same audience. The VERIFIER KIND is stored in clear because it is a three-valued label, not a value.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
)

// CipherVersion is the on-disk format marker written with every sealed row, so a later format change can be
// rolled out without having to guess what an existing row is.
const CipherVersion = 1

var (
	// ErrKeyUnavailable — the appliance-local key is missing or the wrong length. Callers MUST fail towards
	// "record the attempt without its sensitive half", never towards "write the plaintext".
	ErrKeyUnavailable = errors.New("signinattempt: sealing key unavailable")
	// ErrOpen — sealed material did not authenticate. Deliberately one opaque error: a caller cannot act on
	// "wrong key" versus "wrong owner" versus "corrupt", and telling them apart in a response would describe
	// the cryptography to whoever asked.
	ErrOpen = errors.New("signinattempt: sealed credential failed authentication")
)

// Keyring resolves a data-encryption key by its id. Implementations must never log or export key material.
// Same shape as the proven pmsd.Keyring and iamv2.VoucherKeyring, kept local so this package depends on
// neither the connector nor the commerce engine.
type Keyring interface {
	Key(keyID string) ([]byte, bool)
}

// MapKeyring is the in-memory keyring scd populates from the appliance secret store.
type MapKeyring map[string][]byte

func (m MapKeyring) Key(id string) ([]byte, bool) { k, ok := m[id]; return k, ok }

// Sensitive is the guest credential material belonging to ONE attempt. It exists in memory and inside the
// sealed blob, and nowhere else — never in a log line, a journal, a metric, a CI artifact or an export.
type Sensitive struct {
	// SubmittedVerifier is exactly what the guest typed, before any normalization. An operator comparing it
	// with the accepted values needs the raw form: "they typed a trailing space" and "they typed the wrong
	// name" look identical once both sides are trimmed.
	SubmittedVerifier string `json:"submitted_verifier"`
	// NormalizedVerifier is the same value after the ONE canonical normalizer, which is what the comparison
	// actually used. Storing both is the point: the difference between them is frequently the answer.
	NormalizedVerifier string `json:"normalized_verifier"`
	// The accepted values, as the mirror holds them for the matched or candidate stay. Empty when no
	// room/stay candidate existed — in that case there is nothing to have accepted, and inventing a
	// plausible-looking expected value would be worse than showing none.
	AcceptedFirstName         string `json:"accepted_first_name,omitempty"`
	AcceptedFamilyName        string `json:"accepted_family_name,omitempty"`
	AcceptedReservationNumber string `json:"accepted_reservation_number,omitempty"`
	// AdditionalAcceptedGuests counts the OTHER guests on the same stay whose names would also have been
	// accepted (sharers). Without it an operator reads the panel as "only this one name works" and sends a
	// legitimate sharer away.
	AdditionalAcceptedGuests int `json:"additional_accepted_guests,omitempty"`
}

// Empty reports whether there is nothing worth sealing.
func (s Sensitive) Empty() bool {
	return s.SubmittedVerifier == "" && s.NormalizedVerifier == "" &&
		s.AcceptedFirstName == "" && s.AcceptedFamilyName == "" && s.AcceptedReservationNumber == ""
}

// Sealed is the sealed form, ready to be stored as four columns.
type Sealed struct {
	Ciphertext    []byte
	Nonce         []byte
	EncryptionKey string
	CipherVersion int
}

// attemptAAD binds a sealed blob to the EXACT attempt row that owns it, so a ciphertext lifted from one row
// and pasted into another fails authentication instead of quietly describing the wrong guest.
//
// Deterministic and length-prefixed: without the length prefix, ("ab","c") and ("a","bc") would produce the
// same AAD, and the binding would not distinguish the tenants it is supposed to separate.
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

// Seal encrypts the sensitive half for exactly one (tenant, site, attempt).
//
// attemptID must be the id the row is actually stored under. Sealing under one id and inserting under another
// produces a row that decrypts nowhere, and the failure would surface much later as an operator staring at a
// detail panel that cannot show what the guest typed.
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

// Open decrypts the sensitive half. It returns ErrOpen for every authentication failure, including one caused
// by the row having been moved between attempts or sites.
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
