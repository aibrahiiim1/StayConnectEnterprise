// Package phone normalizes user-typed phone numbers into E.164 form.
//
// E.164 ("+" + country code + national number, total 8–15 digits) is the
// canonical wire form expected by SMS gateways (Twilio, MessageBird, etc.)
// and is what we store in guests.phone / auth_otps.destination.
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

// Normalize accepts free-form input like "+1 (555) 123-4567",
// "+44 20-7946 0958", or "00 1 555 123 4567" and returns the E.164 form
// "+15551234567". The leading "00" trunk prefix is treated as "+".
func Normalize(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", ErrEmpty
	}
	// Treat leading "00" as "+" (international trunk prefix used in EU/MENA).
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !strings.HasPrefix(s, "+") {
		return "", ErrNoPlus
	}
	// Strip everything except digits from the number portion.
	var b strings.Builder
	b.Grow(len(s))
	b.WriteByte('+')
	for _, r := range s[1:] {
		switch {
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '-' || r == '.' || r == '(' || r == ')':
			// strip
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
