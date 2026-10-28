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

// asciiDigit maps any Unicode decimal digit (Arabic-Indic, fullwidth, ...) to its ASCII digit, so one number typed in
// two digit scripts is one destination; ok is false for anything that is not a decimal digit.
func asciiDigit(r rune) (byte, bool) {
	if r >= '0' && r <= '9' {
		return byte(r), true
	}
	if !unicode.IsDigit(r) {
		return 0, false
	}
	// Decimal digits come in contiguous runs whose length is a multiple of ten, each run starting at a zero.
	start := r
	for unicode.IsDigit(start - 1) {
		start--
	}
	return byte('0' + (r-start)%10), true
}

// Normalize accepts free-form input and returns E.164 in ASCII digits; a leading "00" trunk prefix is treated as "+".
// Length is counted in digits, never bytes.
func Normalize(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", ErrEmpty
	}
	// Digits first, so a "00" prefix typed in another script is recognised too.
	var norm strings.Builder
	for _, r := range s {
		if d, ok := asciiDigit(r); ok {
			norm.WriteByte(d)
		} else {
			norm.WriteRune(r)
		}
	}
	s = norm.String()
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !strings.HasPrefix(s, "+") {
		return "", ErrNoPlus
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteByte('+')
	digits := 0
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			digits++
		case r == ' ' || r == '\t' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", ErrBadChars
		}
	}
	switch {
	case digits < 8:
		return "", ErrTooShort
	case digits > 15:
		return "", ErrTooLong
	}
	return b.String(), nil
}
