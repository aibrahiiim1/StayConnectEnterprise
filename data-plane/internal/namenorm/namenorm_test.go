package namenorm

import "testing"

// THE ONE PROPERTY THAT MATTERS: whatever the PMS stores and whatever the guest types must land on the same
// value. Every case below is written as that round trip rather than as "f(x) == literal", because the literal
// form is what let the old code look correct in isolation -- each side was individually sensible and only the
// PAIR was wrong.
func TestStoredAndTypedAgree(t *testing.T) {
	cases := []struct {
		name        string
		fromPMS     string // what the feed sends, stored into *_norm
		guestTypes  string // what the guest enters at the portal
		shouldMatch bool
	}{
		// The incident: a mixed-case surname from the PMS could never equal an upper-cased input.
		{"mixed-case surname from PMS", "Anderson", "ANDERSON", true},
		{"mixed-case both sides", "Anderson", "anderson", true},
		{"lower stored, mixed typed", "anderson", "AnDeRsOn", true},
		{"already upper", "ANDERSON", "anderson", true},

		// Whitespace, on either side.
		{"trailing space in feed", "Anderson  ", "ANDERSON", true},
		{"leading space in feed", "   Anderson", "anderson", true},
		{"guest pads the field", "Anderson", "  Anderson  ", true},
		{"non-breaking space around feed value", " Anderson ", "ANDERSON", true},

		// Names that are not ASCII must still round-trip.
		{"accented surname", "Müller", "MÜLLER", true},
		{"accented, guest types lower", "Müller", "müller", true},
		{"cyrillic", "Иванов", "иванов", true},

		// Two-word and hyphenated surnames keep their internal structure: this normalizer does NOT collapse
		// or strip internal separators, and a change there would alter who can authenticate.
		{"space-separated surname", "Van Der Berg", "van der berg", true},
		{"hyphenated", "Smith-Jones", "SMITH-JONES", true},

		// Genuinely different people must still not match.
		{"different surname", "Anderson", "Andersen", false},
		{"prefix is not a match", "Anderson", "Anders", false},
		{"internal space is significant", "Van Der Berg", "VanDerBerg", false},
		{"empty typed value", "Anderson", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stored := Name(c.fromPMS)   // write path
			typed := Name(c.guestTypes) // read path
			if got := stored == typed; got != c.shouldMatch {
				t.Fatalf("stored %q vs typed %q: match=%v, want %v (stored normalized to %q, typed to %q)",
					c.fromPMS, c.guestTypes, got, c.shouldMatch, stored, typed)
			}
		})
	}
}

// Rooms are the same contract. They hid the defect because one property numbers its rooms with digits.
func TestRoomStoredAndTypedAgree(t *testing.T) {
	cases := []struct {
		stored, typed string
		match         bool
	}{
		{"412", "412", true},
		{"412", " 412 ", true},
		{"a12", "A12", true}, // the case the old trim-only write path could never match
		{"12b", "12B", true},
		{"ph1", "PH1", true},
		{"412", "413", false},
		{"412", "41", false},
	}
	for _, c := range cases {
		if got := Room(c.stored) == Room(c.typed); got != c.match {
			t.Fatalf("room stored %q vs typed %q: match=%v want %v", c.stored, c.typed, got, c.match)
		}
	}
}

// IDEMPOTENCE IS WHAT MAKES THE BACKFILL SAFE. The migration rewrites stored values in place; if normalizing
// an already-normalized value changed it, a second run would keep changing rows and the migration could never
// be proven a no-op.
func TestIdempotent(t *testing.T) {
	for _, in := range []string{"Anderson", "  müller ", "VAN DER BERG", "", "a12", "Иванов", " x "} {
		once := Name(in)
		if twice := Name(once); twice != once {
			t.Fatalf("Name not idempotent for %q: %q -> %q", in, once, twice)
		}
		onceR := Room(in)
		if twiceR := Room(onceR); twiceR != onceR {
			t.Fatalf("Room not idempotent for %q: %q -> %q", in, onceR, twiceR)
		}
	}
}

// The SQL backfill must reproduce exactly what Go does, or the two drift again the moment either is touched.
// upper(btrim(x)) is Postgres's equivalent; this pins the pairs the migration will be asserted against.
func TestMatchesSQLSemantics(t *testing.T) {
	// btrim() removes the ASCII whitespace class by default; strings.TrimSpace removes the wider Unicode set.
	// Values whose only difference is a non-ASCII space therefore normalize identically in Go but NOT in SQL,
	// which is why the migration trims with a character class rather than relying on bare btrim().
	if Name(" Anderson") == " ANDERSON" {
		t.Fatal("Name must strip a non-breaking space; SQL side must use an equivalent trim")
	}
	if Name(" Anderson ") != "ANDERSON" {
		t.Fatal("ASCII trim + upper is the baseline the migration must reproduce")
	}
}

// Reservation is deliberately trim-only. Pinned so a future "consistency" change is a deliberate decision.
func TestReservationIsTrimOnlyByDesign(t *testing.T) {
	if Reservation("  ab12  ") != "ab12" {
		t.Fatal("Reservation must trim")
	}
	if Reservation("ab12") == Reservation("AB12") {
		t.Fatal("Reservation must NOT case-fold: it is an opaque PMS identifier, not a name")
	}
}
