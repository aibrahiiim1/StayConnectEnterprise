package iamv2

// edged decodes with UseNumber (json.Number) and scd with plain Unmarshal (float64); a stored STAY_LENGTH rule
// must mean the same thing to both, so these assert every numeric shape on the same rule.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
	for _, good := range []string{`{"min_nights":8}`, `{"max_nights":7}`, `{"min_nights":11}`, `{"min_nights":0}`} {
		if err := ok(good); err != nil {
			t.Errorf("%s was refused: %v", good, err)
		}
	}
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
	err := &Error{Code: ErrInvalidInput, Msg: "STAY_LENGTH min_nights exceeds max_nights"}
	got := reasonWithDetail("invalid_eligibility_rule", RuleStayLength, err)
	for _, want := range []string{"invalid_eligibility_rule", "STAY_LENGTH", "exceeds max_nights"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not contain %q", got, want)
		}
	}
	// Callers match on the leading label.
	if !strings.HasPrefix(got, "invalid_eligibility_rule") {
		t.Errorf("reason %q no longer starts with the label", got)
	}
	if got := reasonWithDetail("invalid_grant_tier", "", errPlain("boom")); got != "invalid_grant_tier" {
		t.Errorf("unexpected reason for a plain error: %q", got)
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }
