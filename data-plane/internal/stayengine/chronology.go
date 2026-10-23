package stayengine

import "time"

// Room-keyed departures (a GO with a room and no reservation) resolve against the one IN_HOUSE stay in the room,
// which is only right if the occupant has not changed since the event; waiting events are the likely ones to
// have gone stale after a resync. These functions are pure so the rule is testable without a database.

// eventAtFor uses the PMS timestamp when trustworthy. When it is absent or clock_suspect, the receipt time is
// the substitute: it is later than the true time, so it can only make the staleness test more permissive.
func eventAtFor(pmsAt *time.Time, receivedAt time.Time, clockSuspect bool) time.Time {
	if clockSuspect || pmsAt == nil || pmsAt.IsZero() {
		return receivedAt
	}
	return *pmsAt
}

// roomDepartureIsStale reports whether the stay found by room arrived after the departure event, i.e. applying
// it would check out a later occupant.
//
// The anchor must be stays.arrival, not occupancy_evidence_at: every GC in a full roster restamps the latter, so
// after a reconnect it reads as "now" and would flag almost every departure. Arrival is a date, so it is compared
// to the end of the event's day: same-day turnover is not stale. A stay with no arrival date is not judged.
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
