package passwordhash

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Policy: 8-128 characters, not all one character, not the email address. No composition rules; they push people to
// Password1! and buy nothing.
const (
	SelfServiceMinLen = 8
	SelfServiceMaxLen = 128
)

// ErrWeakPassword is the single answer for every refusal, so the reason is not an oracle.
var ErrWeakPassword = errors.New("passwordhash: password does not meet the policy")

// CheckSelfService counts length in runes, not bytes.
func CheckSelfService(pw, email string) error {
	n := utf8.RuneCountInString(pw)
	if !utf8.ValidString(pw) || n < SelfServiceMinLen || n > SelfServiceMaxLen {
		return ErrWeakPassword
	}
	var first rune
	same := true
	for i, r := range pw {
		if unicode.IsControl(r) {
			return ErrWeakPassword
		}
		if i == 0 {
			first = r
		} else if r != first {
			same = false
		}
	}
	if same {
		return ErrWeakPassword
	}
	if e := strings.ToLower(strings.TrimSpace(email)); e != "" && strings.ToLower(strings.TrimSpace(pw)) == e {
		return ErrWeakPassword
	}
	return nil
}
