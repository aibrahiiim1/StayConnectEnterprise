package main

// THE BOUNDED KERNEL LEASE.
//
// Packet authorization is the one piece of Phase-3 state that keeps working when everything else stops. A tc
// class that nobody maintains meters nothing important; a Session row that nobody updates is merely stale. An
// nft element that nobody maintains KEEPS A GUEST ON THE INTERNET — past their entitlement window, past
// checkout, past a revocation, for as long as the appliance stays up.
//
// A permanent element therefore makes every liveness property of this system load-bearing for access control:
// the guest goes offline only if acctd is alive to derive the plan, AND netd is alive to apply it, AND the two
// can still reach each other, AND the database is readable. Any one of those failing turns "access ends at
// 11:00" into "access ends whenever somebody notices".
//
// So Phase-3 authorization is never permanent. It is a LEASE, and the kernel is the thing that enforces it:
//
//	the lease is short enough that a dead producer fails closed within a documented, bounded interval;
//	a healthy reconciliation RENEWS it, so a live system never interrupts a guest;
//	an unhealthy one does not, so silence expires access rather than preserving it;
//	and it is clamped by the session's own hard boundary, so a crash-and-restart cycle can never push a
//	guest past the deadline their entitlement actually states.
//
// The last clause is what keeps the business deadline fixed. Renewal moves the KERNEL's expiry forward, never
// the entitlement's: leaseFor always re-derives the remaining time from AccessEndsAt, so however many times a
// session is renewed, the final lease ends at the boundary and not one second later.

import (
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	// phase3LeaseTTL is the bounded lease a healthy, converged session holds.
	//
	// It is deliberately equal to the producer's plan validity (acctd's planValidity). That coupling is the
	// whole safety argument in one sentence: netd already reports a plan older than this as ACTIVE_STALE —
	// "the producer went quiet and what is installed is no longer known to be correct" — and with the lease
	// set to the same span, the kernel STOPS FORWARDING at the same moment the appliance stops being able to
	// vouch for what it is forwarding. A longer lease would mean access outliving the last statement that
	// justified it; a much shorter one would cut guests off during an ordinary producer hiccup.
	//
	// Bounded interval, stated plainly: after the last successful reconciliation of a session, that session
	// loses internet access within 90 seconds unless something renews it.
	phase3LeaseTTL = 90 * time.Second

	// phase3LeaseRenewBelow is the remaining time at which a healthy pass refreshes the lease.
	//
	// Renewing every tick would be one nft transaction per guest per second for no benefit. Renewing only at
	// the very end would make a single missed pass fatal. Refreshing below two thirds leaves roughly 60s — 60
	// reconciliation ticks — of margin, so an ordinary hiccup is invisible to guests while a genuinely dead
	// producer still expires inside the documented bound.
	phase3LeaseRenewBelow = 60 * time.Second

	// phase3ProvisionalLease is the lease a guest holds while their durable ACTIVE state is NOT yet proven.
	//
	// Between "both kernel halves are in force" and "the Session is durably active" there is a real interval,
	// and a crash or an unreadable database can make it permanent. A guest in that interval gets a short lease
	// instead of a full one, so an activation whose outcome was never confirmed cannot become lasting internet
	// access: if nothing proves the promotion, the kernel drops the authorization within this bound.
	phase3ProvisionalLease = 15 * time.Second

	// phase3ActivationGrace bounds how long a session may keep being re-issued a provisional lease.
	//
	// A transient database blip should not disconnect a guest whose enforcement is genuinely correct, so the
	// provisional lease is renewed while the promotion keeps being retried. But "keep retrying forever" is how
	// a provisional state becomes a permanent one, so past this grace the session is failed closed outright:
	// authorization revoked and proven gone, then the accountable class torn down.
	phase3ActivationGrace = 30 * time.Second

	// unprovenRecordHorizon bounds how long a spent quarantine record is kept in the durable file. It is far
	// longer than the largest backoff, so a session still being retried can never be pruned out from under its
	// own countdown; it exists only so the file does not grow without bound across a long uptime.
	unprovenRecordHorizon = 24 * time.Hour
)

// leaseFor derives the lease to install for one desired session.
//
// It returns the full lease clamped by the session's hard access boundary, and false when no lease may be
// issued at all. A caller that treated a non-positive lease as "no timeout" would install exactly the
// permanent element this file exists to prevent, so the boolean is the answer and the duration is never to be
// used without it.
//
// THE CLAMP IS ROUNDED DOWN, NOT UP, AND THIS IS THE WHOLE POINT.
//
// nft's element timeout granularity is whole seconds. An entitlement that ends in 1.9 seconds therefore has
// only two representable leases: 1s, or 2s — and 2s is 100ms PAST the deadline the business stated. Rounding
// up would mean the clamp silently permits what it exists to forbid, on every boundary that does not land on
// a whole second, which is almost all of them. Rounding down costs the guest at most the last 999ms of a
// stay, which nobody can perceive, and holds the invariant exactly:
//
//	kernel authorization expiry <= the exact hard access boundary, for every timestamp precision.
//
// Below one representable second there is no safe lease, so the session is not authorized. That is a real
// (if brief) loss of access at the very end of an entitlement, and it is the correct direction: the
// alternative is forwarding a guest past a deadline because a rounding rule preferred them to a contract.
//
// A session with NO boundary keeps the ordinary 90-second lease unchanged; none of this applies to it.
func leaseFor(s shapeplan.Session, now time.Time) (time.Duration, bool, string) {
	if s.AccessEndsAt == nil {
		return phase3LeaseTTL, true, ""
	}
	remaining := s.AccessEndsAt.Sub(now)
	if remaining <= 0 {
		// The entitlement's window has already elapsed. The plan still lists the session (the producer
		// derives from durable state, and the expiry sweep may not have run yet), but no lease may be
		// issued for access that has ended.
		return 0, false, "access boundary has already passed"
	}
	if remaining >= phase3LeaseTTL {
		// Far from the boundary: the ordinary lease is already shorter than the time remaining, so the clamp
		// does not bind and no rounding question arises.
		return phase3LeaseTTL, true, ""
	}
	// Clamped. Truncate to whole seconds — never up.
	ttl := remaining.Truncate(time.Second)
	if ttl < time.Second {
		return 0, false, "less than one representable second of access remains; no lease can be issued without " +
			"expiring past the boundary"
	}
	return ttl, true, ""
}

// needsRenewal reports whether an installed lease is close enough to expiry to refresh on this pass.
//
// remaining is what the kernel says is LEFT, not what the element was created with. A zero remaining from a
// set that reports no expiry (an element created without a timeout by an older build, or a set whose flags do
// not include timeout) is treated as needing renewal: an unbounded element is precisely the state that must be
// converted into a bounded one at the first opportunity.
func needsRenewal(remaining, want time.Duration) bool {
	if remaining <= 0 {
		return true
	}
	threshold := phase3LeaseRenewBelow
	if want < threshold {
		// A lease clamped by a near boundary is always shorter than the standard threshold; renewing it on
		// every pass is correct, because each pass re-derives a smaller remaining time and the guest must go
		// offline exactly at the boundary.
		threshold = want
	}
	return remaining < threshold
}
