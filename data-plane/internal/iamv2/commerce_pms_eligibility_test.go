package iamv2

// The key cases are evidence present but silent (a VIP flag never set, no departure date, a blank room type):
// treating any of those as "no constraint" would give a suite rate to an unqualified guest.

import (
	"testing"
	"time"
)

func ptrBool(b bool) *bool           { return &b }
func ptrTime(t time.Time) *time.Time { return &t }

func stayBase() *StayEvidence {
	arr := time.Now().Add(-72 * time.Hour)
	dep := time.Now().Add(24 * time.Hour)
	return &StayEvidence{
		StayID: "stay-1", InterfaceID: "iface-1", Status: "IN_HOUSE",
		RoomType: "SUITE", RatePlan: "CORP", TravelAgent: "ACME",
		VIP: ptrBool(true), Arrival: ptrTime(arr), Departure: ptrTime(dep),
		EvidenceVersion: 3,
	}
}

func subjectWith(e *StayEvidence) EligibilitySubject {
	return EligibilitySubject{Now: time.Now(), AuthMethod: "PMS", Kind: SubjectKind("PRINCIPAL"), Stay: e}
}

func TestPMSRulesEvaluateAgainstStayEvidence(t *testing.T) {
	cases := []struct {
		name string
		rule EligibilityRule
		stay func(*StayEvidence)
		want bool
	}{
		{"in-house status matches", EligibilityRule{RuleStayStatus, map[string]any{"statuses": []any{"IN_HOUSE"}}}, nil, true},
		{"other status does not", EligibilityRule{RuleStayStatus, map[string]any{"statuses": []any{"POST_STAY_ACTIVE"}}}, nil, false},
		{"room type matches", EligibilityRule{RuleRoomType, map[string]any{"room_types": []any{"SUITE", "DELUXE"}}}, nil, true},
		{"room type does not", EligibilityRule{RuleRoomType, map[string]any{"room_types": []any{"STANDARD"}}}, nil, false},
		{"rate plan matches", EligibilityRule{RuleRatePlan, map[string]any{"rate_plans": []any{"CORP"}}}, nil, true},
		{"travel agent matches", EligibilityRule{RuleTravelAgent, map[string]any{"travel_agents": []any{"ACME"}}}, nil, true},
		{"interface matches", EligibilityRule{RulePMSInterface, map[string]any{"pms_interface_ids": []any{"iface-1"}}}, nil, true},
		{"interface does not", EligibilityRule{RulePMSInterface, map[string]any{"pms_interface_ids": []any{"iface-2"}}}, nil, false},
		{"vip true matches", EligibilityRule{RuleVIP, map[string]any{"is_vip": true}}, nil, true},
		{"vip false does not", EligibilityRule{RuleVIP, map[string]any{"is_vip": false}}, nil, false},
		{"stay length min satisfied", EligibilityRule{RuleStayLength, map[string]any{"min_nights": float64(2)}}, nil, true},
		{"stay length min not satisfied", EligibilityRule{RuleStayLength, map[string]any{"min_nights": float64(10)}}, nil, false},
		{"stay length max satisfied", EligibilityRule{RuleStayLength, map[string]any{"max_nights": float64(10)}}, nil, true},
		{"stay length max exceeded", EligibilityRule{RuleStayLength, map[string]any{"max_nights": float64(1)}}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := stayBase()
			if c.stay != nil {
				c.stay(e)
			}
			ok, reason := EvaluatePackageEligible([]EligibilityRule{c.rule}, subjectWith(e))
			if ok != c.want {
				t.Fatalf("eligible = %v (want %v), reason %q", ok, c.want, reason)
			}
		})
	}
}

// Each case has a Stay but lacks the fact tested and must fail closed, including {is_vip:false}: "not marked
// VIP" and "we do not know" differ.
func TestPMSRulesFailClosedOnUnstatedEvidence(t *testing.T) {
	cases := []struct {
		name string
		rule EligibilityRule
		mut  func(*StayEvidence)
	}{
		{"vip not stated, rule wants false", EligibilityRule{RuleVIP, map[string]any{"is_vip": false}},
			func(e *StayEvidence) { e.VIP = nil }},
		{"vip not stated, rule wants true", EligibilityRule{RuleVIP, map[string]any{"is_vip": true}},
			func(e *StayEvidence) { e.VIP = nil }},
		{"room type blank", EligibilityRule{RuleRoomType, map[string]any{"room_types": []any{"SUITE"}}},
			func(e *StayEvidence) { e.RoomType = "" }},
		{"rate plan blank", EligibilityRule{RuleRatePlan, map[string]any{"rate_plans": []any{"CORP"}}},
			func(e *StayEvidence) { e.RatePlan = "" }},
		{"travel agent blank", EligibilityRule{RuleTravelAgent, map[string]any{"travel_agents": []any{"ACME"}}},
			func(e *StayEvidence) { e.TravelAgent = "" }},
		{"status blank", EligibilityRule{RuleStayStatus, map[string]any{"statuses": []any{"IN_HOUSE"}}},
			func(e *StayEvidence) { e.Status = "" }},
		{"no departure date", EligibilityRule{RuleStayLength, map[string]any{"min_nights": float64(1)}},
			func(e *StayEvidence) { e.Departure = nil }},
		{"no arrival date", EligibilityRule{RuleStayLength, map[string]any{"max_nights": float64(30)}},
			func(e *StayEvidence) { e.Arrival = nil }},
		{"departure before arrival", EligibilityRule{RuleStayLength, map[string]any{"min_nights": float64(0)}},
			func(e *StayEvidence) { d := e.Arrival.Add(-48 * time.Hour); e.Departure = &d }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := stayBase()
			c.mut(e)
			if ok, _ := EvaluatePackageEligible([]EligibilityRule{c.rule}, subjectWith(e)); ok {
				t.Fatal("an unanswerable PMS rule was satisfied")
			}
		})
	}
}

