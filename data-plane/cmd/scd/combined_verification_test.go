package main

// Under room_any the single typed value is offered to all three comparisons (first name, last name, reservation number);
// nothing may inspect its shape, or a surname containing a digit is submitted as a reservation number and fails.

import (
	"strings"
	"testing"
)

// fanOut calls the handler's own function; re-implementing it would let the handler drift.
func fanOut(verification, last, first, res string) (string, string, string) {
	return combineVerification(verification, normalizeName(last), normalizeName(first), strings.TrimSpace(res))
}

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

func TestExplicitModesAreNotWidenedByCombinedMode(t *testing.T) {
	last, first, res := fanOut("", "Ibrahim", "", "")
	if last == "" {
		t.Fatal("an explicit last-name submission must still populate the last-name comparison")
	}
	if first != "" || res != "" {
		t.Fatalf("an explicit single-field mode must not fan out: first=%q res=%q", first, res)
	}

	last, first, res = fanOut("Somebody", "Ibrahim", "", "")
	if first != "" || res != "" || last != normalizeName("Ibrahim") {
		t.Fatalf("an explicit field must take precedence over a combined value: last=%q first=%q res=%q",
			last, first, res)
	}
}

func TestBlankCombinedVerificationSubmitsNothing(t *testing.T) {
	last, first, res := fanOut("   ", "", "", "")
	if last != "" || first != "" || res != "" {
		t.Fatalf("a blank verification must produce no comparison values, got last=%q first=%q res=%q",
			last, first, res)
	}
}
