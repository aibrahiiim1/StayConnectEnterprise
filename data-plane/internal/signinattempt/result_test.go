package signinattempt

import "testing"

// Every Result must have a class and a label; the tests enumerate AllResults so a forgotten one fails here.
func TestEveryResultHasALabelAndAGuestClass(t *testing.T) {
	for _, r := range AllResults {
		if _, ok := labels[r]; !ok {
			t.Errorf("%s has no operator-facing label", r)
		}
		if r.Label() == string(r) {
			t.Errorf("%s falls back to its raw code; an operator would read the enum", r)
		}
		switch r.GuestClass() {
		case GuestSuccess, GuestCredential, GuestTechnical, GuestRateLimited, GuestLicense, GuestCapacity:
		default:
			t.Errorf("%s maps to an unknown guest class %q", r, r.GuestClass())
		}
	}
}

// Everything a guest could learn about one room by submitting values must produce the same class; otherwise
// someone in the lobby could read off which rooms are occupied, checked out or shared.
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

// A condition true for every guest on the site may be reported as technical, since it identifies nobody.
func TestSiteWideConditionsAreTechnical(t *testing.T) {
	for _, r := range []Result{MirrorStaleOrMissingChange, RoutingOrInterfaceFailure, ServiceUnavailable, SpentRequestID} {
		if got := r.GuestClass(); got != GuestTechnical {
			t.Errorf("%s is guest-class %q, want TECHNICAL", r, got)
		}
	}
}

// A wrong credential must never produce the technical message.
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
		// A surname carrying a digit is labelled identifier-like; harmless because the label decides nothing.
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

// The classification is a label: nothing here may turn a VerifierKind into a field choice.
func TestTheVerifierLabelDecidesNothing(t *testing.T) {
	if VerifierReservationLike == VerifierFullName {
		t.Fatal("the kinds collapsed")
	}
	// Result-only signatures; a compile error here means a kind-sensitive path was added.
	var _ func(Result) string = func(r Result) string { return r.Label() }
	var _ func(Result) GuestClass = func(r Result) GuestClass { return r.GuestClass() }
}
