package passwordhash

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The self-service password policy (contract section 2): 8-128 characters, not all one character, not the email
// address itself. A client chooses this password on a phone in a captive browser, so the policy refuses only
// what is plainly guessable and leaves the rest to the throttles and the argon2 cost; composition rules ("one
// digit, one symbol") are deliberately absent -- they push people to Password1! and buy nothing measurable.
const (
	SelfServiceMinLen = 8
	SelfServiceMaxLen = 128
)

// ErrWeakPassword is the single answer for every policy refusal: the portal says "choose a stronger password"
// with the rules beside the field, and the reason is not an oracle for anything.
var ErrWeakPassword = errors.New("passwordhash: password does not meet the policy")

// CheckSelfService validates a self-service password against its account's address. Length is counted in
// characters (runes), not bytes, so a password in Arabic is measured the way its owner sees it.
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
