package iamv2

// A STAY LENGTH OF 1 TO 5 NIGHTS MUST BE PUBLISHABLE, AND MEAN THE SAME THING TO EVERY READER.
//
// The first operator to set one was refused with "invalid_eligibility_rule". STAY_LENGTH is the first rule
// type that carries a NUMBER, and parseIntField accepted float64, int and int64 but not json.Number -- which
// is exactly what edged's decoder produces, because it sets UseNumber so that bandwidth caps cannot arrive as
// 2047.9999999.
//
// The refusal was the visible half. The dangerous half was invisible: had the rule published, the Phase-3
// offers path in scd (plain json.Unmarshal -> float64) would have evaluated it correctly while the Phase-2
// engine (UseNumber -> json.Number) failed it closed. One stored rule, two meanings, depending on who asked.
// So these assert every numeric shape a decoder in this system produces, on the same rule.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// decodeLikeEdged reproduces edged's request decoding: UseNumber, so numbers are json.Number.
func decodeLikeEdged(t *testing.T, raw string) map[string]any {
	t.Helper()
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

// decodeLikeScd reproduces the Phase-3 offers path: plain Unmarshal, so numbers are float64.
func decodeLikeScd(t *testing.T, raw string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

const oneToFiveNights = `{"min_nights":1,"max_nights":5}`

func TestStayLengthPublishesFromEveryDecoder(t *testing.T) {
	shapes := map[string]map[string]any{
		"edged (json.Number)": decodeLikeEdged(t, oneToFiveNights),
		"scd (float64)":       decodeLikeScd(t, oneToFiveNights),
		"native int":          {"min_nights": 1, "max_nights": 5},
		"native int64":        {"min_nights": int64(1), "max_nights": int64(5)},
	}
	for name, v := range shapes {
		if err := ValidateEligibilityRule(EligibilityRule{Type: RuleStayLength, Value: v}); err != nil {
			t.Errorf("%s: a 1-to-5-night rule was refused: %v", name, err)
		}
	}
}

func TestStayLengthMeansTheSameThingFromEveryDecoder(t *testing.T) {
	// The property that matters most: the SAME stored rule must admit and refuse the same stays whichever
	// decoder produced the map.
	stay := func(nights int) EligibilitySubject {
		a := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		d := a.AddDate(0, 0, nights)
		return EligibilitySubject{
			Now: a, AuthMethod: Method("PMS"), Kind: SubjectPrincipal,
			Stay: &StayEvidence{Arrival: &a, Departure: &d, EvidenceVersion: 1, Status: "IN_HOUSE"},
		}
	}
	shapes := map[string]map[string]any{
		"edged (json.Number)": decodeLikeEdged(t, oneToFiveNights),
		"scd (float64)":       decodeLikeScd(t, oneToFiveNights),
	}
	for name, v := range shapes {
		rules := []EligibilityRule{{Type: RuleStayLength, Value: v}}
		for _, tc := range []struct {
			nights int
			want   bool
		}{{0, false}, {1, true}, {3, true}, {5, true}, {6, false}, {12, false}} {
			got, reason := EvaluatePackageEligible(rules, stay(tc.nights))
			if got != tc.want {
				t.Errorf("%s: %d nights -> eligible=%v (%s), want %v", name, tc.nights, got, reason, tc.want)
			}
		}
	}
}

func TestStayLengthOpenBoundsAndRefusals(t *testing.T) {
	ok := func(raw string) error {
		return ValidateEligibilityRule(EligibilityRule{Type: RuleStayLength, Value: decodeLikeEdged(t, raw)})
	}
	// Either bound alone is a real rule and must publish.
	for _, good := range []string{`{"min_nights":8}`, `{"max_nights":7}`, `{"min_nights":11}`, `{"min_nights":0}`} {
		if err := ok(good); err != nil {
			t.Errorf("%s was refused: %v", good, err)
		}
	}
	// ...and these stay refused, with the strictness unchanged.
	for _, bad := range []string{
		`{}`,                               // constrains nothing
		`{"min_nights":1.5}`,               // not a number of nights
		`{"min_nights":1e2}`,               // exponent form is not a whole night count
		`{"min_nights":-1}`,                // negative
		`{"min_nights":10,"max_nights":5}`, // inverted
		`{"min_nights":"one"}`,             // not a number
	} {
		if err := ok(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestPublishRefusalNamesTheRuleAndTheReason(t *testing.T) {
	// "invalid_eligibility_rule" alone told an operator with a dozen conditions nothing about which one.
	err := &Error{Code: ErrInvalidInput, Msg: "STAY_LENGTH min_nights exceeds max_nights"}
	got := reasonWithDetail("invalid_eligibility_rule", RuleStayLength, err)
	for _, want := range []string{"invalid_eligibility_rule", "STAY_LENGTH", "exceeds max_nights"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not contain %q", got, want)
		}
	}
	// The machine label stays FIRST and unchanged, because callers match on it.
	if !strings.HasPrefix(got, "invalid_eligibility_rule") {
		t.Errorf("reason %q no longer starts with the label", got)
	}
	// A non-domain error still produces a usable reason rather than a bare label.
	if got := reasonWithDetail("invalid_grant_tier", "", errPlain("boom")); got != "invalid_grant_tier" {
		t.Errorf("unexpected reason for a plain error: %q", got)
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }
