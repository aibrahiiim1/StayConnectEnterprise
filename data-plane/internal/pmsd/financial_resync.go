package pmsd

import "time"

// Room charge needs a complete DS..DE resync within the financial mirror max age; live events don't prove
// completeness. pmsd requests a read-only resync itself at half the bound (the database decides:
// p4_financial_resync_due). Storm-proof: consulted at most once a minute from the socket-owning read loop,
// never while a DR is outstanding or a DS..DE window is open; backoff 10 min doubling to 40, reset only by a
// complete resync; if no DS arrives within 5 minutes the resync gate is released so operator resyncs and
// posting are not held up (room charge still fails closed in the database when the bound passes).

// FinancialResyncAsker is optional: a sink without it never triggers a financial resync.
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

func (f *finResyncScheduler) started() { f.awaitingStart = false }

func (f *finResyncScheduler) completed() {
	f.awaitingStart = false
	f.backoff = finResyncBackoffMin
}

func (f *finResyncScheduler) unanswered(now time.Time) bool {
	if f.awaitingStart && now.Sub(f.requestedAt) > finResyncStartWait {
		f.awaitingStart = false
		return true
	}
	return false
}
