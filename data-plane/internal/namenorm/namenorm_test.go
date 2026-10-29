package namenorm

import "testing"

// Whatever the PMS stores and whatever the guest types must land on the same value; cases are written as that round
// trip rather than f(x) == literal, since each side alone looked correct.
func TestStoredAndTypedAgree(t *testing.T) {
	cases := []struct {
		name        string
		fromPMS     string // what the feed sends, stored into *_norm
		guestTypes  string // what the guest enters at the portal
		shouldMatch bool
	}{
		{"mixed-case surname from PMS", "Anderson", "ANDERSON", true},
		{"mixed-case both sides", "Anderson", "anderson", true},
		{"lower stored, mixed typed", "anderson", "AnDeRsOn", true},
		{"already upper", "ANDERSON", "anderson", true},

		{"trailing space in feed", "Anderson  ", "ANDERSON", true},
		{"leading space in feed", "   Anderson", "anderson", true},
		{"guest pads the field", "Anderson", "  Anderson  ", true},
		{"non-breaking space around feed value", " Anderson ", "ANDERSON", true},

		{"accented surname", "Müller", "MÜLLER", true},
		{"accented, guest types lower", "Müller", "müller", true},
		{"cyrillic", "Иванов", "иванов", true},

		// Internal separators are not collapsed or stripped; changing that would alter who can authenticate.
		{"space-separated surname", "Van Der Berg", "van der berg", true},
		{"hyphenated", "Smith-Jones", "SMITH-JONES", true},

		{"different surname", "Anderson", "Andersen", false},
		{"prefix is not a match", "Anderson", "Anders", false},
		{"internal space is significant", "Van Der Berg", "VanDerBerg", false},
		{"empty typed value", "Anderson", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stored := Name(c.fromPMS)
			typed := Name(c.guestTypes)
			if got := stored == typed; got != c.shouldMatch {
				t.Fatalf("stored %q vs typed %q: match=%v, want %v (stored normalized to %q, typed to %q)",
					c.fromPMS, c.guestTypes, got, c.shouldMatch, stored, typed)
			}
		})
	}
}

func TestRoomStoredAndTypedAgree(t *testing.T) {
	cases := []struct {
		stored, typed string
		match         bool
	}{
		{"412", "412", true},
		{"412", " 412 ", true},
		{"a12", "A12", true},
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

// Idempotence makes the migration backfill safe: re-normalizing a normalized value must not change it.
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

// The SQL backfill must match Go: upper(btrim(x)) is the Postgres equivalent; this pins the pairs.
func TestMatchesSQLSemantics(t *testing.T) {
	// btrim() removes only ASCII whitespace by default while strings.TrimSpace removes Unicode spaces, so the
	// migration trims with a character class.
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

// AD-33: a name stored decomposed and the same name typed precomposed (and the reverse) are the same value.
func TestCanonicallyEquivalentSpellingsAgree(t *testing.T) {
	const decomposed = "Mu\u0308ller" // u + combining diaeresis
	const precomposed = "M\u00fcller"
	for _, c := range []struct{ stored, typed string }{
		{decomposed, precomposed}, {precomposed, decomposed}, {decomposed, "m\u00dcLLER"}, {" " + decomposed + " ", "MULLER" + ""},
	} {
		got, want := Name(c.stored), Name(c.typed)
		if c.typed == "MULLER" {
			// NFC is not accent stripping: "Muller" is a different name from "M\u00fcller".
			if got == want {
				t.Fatalf("%q and %q must stay different values", c.stored, c.typed)
			}
			continue
		}
		if got != want {
			t.Fatalf("Name(%q)=%q, Name(%q)=%q: canonically equivalent spellings must agree", c.stored, got, c.typed, want)
		}
	}
	if Room("12\u0301b") != Room("12\u0301B") {
		t.Fatal("Room is not case-folded after NFC")
	}
}
