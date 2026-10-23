// Package crockford generates 12-char Crockford base32 voucher codes (display XXXX-XXXX-XXXX, stored without
// dashes). Duplicates are rare enough that the DB UNIQUE index is an adequate safety net.
package crockford

import (
	"crypto/rand"
	"fmt"
	"strings"
)

const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

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

// GenerateN dedupes in memory only; the DB UNIQUE index is the authority across tenants.
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

// Format returns input that is not 12 chars unchanged.
func Format(code string) string {
	if len(code) != 12 {
		return code
	}
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// Normalize strips dashes/whitespace, uppercases and applies Crockford's I/L->1, O->0 rules; U is dropped.
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue
		case r >= 'a' && r <= 'z':
			r = r - 32
		}
		switch r {
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		case 'U':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
