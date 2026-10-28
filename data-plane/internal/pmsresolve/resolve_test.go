package pmsresolve

import "testing"

func c(id string, o CandidateOutcome) Candidate { return Candidate{InterfaceID: id, Outcome: o} }

// Room numbers are not globally unique; each candidate is independent.
func TestD1_NamespaceIndependence(t *testing.T) {
	o := Resolve([]Candidate{c("if-A", Verified), c("if-B", NoMatch)}, 8)
	if o.Resolution != ResVerified || o.InterfaceID != "if-A" {
		t.Fatalf("D1: got %+v, want VERIFIED if-A", o)
	}
}

func TestD2_AmbiguityEscalates(t *testing.T) {
	if o := Resolve([]Candidate{c("A", Verified), c("B", Verified)}, 8); o.Resolution != ResAmbiguous {
		t.Fatalf("D2 (2 verified): got %+v, want AMBIGUOUS", o)
	}
	if o := Resolve([]Candidate{c("A", AmbiguousLocal), c("B", NoMatch)}, 8); o.Resolution != ResAmbiguous {
		t.Fatalf("D2 (ambiguous_local): got %+v, want AMBIGUOUS", o)
	}
}

func TestD3_SlowVerifiedBeatsFastNoMatch(t *testing.T) {
	o := Resolve([]Candidate{c("fast", NoMatch), c("slow", Verified)}, 8)
	if o.Resolution != ResVerified || o.InterfaceID != "slow" {
		t.Fatalf("D3: got %+v, want VERIFIED slow", o)
	}
}

func TestD4_IndeterminateFailsClosed(t *testing.T) {
	for _, bad := range []CandidateOutcome{Unavailable, Stale, UnsupportedEvidence} {
		o := Resolve([]Candidate{c("A", Verified), c("B", bad)}, 8)
		if o.Resolution != ResIndeterminate {
			t.Fatalf("D4 (%s): got %+v, want INDETERMINATE", bad, o)
		}
	}
}

func TestD5_UnmappedFailsClosed(t *testing.T) {
	if o := Resolve(nil, 8); o.Resolution != ResIndeterminate || o.Reason != "UNMAPPED" {
		t.Fatalf("D5: got %+v, want INDETERMINATE/UNMAPPED", o)
	}
}

func TestD6_CandidateCap(t *testing.T) {
	cands := []Candidate{c("A", NoMatch), c("B", NoMatch), c("C", Verified)}
	if o := Resolve(cands, 2); o.Resolution != ResIndeterminate || o.Reason != "CANDIDATE_CAP_EXCEEDED" {
		t.Fatalf("D6: got %+v, want INDETERMINATE/CANDIDATE_CAP_EXCEEDED", o)
	}
	if o := Resolve(cands, 8); o.Resolution != ResVerified {
		t.Fatalf("D6 within cap: got %+v, want VERIFIED", o)
	}
}

// Forged client hints cannot matter: Resolve has no hint input, only the trusted candidate vector.
func TestD7_ForgedHintsIgnored(t *testing.T) {
	vec := []Candidate{c("A", Verified), c("B", NoMatch)}
	want := Resolve(vec, 8)
	if want.Resolution != ResVerified || want.InterfaceID != "A" {
		t.Fatalf("D7: got %+v, want VERIFIED A", want)
	}
	// An equal vector built afresh gives the same outcome: nothing but the vector is an input.
	if got := Resolve([]Candidate{c("A", Verified), c("B", NoMatch)}, 8); got != want {
		t.Fatalf("D7: outcome must depend ONLY on the candidate vector: %+v vs %+v", got, want)
	}
}

func TestD8_PublishRule(t *testing.T) {
	o := Resolve([]Candidate{c("A", NoMatch), c("B", Verified), c("C", NoMatch)}, 8)
	if !o.GuestVisibleSuccess() || o.InterfaceID != "B" {
		t.Fatalf("D8: got %+v, want guest-visible VERIFIED B", o)
	}
	if Resolve([]Candidate{c("A", Unavailable)}, 8).GuestVisibleSuccess() {
		t.Fatal("D8: a non-success must never be guest-visible")
	}
}

func TestD10_AllNoMatch(t *testing.T) {
	if o := Resolve([]Candidate{c("A", NoMatch), c("B", NoMatch)}, 8); o.Resolution != ResNoMatch {
		t.Fatalf("D10: got %+v, want NO_MATCH", o)
	}
}

func TestD11_UniformNonSuccess(t *testing.T) {
	nonSuccess := []Outcome{
		Resolve(nil, 8),
		Resolve([]Candidate{c("A", NoMatch)}, 8),
		Resolve([]Candidate{c("A", Verified), c("B", Verified)}, 8),
		Resolve([]Candidate{c("A", Stale)}, 8),
	}
	for _, o := range nonSuccess {
		if o.GuestVisibleSuccess() {
			t.Fatalf("non-success %+v must not be guest-visible", o)
		}
		if o.Reason == "" {
			t.Fatalf("non-success %+v must carry an internal reason code", o)
		}
	}
}

func TestAnOutOfServiceCandidateIsIndeterminateAndNamedAsAConfigurationFact(t *testing.T) {
	out := Resolve([]Candidate{
		{InterfaceID: "a", Outcome: Verified},
		{InterfaceID: "b", Outcome: Unavailable, NotActive: true},
	}, MaxSharedCandidates)
	if out.Resolution != ResIndeterminate || out.Reason != "CANDIDATE_NOT_ACTIVE" || out.GuestVisibleSuccess() {
		t.Fatalf("a verified guest was admitted while a selected PMS was out of service: %+v", out)
	}
	// a feed that cannot answer is a different fact from a PMS that is out of service
	out = Resolve([]Candidate{
		{InterfaceID: "a", Outcome: Verified},
		{InterfaceID: "b", Outcome: Stale},
		{InterfaceID: "c", Outcome: Unavailable, NotActive: true},
	}, MaxSharedCandidates)
	if out.Resolution != ResIndeterminate || out.Reason != "INCOMPLETE_EVIDENCE" {
		t.Fatalf("%+v", out)
	}
}

func TestTheDefaultResolverBoundsTheVectorAtTheSupportedSharedSet(t *testing.T) {
	over := make([]Candidate, MaxSharedCandidates+1)
	for i := range over {
		over[i] = Candidate{InterfaceID: string(rune('a' + i)), Outcome: NoMatch}
	}
	if out := Resolve(over, MaxSharedCandidates); out.Resolution != ResIndeterminate || out.Reason != "CANDIDATE_CAP_EXCEEDED" {
		t.Fatalf("%+v", out)
	}
	if NewResolver(nil).MaxCandidates != MaxSharedCandidates {
		t.Fatal("the production resolver is built unbounded")
	}
}

func TestAmbiguityBeatsAnOutOfServiceCandidate(t *testing.T) {
	out := Resolve([]Candidate{
		{InterfaceID: "a", Outcome: Verified}, {InterfaceID: "b", Outcome: Verified},
		{InterfaceID: "c", Outcome: Unavailable, NotActive: true},
	}, MaxSharedCandidates)
	if out.Resolution != ResAmbiguous {
		t.Fatalf("%+v", out)
	}
}
