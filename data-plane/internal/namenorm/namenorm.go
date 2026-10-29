// Package namenorm is the one canonical normalizer for the values the PMS mirror stores in its `*_norm` columns and
// the guest sign-in compares against. Both the write side (stayengine) and the read side (phase3_auth) must use
// these functions; a second normalizer let them diverge and made guests with mixed-case PMS names unable to sign in.
//
// Unicode normal form (AD-33): every value is brought to NFC before and after the case fold, so a name the PMS stored
// decomposed ("e" + combining acute) matches the precomposed form a guest's keyboard produces, and the reverse. NFC
// only unifies canonically equivalent spellings of the SAME text; it does not strip accents or loosen anything.
// iam_v2.norm_identity_text (migration 0164) is the SQL twin and must stay identical.
//
// Deliberately not done: accent stripping, transliteration, fuzzy or prefix matching, collapsing internal
// whitespace. Those change who can authenticate and need a Product-Owner decision.
// Known limits of Go's Unicode upper-casing: "ß" upper-cases to itself (not "SS"), and Turkish dotless "ı" and
// dotted "İ" do not round-trip.
package namenorm

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

func canon(s string) string {
	return norm.NFC.String(strings.ToUpper(strings.TrimSpace(norm.NFC.String(s))))
}

// Name canonicalizes a guest first or last name for storage in a `*_norm` column and for comparison: Unicode-aware
// trim and upper-case. Call it on both sides.
func Name(s string) string {
	return canon(s)
}

// Room canonicalizes a room number the same way. Numeric rooms hid the missing case folding; alphanumeric rooms
// ("A12", "12b") need it.
func Room(s string) string {
	return canon(s)
}

// Reservation canonicalizes an external reservation identifier: trim only, deliberately. It is an opaque PMS
// identifier compared against stays.external_reservation_id (stored verbatim); upper-casing would change which
// reservations authenticate.
func Reservation(s string) string {
	return strings.TrimSpace(s)
}
