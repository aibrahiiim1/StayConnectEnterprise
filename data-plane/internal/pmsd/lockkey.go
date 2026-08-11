package pmsd

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrMalformedUUID is returned when a tenant/site/interface identifier is not a canonical UUID.
var ErrMalformedUUID = errors.New("pmsd: malformed UUID (expected canonical 8-4-4-4-12 hex)")

// lockKeyNamespace is the fixed, versioned domain-separation label for the PMS Interface owner lock.
// Bumping the version (…:v2) deliberately changes every derived key so a new lock regime cannot collide
// with the old one.
const lockKeyNamespace = "stayconnect:pms-interface-owner:v1"

// LockKey derives the deterministic single-owner advisory-lock key for a PMS Interface.
//
// Derivation (documented + covered by fixed test vectors in lockkey_test.go):
//
//	digest = SHA-256( namespace_bytes || tenant16 || site16 || interface16 )
//	key    = int64( big-endian uint64 of digest[0:8] )   // first 8 bytes, big-endian, as a signed bigint
//
// where namespace_bytes = "stayconnect:pms-interface-owner:v1" and each *16 is the canonical 16-byte
// binary form of the UUID. Malformed UUIDs are REJECTED (never hashed as arbitrary strings) so a garbage
// identity can never silently share a lock with a real one. The result is a PostgreSQL signed bigint
// suitable for pg_advisory_lock.
func LockKey(tenantID, siteID, interfaceID string) (int64, error) {
	t, err := parseUUID16(tenantID)
	if err != nil {
		return 0, fmt.Errorf("tenant: %w", err)
	}
	s, err := parseUUID16(siteID)
	if err != nil {
		return 0, fmt.Errorf("site: %w", err)
	}
	i, err := parseUUID16(interfaceID)
	if err != nil {
		return 0, fmt.Errorf("interface: %w", err)
	}
	h := sha256.New()
	_, _ = h.Write([]byte(lockKeyNamespace))
	_, _ = h.Write(t[:])
	_, _ = h.Write(s[:])
	_, _ = h.Write(i[:])
	sum := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8])), nil
}

// parseUUID16 parses a canonical UUID (8-4-4-4-12 lowercase/uppercase hex with dashes) into 16 bytes.
// It is strict: exactly 36 characters, dashes only at positions 8/13/18/23, hex elsewhere.
func parseUUID16(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return out, ErrMalformedUUID
	}
	j := 0
	for k := 0; k < 36; k++ {
		if k == 8 || k == 13 || k == 18 || k == 23 {
			continue
		}
		hi, ok1 := hexNibble(s[k])
		k++
		lo, ok2 := hexNibble(s[k])
		if !ok1 || !ok2 {
			return out, ErrMalformedUUID
		}
		out[j] = hi<<4 | lo
		j++
	}
	return out, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
