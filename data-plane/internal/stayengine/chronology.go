package stayengine

import "time"

// Chronology for room-keyed departures.
//
// This PMS reports every departure as a GO carrying a room and no reservation number. Resolving that against
// "the one IN_HOUSE stay in this room" is correct only while the room's occupant has not changed since the
// event was raised — and the events that most need resolving are precisely the ones that have been waiting,
// because a full resync restages every unapplied record on every reconnect.
//
// These two functions are pure so the rule can be tested without a database, and so the decision an operator
// reads about later is the same decision the engine made.

// eventAtFor picks the instant a departure event describes.
//
// The PMS's own timestamp is the right answer and is used whenever it can be trusted. It is NULLABLE, which
// is why it arrives here as a pointer: the connector leaves it unset whenever it could not derive a
// trustworthy instant from the frame, and that is an ordinary outcome rather than an error.
//
// clock_suspect means the connector caught the source clock disagreeing with ours badly enough to be unsafe
// for ordering. In that case — and when the timestamp is absent — the instant WE received the record is the
// honest substitute: it is later than the true event time, so it can only make the staleness test more
// permissive, never less. The test must not invent staleness out of a bad clock, because refusing to apply a
// departure has a cost of its own.
func eventAtFor(pmsAt *time.Time, receivedAt time.Time, clockSuspect bool) time.Time {
	if clockSuspect || pmsAt == nil || pmsAt.IsZero() {
		return receivedAt
	}
	return *pmsAt
}

// roomDepartureIsStale reports whether the single IN_HOUSE stay found by room alone arrived AFTER the
// departure event — i.e. whether applying it would check out a later occupant.
//
// THE ANCHOR IS stays.arrival, AND IT IS THE ONLY ONE THAT ANSWERS THE QUESTION. The obvious candidate is
// stays.occupancy_evidence_at, and it is wrong: it records when the PMS last SAID something about this
// occupancy, not when the occupancy began. Every GC in a full roster restamps it, so after a reconnect it
// reads as "now" for most of the property — on the appliance this was written against, 449 of 839 in-house
// stays had been restamped within two hours of a resync, and 800 of 839 carried evidence later than their
// own arrival date. A rule built on it would have called almost every departure a later occupant and
// referred the whole backlog to a human for no reason.
//
// arrival is the PMS's own statement of when this guest came in. It is a DATE, so the comparison is against
// the END of the event's day: a guest who arrived on the same day the departure was raised is NOT judged
// stale. That is the forgiving direction, deliberately — same-day turnover is ordinary, and a rule that
// referred every same-day checkout to review would replace one useless backlog with another.
//
// A stay with no arrival date cannot be judged, and is not judged: an absent fact is not evidence of a later
// occupant, so the answer is false and the event resolves exactly as it did before.
func roomDepartureIsStale(eventAt time.Time, arrival *time.Time) bool {
	if eventAt.IsZero() || arrival == nil {
		return false
	}
	return arrival.After(endOfDay(eventAt))
}

func endOfDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 23, 59, 59, int(time.Second-1), time.UTC)
}
