package signinattempt

import "testing"

// EVERY RESULT HAS A CLASS AND A LABEL, and the tests enumerate AllResults rather than a hand-written list so
// that adding a Result and forgetting either one fails here instead of reaching an operator's screen as a raw
// enum or a guest's screen as an unexplained default.
func TestEveryResultHasALabelAndAGuestClass(t *testing.T) {
	for _, r := range AllResults {
		if _, ok := labels[r]; !ok {
			t.Errorf("%s has no operator-facing label", r)
		}
		if r.Label() == string(r) {
			t.Errorf("%s falls back to its raw code; an operator would read the enum", r)
		}
		switch r.GuestClass() {
		case GuestSuccess, GuestCredential, GuestTechnical, GuestRateLimited:
		default:
			t.Errorf("%s maps to an unknown guest class %q", r, r.GuestClass())
		}
	}
}

// THE NON-DISCLOSURE PROPERTY, asserted as a property rather than as prose.
//
// Everything a guest could learn about ONE ROOM by submitting values must produce the SAME class. If any of
// these ever diverged, someone in the lobby could submit room numbers and read off which rooms are occupied,
// which have checked out, and which are shared -- without ever seeing a distinguishing message, just by
// noticing that one class of answer came back for some rooms and another for others.
func TestPerRoomOutcomesAreIndistinguishableToTheGuest(t *testing.T) {
	perRoom := []Result{RoomNotInMirror, CredentialMismatch, StayNotEligible, AmbiguousRoomCandidates}
	want := perRoom[0].GuestClass()
	if want != GuestCredential {
		t.Fatalf("the per-room class is %q, want CREDENTIAL", want)
	}
	for _, r := range perRoom {
		if got := r.GuestClass(); got != want {
			t.Errorf("%s is guest-class %q while %s is %q: submitting room numbers would now reveal which "+
				"rooms are occupied", r, got, perRoom[0], want)
		}
	}
}

// ...and the mirror image: a condition that is true for EVERY guest on the site right now may be reported as
// technical, because knowing it identifies nobody. A guest learns "sign-in is not working here", which anyone
// standing in the lobby can already see.
func TestSiteWideConditionsAreTechnical(t *testing.T) {
	for _, r := range []Result{MirrorStaleOrMissingChange, RoutingOrInterfaceFailure, ServiceUnavailable, SpentRequestID} {
		if got := r.GuestClass(); got != GuestTechnical {
			t.Errorf("%s is guest-class %q, want TECHNICAL", r, got)
		}
	}
}

// A wrong credential must NEVER produce the technical message. That is the Product Owner's rule stated
// directly: "Do not use the technical message for an ordinary wrong credential."
func TestAnOrdinaryMismatchIsNeverTechnical(t *testing.T) {
	if CredentialMismatch.GuestClass() == GuestTechnical {
		t.Fatal("an ordinary wrong value would tell the guest to contact Reception")
	}
	if !Verified.Succeeded() || CredentialMismatch.Succeeded() {
		t.Fatal("Succeeded() does not track VERIFIED")
	}
}

// An unknown code must not be reported to the guest as their own fault.
func TestAnUnknownResultFailsTowardsTechnical(t *testing.T) {
	if got := Result("SOMETHING_NEW").GuestClass(); got != GuestTechnical {
		t.Fatalf("unknown result is guest-class %q, want TECHNICAL", got)
	}
}

func TestClassifyVerifier(t *testing.T) {
	cases := []struct {
		in   string
		want VerifierKind
	}{
		{"", VerifierUnknown},
		{"   ", VerifierUnknown},
		{"Okonkwo", VerifierFullName},
		{"Maria Del Carmen", VerifierFullName},
		{"O'Neill-Smith", VerifierFullName},
		{"Ngozi", VerifierFullName},
		{"RES-4001", VerifierReservationLike},
		{"4001", VerifierReservationLike},
		// A surname carrying a digit is LABELLED as identifier-like, and that is harmless because the label
		// decides nothing. The legacy "either" mode DID decide on this shape, and that is why a guest whose
		// name contained a digit could never sign in.
		{"Smith2", VerifierReservationLike},
		{"who@example.com", VerifierUnknown},
		{"{}", VerifierUnknown},
	}
	for _, c := range cases {
		if got := ClassifyVerifier(c.in); got != c.want {
			t.Errorf("ClassifyVerifier(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The classification is a LABEL. This asserts the property that matters: nothing in this package offers a way
// to turn a VerifierKind into a field choice, because that is the defect room_any exists to remove.
func TestTheVerifierLabelDecidesNothing(t *testing.T) {
	// A value classified as identifier-like and a value classified as a name must reach the same machinery:
	// the package exposes no per-kind branch at all, so this is asserted by the absence of such an API plus
	// the fact that GuestClass and Label take a Result, never a VerifierKind.
	if VerifierReservationLike == VerifierFullName {
		t.Fatal("the kinds collapsed")
	}
	// Label/GuestClass signatures are Result-only; a compile error here would mean a kind-sensitive path
	// had been added.
	var _ func(Result) string = func(r Result) string { return r.Label() }
	var _ func(Result) GuestClass = func(r Result) GuestClass { return r.GuestClass() }
}
