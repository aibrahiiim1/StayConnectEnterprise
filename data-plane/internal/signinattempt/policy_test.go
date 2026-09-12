package signinattempt

import "testing"

// THE COUNTING SET IS CLOSED, and this walks every Result so that adding one and forgetting to decide
// whether it counts fails here rather than in a hotel lobby.
func TestOnlyWrongCredentialsCount(t *testing.T) {
	want := map[Result]bool{
		CredentialMismatch: true,
		RoomNotInMirror:    true,
	}
	for _, r := range AllResults {
		if got := r.CountsAsCredentialFailure(); got != want[r] {
			t.Errorf("%s counts=%v, want %v", r, got, want[r])
		}
	}
	if len(CountingResults) != len(want) {
		t.Fatalf("CountingResults has %d entries, want %d — the data and the function disagree",
			len(CountingResults), len(want))
	}
	for _, r := range CountingResults {
		if !r.CountsAsCredentialFailure() {
			t.Errorf("%s is in CountingResults but does not count", r)
		}
	}
}

// A TECHNICAL FAILURE MUST NEVER RESTRICT A GUEST. Stated as its own test because it is the rule most likely
// to be broken by someone "simplifying" the set to "everything that is not VERIFIED".
func TestNoTechnicalFailureCounts(t *testing.T) {
	for _, r := range AllResults {
		if r.GuestClass() == GuestTechnical && r.CountsAsCredentialFailure() {
			t.Errorf("%s is a technical failure and counts as a wrong credential; during an outage every "+
				"guest in the hotel would be restricted within five taps", r)
		}
	}
}

// An attempt refused BY the policy must not feed it, or a sixty-second restriction becomes permanent for
// anyone who keeps tapping.
func TestARateLimitedAttemptDoesNotFeedThePolicy(t *testing.T) {
	if RateLimited.CountsAsCredentialFailure() {
		t.Fatal("a blocked attempt extends the restriction that blocked it")
	}
	if Verified.CountsAsCredentialFailure() {
		t.Fatal("a success counts as a failure")
	}
}

func TestDefaultsAreTheApprovedValuesAndAreValid(t *testing.T) {
	d := Defaults()
	if d.MaxFailedAttempts != 5 || d.WindowSeconds != 60 || d.RestrictionSeconds != 60 {
		t.Fatalf("defaults = %+v, want the approved 5 / 60s / 60s", d)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("the approved defaults are rejected by our own bounds: %v", err)
	}
}

// THE LOWER BOUNDS ARE THE ONES THAT PROTECT GUESTS. A threshold of one would restrict somebody for a single
// typo; a five-second window would make the control invisible. These assert the refusals, and they assert
// that the message names the unit and the range, because an operator typing into a form needs to know what
// to type instead.
func TestValidateRefusesValuesThatWouldHurtGuests(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		want string
	}{
		{"one attempt", Policy{1, 60, 60}, "between 3 and 20"},
		{"zero attempts", Policy{0, 60, 60}, "between 3 and 20"},
		{"absurdly many attempts", Policy{100, 60, 60}, "between 3 and 20"},
		{"a five second window", Policy{5, 5, 60}, "between 30 and 3600 seconds"},
		{"a day-long window", Policy{5, 90000, 60}, "between 30 and 3600 seconds"},
		{"a two second restriction", Policy{5, 60, 2}, "between 30 and 3600 seconds"},
		{"a day-long restriction", Policy{5, 60, 90000}, "between 30 and 3600 seconds"},
		{"the zero policy", Policy{}, "between 3 and 20"},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if err == nil {
			t.Errorf("%s: accepted %+v", c.name, c.p)
			continue
		}
		if !contains(err.Error(), c.want) {
			t.Errorf("%s: message %q does not tell the operator the accepted range %q", c.name, err, c.want)
		}
	}
}

func TestValidateAcceptsTheEdgesOfTheAcceptedRange(t *testing.T) {
	for _, p := range []Policy{
		{MinFailedAttempts, MinWindowSeconds, MinRestrictionSeconds},
		{MaxFailedAttemptsCap, MaxWindowSeconds, MaxRestrictionSeconds},
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v is on the boundary and was refused: %v", p, err)
		}
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
