package pmsd

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

var ErrMalformedUUID = errors.New("pmsd: malformed UUID (expected canonical 8-4-4-4-12 hex)")

// Bumping the version changes every derived key, so a new lock regime cannot collide with the old one.
const lockKeyNamespace = "stayconnect:pms-interface-owner:v1"

// LockKey derives the pg_advisory_lock key: int64(big-endian first 8 bytes of
// SHA-256(namespace || tenant16 || site16 || interface16)); fixed vectors in lockkey_test.go.
// Malformed UUIDs are rejected, never hashed as strings, so garbage can't share a lock with a real identity.
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
