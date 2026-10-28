package main

// Phase-3 authorization is never permanent: a lease the kernel enforces, renewed by healthy reconciliation
// and clamped by the session's hard boundary, so a dead producer fails closed within a bounded interval and
// no renewal can push a guest past AccessEndsAt.

import (
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	// phase3LeaseTTL equals the producer's plan validity (acctd's planValidity), so the kernel stops forwarding
	// when netd starts reporting the plan ACTIVE_STALE. A session loses access within this bound unless renewed.
	phase3LeaseTTL = 90 * time.Second

	// phase3LeaseRenewBelow leaves about 60 ticks of margin so one missed pass is invisible, while a dead
	// producer still expires inside the bound.
	phase3LeaseRenewBelow = 60 * time.Second

	// phase3ProvisionalLease is the short lease held while durable ACTIVE state is not yet proven, so an
	// unconfirmed activation cannot become lasting access.
	phase3ProvisionalLease = 15 * time.Second

	// phase3ActivationGrace bounds how long a provisional lease is re-issued; past it the session fails closed.
	phase3ActivationGrace = 30 * time.Second
)

// leaseFor returns the lease clamped by the hard access boundary; false means no lease may be issued, and
// the duration must not be used then (a non-positive TTL would install a permanent element).
//
// The clamp rounds DOWN: nft timeouts are whole seconds, so rounding up would let the element outlive the
// boundary by up to 999ms. Below one second there is no safe lease. Invariant: kernel expiry <= boundary.
func leaseFor(s shapeplan.Session, now time.Time) (time.Duration, bool, string) {
	if s.AccessEndsAt == nil {
		return phase3LeaseTTL, true, ""
	}
	remaining := s.AccessEndsAt.Sub(now)
	if remaining <= 0 {
		// The plan may still list the session before the expiry sweep runs.
		return 0, false, "access boundary has already passed"
	}
	if remaining >= phase3LeaseTTL {
		return phase3LeaseTTL, true, ""
	}
	ttl := remaining.Truncate(time.Second)
	if ttl < time.Second {
		return 0, false, "less than one representable second of access remains; no lease can be issued without " +
			"expiring past the boundary"
	}
	return ttl, true, ""
}

// needsRenewal: remaining is what the kernel reports left. Zero (no expiry reported) needs renewal, to turn
// an unbounded element into a bounded one.
func needsRenewal(remaining, want time.Duration) bool {
	if remaining <= 0 {
		return true
	}
	threshold := phase3LeaseRenewBelow
	if want < threshold {
		// A boundary-clamped lease is renewed every pass so each pass re-derives the smaller remaining time.
		threshold = want
	}
	return remaining < threshold
}
