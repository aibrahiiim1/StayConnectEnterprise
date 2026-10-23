// Package ratebudget caps how many attacker-triggerable events (failed sign-ins) are written to the audit log,
// so unauthenticated callers cannot grow it without bound while a flood still leaves a trace.
package ratebudget

import (
	"sync"
	"time"
)

// Budget is a fixed-window, in-process counter; ctrlapi is a single instance, so in-process is authoritative.
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

func New(perKey, total int, window time.Duration) *Budget {
	return &Budget{PerKey: perKey, Total: total, Window: window, perKey: map[string]int{}, now: time.Now}
}

// Allow reports whether an event for key may be recorded. suppressedBefore returns the previous window's
// suppressed count once, so the caller can record that a flood was cut short.
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
