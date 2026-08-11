package pmsresolve

import "testing"

func c(id string, o CandidateOutcome) Candidate { return Candidate{InterfaceID: id, Outcome: o} }

// D1: namespace independence — two interfaces each report on their OWN scope; exactly one VERIFIED + the other
// NO_MATCH resolves to that interface (room numbers are never globally unique; each candidate is independent).
func TestD1_NamespaceIndependence(t *testing.T) {
	o := Resolve([]Candidate{c("if-A", Verified), c("if-B", NoMatch)}, 8)
	if o.Resolution != ResVerified || o.InterfaceID != "if-A" {
		t.Fatalf("D1: got %+v, want VERIFIED if-A", o)
	}
}

// D2: ambiguity → discriminator escalation (never auto-pick).
func TestD2_AmbiguityEscalates(t *testing.T) {
	if o := Resolve([]Candidate{c("A", Verified), c("B", Verified)}, 8); o.Resolution != ResAmbiguous {
		t.Fatalf("D2 (2 verified): got %+v, want AMBIGUOUS", o)
	}
	if o := Resolve([]Candidate{c("A", AmbiguousLocal), c("B", NoMatch)}, 8); o.Resolution != ResAmbiguous {
		t.Fatalf("D2 (ambiguous_local): got %+v, want AMBIGUOUS", o)
	}
}

// D3: a slow VERIFIED beats a fast NO_MATCH — the decision runs on the COMPLETE vector, so ordering/timing is
// irrelevant to the outcome (fast NO_MATCH first, slow VERIFIED second still resolves VERIFIED).
func TestD3_SlowVerifiedBeatsFastNoMatch(t *testing.T) {
	o := Resolve([]Candidate{c("fast", NoMatch), c("slow", Verified)}, 8)
	if o.Resolution != ResVerified || o.InterfaceID != "slow" {
		t.Fatalf("D3: got %+v, want VERIFIED slow", o)
	}
}

// D4: any UNAVAILABLE/STALE/UNSUPPORTED → INDETERMINATE fail-closed, even alongside a VERIFIED (we cannot
// prove exactly-one).
func TestD4_IndeterminateFailsClosed(t *testing.T) {
	for _, bad := range []CandidateOutcome{Unavailable, Stale, UnsupportedEvidence} {
		o := Resolve([]Candidate{c("A", Verified), c("B", bad)}, 8)
		if o.Resolution != ResIndeterminate {
			t.Fatalf("D4 (%s): got %+v, want INDETERMINATE", bad, o)
		}
	}
}

// D5: unmapped (no candidates) fails closed.
func TestD5_UnmappedFailsClosed(t *testing.T) {
	if o := Resolve(nil, 8); o.Resolution != ResIndeterminate || o.Reason != "UNMAPPED" {
		t.Fatalf("D5: got %+v, want INDETERMINATE/UNMAPPED", o)
	}
}

// D6: over the candidate cap fails closed (runtime enforcement).
func TestD6_CandidateCap(t *testing.T) {
	cands := []Candidate{c("A", NoMatch), c("B", NoMatch), c("C", Verified)}
	if o := Resolve(cands, 2); o.Resolution != ResIndeterminate || o.Reason != "CANDIDATE_CAP_EXCEEDED" {
		t.Fatalf("D6: got %+v, want INDETERMINATE/CANDIDATE_CAP_EXCEEDED", o)
	}
	// within the cap it resolves normally
	if o := Resolve(cands, 8); o.Resolution != ResVerified {
		t.Fatalf("D6 within cap: got %+v, want VERIFIED", o)
	}
}

// D7: forged client hints cannot influence the outcome — Resolve has no hint input; only the trusted-network
// candidate vector decides. (Structural guarantee: identical vector → identical outcome regardless of any
// out-of-band hint the caller might hold.)
func TestD7_ForgedHintsIgnored(t *testing.T) {
	vec := []Candidate{c("A", Verified), c("B", NoMatch)}
	if Resolve(vec, 8) != Resolve(vec, 8) {
		t.Fatal("D7: outcome must depend ONLY on the candidate vector")
	}
}

// D8 / success rule: exactly one VERIFIED with every other determinate NO_MATCH publishes that interface;
// nothing else is guest-visible.
func TestD8_PublishRule(t *testing.T) {
	o := Resolve([]Candidate{c("A", NoMatch), c("B", Verified), c("C", NoMatch)}, 8)
	if !o.GuestVisibleSuccess() || o.InterfaceID != "B" {
		t.Fatalf("D8: got %+v, want guest-visible VERIFIED B", o)
	}
	// any non-success is NOT guest-visible
	if Resolve([]Candidate{c("A", Unavailable)}, 8).GuestVisibleSuccess() {
		t.Fatal("D8: a non-success must never be guest-visible")
	}
}

// D10: all determinate NO_MATCH (e.g. a checked-out / absent Stay everywhere) → NO_MATCH (no fabrication).
func TestD10_AllNoMatch(t *testing.T) {
	if o := Resolve([]Candidate{c("A", NoMatch), c("B", NoMatch)}, 8); o.Resolution != ResNoMatch {
		t.Fatalf("D10: got %+v, want NO_MATCH", o)
	}
}

// D11: every non-success outcome is uniform to the guest (not guest-visible) and carries a bounded internal
// reason code; a single VERIFIED is the only guest-visible success.
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
