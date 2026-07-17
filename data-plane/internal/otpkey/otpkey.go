// Package otpkey implements keyed-HMAC OTP verification (decision D7).
//
// A one-time code is never stored in plaintext. New OTPs are stored as an HMAC-SHA256 over a
// domain-separated, length-prefixed message binding the challenge context (tenant, channel,
// destination, challenge id) to the code, keyed by a dedicated appliance-local secret. Each stored
// digest is PINNED to the key generation that produced it, so the key can be rotated: a new
// generation becomes active for issuance while superseded generations are retained (verify-only)
// until every OTP pinned to them has expired, after which they may be removed (removal gate =
// max OTP TTL).
//
// Verification is constant-time. The key material never appears in the database, logs, or the stored
// digest. Legacy in-flight OTPs (stored as "salt:sha256(salt|code)") are verified through a
// time-bounded compatibility path; no new OTP is ever issued in the legacy format.
package otpkey

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const domainSep = "stayconnect/otp/hmac/v1"

// Ring is an immutable set of key generations with one active generation for issuance.
type Ring struct {
	keys   map[int][]byte // generation -> key bytes
	active int
}

// NewRing builds a key ring. Every key must be >= 16 bytes. active must exist in keys.
func NewRing(keys map[int][]byte, active int) (*Ring, error) {
	if len(keys) == 0 {
		return nil, errors.New("otpkey: no key generations")
	}
	cp := make(map[int][]byte, len(keys))
	for g, k := range keys {
		if g < 1 {
			return nil, fmt.Errorf("otpkey: invalid generation %d", g)
		}
		if len(k) < 16 {
			return nil, fmt.Errorf("otpkey: generation %d key too short (%d bytes)", g, len(k))
		}
		b := make([]byte, len(k))
		copy(b, k)
		cp[g] = b
	}
	if _, ok := cp[active]; !ok {
		return nil, fmt.Errorf("otpkey: active generation %d not present", active)
	}
	return &Ring{keys: cp, active: active}, nil
}

// Active returns the current issuance generation.
func (r *Ring) Active() int { return r.active }

// Generations returns the known generations, ascending.
func (r *Ring) Generations() []int {
	gs := make([]int, 0, len(r.keys))
	for g := range r.keys {
		gs = append(gs, g)
	}
	sort.Ints(gs)
	return gs
}

// Challenge is the immutable context an OTP is bound to.
type Challenge struct {
	TenantID    string
	Channel     string // "email" | "sms"
	Destination string // email/phone
	ChallengeID string // the auth_otps row id
}

// message builds the domain-separated, length-prefixed HMAC input. Length-prefixing every field
// makes the encoding unambiguous, so no two distinct field tuples can collide.
func message(c Challenge, code string) []byte {
	var b []byte
	put := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		b = append(b, n[:]...)
		b = append(b, s...)
	}
	put(domainSep)
	put(c.TenantID)
	put(strings.ToLower(strings.TrimSpace(c.Channel)))
	put(strings.ToLower(strings.TrimSpace(c.Destination)))
	put(c.ChallengeID)
	put(code)
	return b
}

func (r *Ring) mac(gen int, c Challenge, code string) ([]byte, error) {
	k, ok := r.keys[gen]
	if !ok {
		return nil, fmt.Errorf("otpkey: unknown generation %d", gen)
	}
	m := hmac.New(sha256.New, k)
	m.Write(message(c, code))
	return m.Sum(nil), nil
}

// Issue returns the (generation, hex-digest) to store for a new OTP, using the active generation.
func (r *Ring) Issue(c Challenge, code string) (generation int, digestHex string, err error) {
	d, err := r.mac(r.active, c, code)
	if err != nil {
		return 0, "", err
	}
	return r.active, hex.EncodeToString(d), nil
}

// Verify checks a candidate code against a stored (generation, hex-digest) in constant time.
func (r *Ring) Verify(gen int, storedHex string, c Challenge, code string) (bool, error) {
	stored, err := hex.DecodeString(storedHex)
	if err != nil || len(stored) != sha256.Size {
		return false, errors.New("otpkey: malformed stored digest")
	}
	want, err := r.mac(gen, c, code)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(stored, want) == 1, nil
}

// VerifyLegacy verifies a legacy stored value "salt:sha256hex(salt|code)" in constant time.
// Used only for already-issued in-flight OTPs during the compatibility window; never for issuance.
func VerifyLegacy(stored, code string) (bool, error) {
	i := strings.IndexByte(stored, ':')
	if i <= 0 || i == len(stored)-1 {
		return false, errors.New("otpkey: malformed legacy digest")
	}
	salt, hexHash := stored[:i], stored[i+1:]
	raw, err := hex.DecodeString(hexHash)
	if err != nil || len(raw) != sha256.Size {
		return false, errors.New("otpkey: malformed legacy digest")
	}
	sum := sha256.Sum256([]byte(salt + "|" + code))
	return subtle.ConstantTimeCompare(raw, sum[:]) == 1, nil
}

// IsLegacyFormat reports whether a stored value is the legacy "salt:sha256hex" form (has a colon and
// a 64-hex tail). Keyed-HMAC storage records the generation separately and stores only the hex digest.
func IsLegacyFormat(stored string) bool {
	i := strings.IndexByte(stored, ':')
	if i <= 0 {
		return false
	}
	tail := stored[i+1:]
	if len(tail) != 2*sha256.Size {
		return false
	}
	if _, err := hex.DecodeString(tail); err != nil {
		return false
	}
	return true
}
