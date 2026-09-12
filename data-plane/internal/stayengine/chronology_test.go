package stayengine

// A ROOM NUMBER IS NOT AN IDENTITY.
//
// These tests exist because of a measured defect, not a hypothetical one. This PMS reports every departure
// as a GO carrying a room and no reservation number, an unmatched departure stays PENDING, and every
// reconnect restages the whole roster — so an old departure is re-offered against a room whose occupant has
// since changed. On the appliance this was written against, 83 of 397 distinct unresolved departures named a
// room whose current guest arrived AFTER the departure was raised. Without the rule below, each of those
// would have checked out a resident guest and revoked their access on the way past.

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

func TestRoomDepartureIsStale(t *testing.T) {
	event := at("2026-08-22T10:00:00Z")

	cases := []struct {
		name    string
		eventAt time.Time
		arrival *time.Time
		want    bool
		why     string
	}{
		{
			name: "a guest who arrived after the departure is a later occupant",
			// The whole defect in one row: the departure is from 22 August, the guest in that room arrived
			// on 10 September. Matching on the room alone would check them out.
			eventAt: event, arrival: ptr(at("2026-09-10T00:00:00Z")), want: true,
			why: "a later arrival must not be checked out by an older room-keyed departure",
		},
		{
			name:    "a guest already in the room is the subject of the departure",
			eventAt: event, arrival: ptr(at("2026-08-19T00:00:00Z")), want: false,
			why: "the ordinary case must still resolve, or every checkout goes to review",
		},
		{
			name: "a same-day arrival is NOT stale",
			// arrival is a DATE and lands at midnight, so a strict comparison against the event's instant
			// would call every same-day turnover a later occupant. Same-day turnover is ordinary; refusing
			// it would replace one useless backlog with another.
			eventAt: event, arrival: ptr(at("2026-08-22T00:00:00Z")), want: false,
			why: "same-day turnover is ordinary and must resolve normally",
		},
		{
			name:    "an arrival the day after the departure is stale",
			eventAt: event, arrival: ptr(at("2026-08-23T00:00:00Z")), want: true,
			why: "one day past the event's own day is the first unambiguous later occupant",
		},
		{
			name: "no arrival date cannot be judged, and is not judged",
			// An absent fact is not evidence of a later occupant. Refusing here would send stays that predate
			// the arrival column to review for no reason.
			eventAt: event, arrival: nil, want: false,
			why: "an absent fact must not manufacture a refusal",
		},
		{
			name:    "no event instant cannot be judged either",
			eventAt: time.Time{}, arrival: ptr(at("2026-09-10T00:00:00Z")), want: false,
			why: "with no event time there is no chronology to compare",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := roomDepartureIsStale(c.eventAt, c.arrival); got != c.want {
				t.Errorf("roomDepartureIsStale = %v, want %v — %s", got, c.want, c.why)
			}
		})
	}
}

// THE ANCHOR IS arrival, AND THIS IS WHY IT IS NOT occupancy_evidence_at.
//
// occupancy_evidence_at is the obvious-looking candidate and is wrong: it records when the PMS last SAID
// something about an occupancy, not when the occupancy began, and a full roster restamps it for everybody.
// Measured on the appliance this was written against: 449 of 839 in-house stays had been restamped within
// two hours of one resync, and 800 of 839 carried evidence later than their own arrival date. A rule anchored
// on it would have declared almost the entire property a "later occupant" after every reconnect.
//
// The signature is the test. roomDepartureIsStale takes the arrival date and nothing else, so the wrong fact
// cannot be passed to it by a later change without this file failing to compile.
func TestRoomDepartureStaleness_DoesNotDependOnRestampedEvidence(t *testing.T) {
	event := at("2026-08-22T10:00:00Z")
	arrival := at("2026-08-19T00:00:00Z") // the guest this departure is about

	// A resync happening right now would set occupancy_evidence_at to "now" for this stay. The rule must be
	// unaffected: the guest arrived before the departure and is still its subject.
	if roomDepartureIsStale(event, ptr(arrival)) {
		t.Fatal("a stay whose evidence was restamped by a resync was judged a later occupant")
	}
}

// eventAtFor decides WHICH instant a departure describes. A bad source clock must make the test more
// permissive, never less: refusing to apply a departure has a cost of its own.
func TestEventAtFor(t *testing.T) {
	pms := at("2026-08-22T10:00:00Z")
	recv := at("2026-08-22T10:00:30Z")

	if got := eventAtFor(ptr(pms), recv, false); !got.Equal(pms) {
		t.Errorf("a trusted PMS timestamp must be used: got %v", got)
	}
	if got := eventAtFor(ptr(pms), recv, true); !got.Equal(recv) {
		t.Errorf("a clock-suspect timestamp must fall back to receipt: got %v", got)
	}
	// NULL, not zero. pms_timestamp_utc is a nullable column and arrives here as a nil pointer whenever the
	// connector could not derive a trustworthy instant — which is ordinary, and must not panic or be read as
	// "the beginning of time", which would make every arrival look like a later occupant.
	if got := eventAtFor(nil, recv, false); !got.Equal(recv) {
		t.Errorf("an ABSENT PMS timestamp must fall back to receipt: got %v", got)
	}
	if got := eventAtFor(ptr(time.Time{}), recv, false); !got.Equal(recv) {
		t.Errorf("a zero PMS timestamp must fall back to receipt: got %v", got)
	}
	// The fallback is LATER than the true event time, so it can only widen the window in which a stay counts
	// as "already here". That direction is deliberate.
	if !recv.After(pms) {
		t.Fatal("fixture is wrong: receipt should be after the PMS instant")
	}
	arrival := at("2026-08-22T00:00:00Z")
	if roomDepartureIsStale(eventAtFor(ptr(pms), recv, true), ptr(arrival)) {
		t.Error("the clock-suspect fallback made a same-day arrival look like a later occupant")
	}
}
