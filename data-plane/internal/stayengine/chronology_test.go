package stayengine

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
			name:    "a guest who arrived after the departure is a later occupant",
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
			// arrival is a DATE (midnight), so a strict comparison would call every same-day turnover stale.
			eventAt: event, arrival: ptr(at("2026-08-22T00:00:00Z")), want: false,
			why: "same-day turnover is ordinary and must resolve normally",
		},
		{
			name:    "an arrival the day after the departure is stale",
			eventAt: event, arrival: ptr(at("2026-08-23T00:00:00Z")), want: true,
			why: "one day past the event's own day is the first unambiguous later occupant",
		},
		{
			name:    "no arrival date cannot be judged, and is not judged",
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

// The anchor is arrival, not occupancy_evidence_at: a full roster resync restamps the latter for everybody.
func TestRoomDepartureStaleness_DoesNotDependOnRestampedEvidence(t *testing.T) {
	event := at("2026-08-22T10:00:00Z")
	arrival := at("2026-08-19T00:00:00Z")

	if roomDepartureIsStale(event, ptr(arrival)) {
		t.Fatal("a stay whose evidence was restamped by a resync was judged a later occupant")
	}
}

// A bad source clock must make the staleness test more permissive, never less.
func TestEventAtFor(t *testing.T) {
	pms := at("2026-08-22T10:00:00Z")
	recv := at("2026-08-22T10:00:30Z")

	if got := eventAtFor(ptr(pms), recv, false); !got.Equal(pms) {
		t.Errorf("a trusted PMS timestamp must be used: got %v", got)
	}
	if got := eventAtFor(ptr(pms), recv, true); !got.Equal(recv) {
		t.Errorf("a clock-suspect timestamp must fall back to receipt: got %v", got)
	}
	// pms_timestamp_utc is nullable; nil must not panic or read as "the beginning of time".
	if got := eventAtFor(nil, recv, false); !got.Equal(recv) {
		t.Errorf("an ABSENT PMS timestamp must fall back to receipt: got %v", got)
	}
	if got := eventAtFor(ptr(time.Time{}), recv, false); !got.Equal(recv) {
		t.Errorf("a zero PMS timestamp must fall back to receipt: got %v", got)
	}
	// The fallback is later than the true event time, so it only widens the "already here" window.
	if !recv.After(pms) {
		t.Fatal("fixture is wrong: receipt should be after the PMS instant")
	}
	arrival := at("2026-08-22T00:00:00Z")
	if roomDepartureIsStale(eventAtFor(ptr(pms), recv, true), ptr(arrival)) {
		t.Error("the clock-suspect fallback made a same-day arrival look like a later occupant")
	}
}
