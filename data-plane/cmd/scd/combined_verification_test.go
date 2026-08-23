package main

// COMBINED VERIFICATION: ONE BOX, THREE COMPARISONS, DECIDED SERVER-SIDE.
//
// Under room_any the guest types a single value and is never asked what kind of identifier it is. The value
// goes to the server as typed, and the server offers it to all three comparisons at once — first name, last
// name, reservation number — each normalised the way its own stored column was.
//
// What is asserted here is the FAN-OUT, because that is the part that is easy to get subtly wrong. The
// alternative anyone reaches for first is to look at the value and guess: digits mean a reservation number,
// letters mean a name. That is the legacy "either" behaviour, and it is why a surname containing a digit was
// submitted as a reservation number and failed for the guest who owned it. Nothing in this path may inspect
// the value's shape.

import (
	"strings"
	"testing"
)

// fanOut calls THE HANDLER'S OWN function, normalising the explicit fields first exactly as resolveHandler
// does. Re-implementing the logic here would produce a test that agrees with itself while the handler drifts.
func fanOut(verification, last, first, res string) (string, string, string) {
	return combineVerification(verification, normalizeName(last), normalizeName(first), strings.TrimSpace(res))
}

// The one value must reach all three comparisons. If it reached only one, room_any would silently behave as
// whichever single mode that was, and two thirds of guests would be refused with the uniform message.
func TestCombinedVerificationReachesAllThreeComparisons(t *testing.T) {
	for _, v := range []string{"Ibrahim", "ABC123", "14332", "O'Brien", "42"} {
		last, first, res := fanOut(v, "", "", "")
		if last == "" || first == "" || res == "" {
			t.Fatalf("verification %q did not reach all three comparisons: last=%q first=%q res=%q",
				v, last, first, res)
		}
		if want := normalizeName(v); last != want || first != want {
			t.Fatalf("name comparisons must use the name normaliser: got last=%q first=%q want %q", last, first, want)
		}
		if res != v {
			t.Fatalf("the reservation comparison must use the value as typed, got %q want %q", res, v)
		}
	}
}

// No shape inspection. A value that looks like a reservation number and a value that looks like a name are
// treated identically — both are offered to all three fields. This is the specific regression that "either"
// represents, so it is asserted directly rather than implied.
func TestCombinedVerificationDoesNotGuessFromShape(t *testing.T) {
	numericLast, numericFirst, numericRes := fanOut("ABC123", "", "", "")
	nameLast, nameFirst, nameRes := fanOut("Ibrahim", "", "", "")

	numericReached := (numericLast != "") && (numericFirst != "") && (numericRes != "")
	nameReached := (nameLast != "") && (nameFirst != "") && (nameRes != "")
	if numericReached != nameReached {
		t.Fatal("a reservation-shaped value and a name-shaped value were routed differently; combined mode " +
			"must not infer the kind of identifier from what the value looks like")
	}
}

// The explicit single-field modes are untouched. A site on room_lastname must keep comparing exactly one
// field: combined mode is an addition, not a change to what the existing modes do.
func TestExplicitModesAreNotWidenedByCombinedMode(t *testing.T) {
	last, first, res := fanOut("", "Ibrahim", "", "")
	if last == "" {
		t.Fatal("an explicit last-name submission must still populate the last-name comparison")
	}
	if first != "" || res != "" {
		t.Fatalf("an explicit single-field mode must not fan out: first=%q res=%q", first, res)
	}

	// And a combined value alongside an explicit field does not widen it either — the explicit field wins.
	last, first, res = fanOut("Somebody", "Ibrahim", "", "")
	if first != "" || res != "" || last != normalizeName("Ibrahim") {
		t.Fatalf("an explicit field must take precedence over a combined value: last=%q first=%q res=%q",
			last, first, res)
	}
}

// Empty stays empty: a blank combined value must not turn into three empty comparisons that somehow match,
// and the handler's incomplete-evidence check must still see nothing.
func TestBlankCombinedVerificationSubmitsNothing(t *testing.T) {
	last, first, res := fanOut("   ", "", "", "")
	if last != "" || first != "" || res != "" {
		t.Fatalf("a blank verification must produce no comparison values, got last=%q first=%q res=%q",
			last, first, res)
	}
}
