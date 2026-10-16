package main

// ONE RULE FOR EVERY MANDATORY STAFF REASON.
//
// A reason is the justification an operator types before an action that weakens a control or changes what a
// client holds: releasing a device's wait, revealing a code, changing a site setting, vouching for an address.
// The bounds used to be counted four different ways -- BYTES 4-500 (validReason), RUNES 3-200 (the sign-in
// restriction release), RUNES 1-500 in some definer functions, and unbounded or optional elsewhere -- while the
// Admin Console counts CHARACTERS. A reason typed in Arabic is two bytes a character, so "four characters"
// passed in the browser and was refused here, and a 300-character reason the browser accepted was refused as
// "too long".
//
// So there is one helper, counting characters (runes) of the TRIMMED text, with one pair of bounds, and every
// handler that takes a reason uses it. The database's own checks count characters too (length(btrim(..))), so
// the three layers agree.
//
// The Admin Console mirrors these numbers (hotel-admin/lib/reason.ts); reason_bounds_test.go pins them.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	reasonMinChars = 4
	reasonMaxChars = 500
)

// reasonBoundsMessage is what a refused reason is told, in the same words everywhere.
var reasonBoundsMessage = fmt.Sprintf("a bounded reason (%d-%d characters) is required", reasonMinChars, reasonMaxChars)

// reasonText returns the trimmed reason and whether it is within bounds, counted in characters.
func reasonText(reason string) (string, bool) {
	t := strings.TrimSpace(reason)
	n := utf8.RuneCountInString(t)
	return t, n >= reasonMinChars && n <= reasonMaxChars
}

// validReason bounds the mandatory justification. Empty is refused because an unexplained action is
// indistinguishable afterwards from an unauthorized one; a cap keeps a free-text field from becoming a place to
// store something else.
func validReason(reason string) bool {
	_, ok := reasonText(reason)
	return ok
}