// A subject with no Stay evidence (voucher, account, OTP guest) never matches a PMS rule: it is unanswerable, not "true for everyone".
func TestPMSRulesNeverMatchWithoutStayEvidence(t *testing.T) {
	nonPMS := EligibilitySubject{Now: time.Now(), AuthMethod: MethodVoucher, Kind: SubjectKind("VOUCHER")}
	for _, r := range []EligibilityRule{
		{RuleStayStatus, map[string]any{"statuses": []any{"IN_HOUSE"}}},
		{RuleRoomType, map[string]any{"room_types": []any{"SUITE"}}},
		{RuleVIP, map[string]any{"is_vip": false}},
		{RuleStayLength, map[string]any{"min_nights": float64(0)}},
		{RulePMSInterface, map[string]any{"pms_interface_ids": []any{"iface-1"}}},
	} {
		if ok, reason := EvaluatePackageEligible([]EligibilityRule{r}, nonPMS); ok {
			t.Fatalf("%s matched a subject with no Stay evidence (reason %q)", r.Type, reason)
		}
	}
}

// Evidence with no authoritative version cannot be reproduced later, so no decision is made on it.
func TestPMSRulesRequireVersionedEvidence(t *testing.T) {
	e := stayBase()
	e.EvidenceVersion = 0
	r := EligibilityRule{RuleStayStatus, map[string]any{"statuses": []any{"IN_HOUSE"}}}
	if ok, reason := EvaluatePackageEligible([]EligibilityRule{r}, subjectWith(e)); ok {
		t.Fatalf("unversioned evidence satisfied a rule (reason %q)", reason)
	}
}

// Malformed rules fail closed; a typo must not mean "no constraint".
func TestMalformedPMSRulesFailClosed(t *testing.T) {
	for _, r := range []EligibilityRule{
		{RuleStayStatus, map[string]any{"statuses": []any{}}},
		{RuleRoomType, map[string]any{}},
		{RuleVIP, map[string]any{"is_vip": "yes"}},
		{RuleStayLength, map[string]any{}},
		{RuleStayLength, map[string]any{"min_nights": float64(5), "max_nights": float64(2)}},
		{RuleStayLength, map[string]any{"min_nights": "two"}},
	} {
		if ok, _ := EvaluatePackageEligible([]EligibilityRule{r}, subjectWith(stayBase())); ok {
			t.Fatalf("malformed rule %s/%v was satisfied", r.Type, r.Value)
		}
	}
}

// Publication validates the same shapes, so the operator sees the error at the form.
func TestPMSRulePublicationValidation(t *testing.T) {
	good := []EligibilityRule{
		{RuleStayStatus, map[string]any{"statuses": []any{"IN_HOUSE"}}},
		{RuleStayLength, map[string]any{"min_nights": float64(1), "max_nights": float64(7)}},
		{RuleVIP, map[string]any{"is_vip": true}},
		{RuleRoomType, map[string]any{"room_types": []any{"SUITE"}}},
	}
	for _, r := range good {
		if err := ValidateEligibilityRule(r); err != nil {
			t.Fatalf("valid rule %s rejected: %v", r.Type, err)
		}
	}
	bad := []EligibilityRule{
		{RuleStayStatus, map[string]any{"statuses": []any{}}},
		{RuleStayStatus, map[string]any{"status": []any{"IN_HOUSE"}}}, // unknown field
		{RuleVIP, map[string]any{}},
		{RuleStayLength, map[string]any{"min_nights": float64(9), "max_nights": float64(2)}},
		{RuleStayLength, map[string]any{"min_nights": float64(-1)}},
	}
	for _, r := range bad {
		if err := ValidateEligibilityRule(r); err == nil {
			t.Fatalf("invalid rule %s/%v was accepted for publication", r.Type, r.Value)
		}
	}
}
