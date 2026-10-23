// Package phone normalizes typed phone numbers into E.164 (+ country code and national number, 8-15 digits), the form
// SMS gateways expect and the stored form.
package phone

import (
	"errors"
	"strings"
	"unicode"
)

var (
	ErrEmpty    = errors.New("phone: empty")
	ErrNoPlus   = errors.New("phone: must start with country code (+...)")
	ErrTooShort = errors.New("phone: too short (need 8–15 digits)")
	ErrTooLong  = errors.New("phone: too long (need 8–15 digits)")
	ErrBadChars = errors.New("phone: contains non-digit characters after +")
)

// Normalize accepts free-form input and returns E.164; a leading "00" trunk prefix is treated as "+".
func Normalize(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", ErrEmpty
	}
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !strings.HasPrefix(s, "+") {
		return "", ErrNoPlus
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteByte('+')
	for _, r := range s[1:] {
		switch {
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", ErrBadChars
		}
	}
	out := b.String()
	digits := len(out) - 1
	switch {
	case digits < 8:
		return "", ErrTooShort
	case digits > 15:
		return "", ErrTooLong
	}
	return out, nil
}
