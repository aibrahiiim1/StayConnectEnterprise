// Package ratebudget bounds how often a noisy, attacker-triggerable event is WRITTEN DOWN.
//
// Failed sign-ins and failed appliance authentications must be audited, but they arrive from unauthenticated
// callers: recording every one lets anyone grow the audit log as fast as they can send requests. A Budget
// admits at most PerKey events per key and Total events overall in each window; the caller records what is
// admitted and counts the rest (Suppressed), so the trail shows that a flood happened without becoming one.
package ratebudget

import (
	"sync"
	"time"
)

// Budget is a fixed-window, in-process counter. ctrlapi runs as one instance on Central, so in-process is
// authoritative (the same reasoning as applianceauth.ReplayCache).
type Budget struct {
	PerKey int
	Total  int
	Window time.Duration

	mu         sync.Mutex
	start      time.Time
	perKey     map[string]int
	total      int
	suppressed int
	now        func() time.Time
}

// New returns a budget of perKey events per key and total events overall per window.
func New(perKey, total int, window time.Duration) *Budget {
	return &Budget{PerKey: perKey, Total: total, Window: window, perKey: map[string]int{}, now: time.Now}
}

// Allow reports whether an event for key may be recorded now. When the previous window had suppressed
// events, suppressedBefore returns their count once, so the caller can record that a flood was cut short.
func (b *Budget) Allow(key string) (ok bool, suppressedBefore int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.Sub(b.start) >= b.Window {
		suppressedBefore = b.suppressed
		b.start, b.perKey, b.total, b.suppressed = now, map[string]int{}, 0, 0
	}
	if b.total >= b.Total || b.perKey[key] >= b.PerKey {
		b.suppressed++
		return false, suppressedBefore
	}
	b.perKey[key]++
	b.total++
	return true, suppressedBefore
}
