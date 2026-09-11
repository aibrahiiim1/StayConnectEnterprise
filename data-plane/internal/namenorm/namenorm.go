// Package namenorm is the ONE canonical normalizer for the values the PMS mirror stores in its `*_norm`
// columns and the guest-authentication path compares against.
//
// WHY THIS PACKAGE EXISTS.
//
// The guest room sign-in compares what the guest typed with what the PMS mirror stored. That comparison is
// only sound if BOTH SIDES apply the identical transformation. They did not. The read side
// (cmd/scd/phase3_auth.go) upper-cased and trimmed; the write side (internal/stayengine/pg.go) wrote the PMS
// feed value STRAIGHT INTO `first_name_norm` / `last_name_norm` with no transformation at all. The read
// side's own comment asserted the contract -- "The PMS mirror stores normalized values" -- that the write
// side silently did not honour.
//
// The consequence was not theoretical and not rare. A PMS that sends "Anderson" rather than "ANDERSON" put a
// mixed-case value in a column the query could only ever match upper-case, so that guest could never sign in
// with room + surname. Measured on the PRE-LIVE mirror at the time of the incident: 107 of 784 in-house
// guests -- 13.6%, about one in seven -- were unmatchable this way. Every one of them received the same
// "check your details or contact reception" message a genuine typo produces, so the fault was invisible from
// the outside and produced no error anywhere.
//
// THE RULE: a value is normalized HERE, by these functions, and nowhere else. A second normalizer is how the
// first divergence happened; three already existed in this repository, two of them dead. If a caller needs a
// different transformation, that is a product decision, not a local helper.
//
// DELIBERATELY NOT DONE. No accent stripping, no transliteration, no fuzzy or prefix matching, no collapsing
// of internal whitespace. Those change WHO CAN AUTHENTICATE and need a Product-Owner decision of their own.
// This package only makes the two sides agree on the transformation that was already intended.
//
// KNOWN LIMITS OF Go's Unicode upper-casing, stated rather than discovered later:
//   - "ß" upper-cases to itself, not to "SS", so a stored "Straß" and a typed "STRASS" still differ.
//   - Turkish dotless "ı" and dotted "İ" do not round-trip under locale-independent casing.
//
// Both are pre-existing properties of the comparison this package preserves, not regressions introduced by
// it, and both are far rarer than the defect being fixed. Changing them means choosing a locale or a folding
// library, which is again a product decision.
package namenorm

import "strings"

// Name canonicalizes a guest first or last name for STORAGE in a `*_norm` column and for COMPARISON against
// one. strings.TrimSpace is Unicode-aware (it trims by unicode.IsSpace, so non-breaking and ideographic
// spaces go too), and strings.ToUpper maps per rune using Unicode's simple upper-case mapping.
//
// Call this on both sides. Storing a raw feed value in a `_norm` column is the defect this package exists to
// prevent.
func Name(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Room canonicalizes a room number for the same two purposes.
//
// Purely numeric rooms are unaffected by case, which is why this divergence stayed hidden: on the appliance
// where the incident occurred every unmatchable row was a NAME, and rooms happened to be digits. That is a
// property of one property's data, not a guarantee -- alphanumeric rooms ("A12", "12b", "PH1") are ordinary
// in hotels, and under the old asymmetry a lower-case stored room could never match an upper-cased query.
func Room(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Reservation canonicalizes an external reservation identifier.
//
// TRIM ONLY, AND THAT IS DELIBERATE. A reservation number is an opaque identifier issued by the PMS and is
// compared against `stays.external_reservation_id`, which is NOT a `_norm` column and is stored verbatim as
// the PMS emitted it. Upper-casing here would change which reservations authenticate -- it could make a
// case-sensitive identifier match when the PMS considers it distinct -- which is a semantic change to an
// external contract, not a normalization fix. It is included in this package so that every input on the
// authentication path is normalized through one place, with its difference documented rather than implicit.
func Reservation(s string) string {
	return strings.TrimSpace(s)
}
