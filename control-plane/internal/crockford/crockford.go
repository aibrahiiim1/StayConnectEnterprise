// Package crockford generates human-friendly voucher codes.
//
// Alphabet: Crockford base32 — 0-9, A-Z minus I, L, O, U. 32 symbols, 5 bits
// each. A 12-char code carries 60 bits of entropy — collision probability
// after 10^6 codes is roughly 4e-7, so duplicates are astronomically rare and
// the DB UNIQUE index is an adequate safety net.
//
// Display format: XXXX-XXXX-XXXX. Storage format: 12 chars, no dashes.
// Normalize() handles input:
//   - strip whitespace and dashes
//   - uppercase
//   - map I→1, L→1, O→0 (Crockford's documented ambiguity rules)
package crockford

import (
	"crypto/rand"
	"fmt"
	"strings"
)

const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Generate returns a 12-character code without dashes.
func Generate() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, 12)
	for i, b := range buf {
		out[i] = Alphabet[int(b)%32]
	}
	return string(out), nil
}

// GenerateN returns n unique codes (deduped in-memory; the DB UNIQUE index
// is still the authority on uniqueness across tenants).
func GenerateN(n int) ([]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("n must be > 0")
	}
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	for attempts := 0; len(out) < n; attempts++ {
		if attempts > n*3 {
			return nil, fmt.Errorf("crockford: exhausted retries")
		}
		c, err := Generate()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

// Format inserts dashes at positions 4 and 8: "XXXXYYYYZZZZ" -> "XXXX-YYYY-ZZZZ".
// Input shorter or longer than 12 is returned as-is (no panic).
func Format(code string) string {
	if len(code) != 12 {
		return code
	}
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// Normalize prepares a user-entered code for lookup: strips dashes and
// whitespace, uppercases, maps the ambiguous letters per Crockford's rules.
// Returns the 12-char canonical form (or whatever length survives cleanup).
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue
		case r >= 'a' && r <= 'z':
			r = r - 32 // to upper
		}
		switch r {
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		case 'U':
			continue // Crockford forbids U — treat as typo and drop
		}
		b.WriteRune(r)
	}
	return b.String()
}
