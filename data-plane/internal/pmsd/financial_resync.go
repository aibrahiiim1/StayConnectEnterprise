package pmsd

import "time"

// THE FINANCIAL MIRROR PROVES ITSELF (decision D48, migration 0103).
//
// A room charge needs the whole mirror to have been proven complete recently: a successful DS..DE resync within
// the interface's financial mirror maximum age. A live guest event updates its own stay but proves nothing about
// the records that may have been missed, so it does not count. A hotel can be quiet for hours on a perfectly
// healthy link, and without help the mirror would simply age out and room charge would stop. So pmsd asks for a
// read-only full resync itself once half the bound has passed (the database decides that: p4_financial_resync_due,
// which also requires the interface to be financially onboarded, connected and IN_SYNC with nothing in flight).
//
// STORM-PROOF BY CONSTRUCTION:
//   - it is consulted at most once a minute, from the read loop that owns the socket (the same single place an
//     operator's resync is claimed), and never while a DR is outstanding or a DS..DE window is open;
//   - after asking, it waits at least a backoff (10 minutes, doubling to 40) before it may ask again, reset only
//     by a successful complete resync;
//   - if the PMS does not even begin answering (no DS) within 5 minutes, the adapter releases its resync gate so
//     operator resyncs and posting are not held hostage by an ignored request -- the mirror age keeps running,
//     and if no complete resync succeeds before the bound, room charge fails closed in the database.

// FinancialResyncAsker is implemented by a sink that can say whether the financial mirror is due to be proven
// again. It is optional: a sink without it (a test double, a non-financial deployment) never triggers one.
type FinancialResyncAsker interface {
	FinancialResyncDue() bool
}

const (
	finResyncCheckEvery = time.Minute
	finResyncBackoffMin = 10 * time.Minute
	finResyncBackoffMax = 40 * time.Minute
	finResyncStartWait  = 5 * time.Minute
)

type finResyncScheduler struct {
	lastCheck     time.Time
	nextAllowed   time.Time
	requestedAt   time.Time
	backoff       time.Duration
	awaitingStart bool
}

// shouldRequest reports whether a financial resync should be requested now. ask is consulted only when every
// local guard allows it, and at most once per finResyncCheckEvery.
func (f *finResyncScheduler) shouldRequest(now time.Time, resyncing bool, ask func() bool) bool {
	if resyncing || ask == nil {
		return false
	}
	if !f.lastCheck.IsZero() && now.Sub(f.lastCheck) < finResyncCheckEvery {
		return false
	}
	f.lastCheck = now
	if now.Before(f.nextAllowed) {
		return false
	}
	return ask()
}

// requested records that this scheduler's DR went on the wire.
func (f *finResyncScheduler) requested(now time.Time) {
	if f.backoff == 0 {
		f.backoff = finResyncBackoffMin
	}
	f.requestedAt = now
	f.awaitingStart = true
	f.nextAllowed = now.Add(f.backoff)
	f.backoff *= 2
	if f.backoff > finResyncBackoffMax {
		f.backoff = finResyncBackoffMax
	}
}

// started records a DS: whatever asked for it, a roster is now arriving.
func (f *finResyncScheduler) started() { f.awaitingStart = false }

// completed records a DE: the mirror has just been proven, so the backoff starts again from its minimum.
func (f *finResyncScheduler) completed() {
	f.awaitingStart = false
	f.backoff = finResyncBackoffMin
}

// unanswered reports, once, that this scheduler's DR has not been answered with a DS within finResyncStartWait.
func (f *finResyncScheduler) unanswered(now time.Time) bool {
	if f.awaitingStart && now.Sub(f.requestedAt) > finResyncStartWait {
		f.awaitingStart = false
		return true
	}
	return false
}
