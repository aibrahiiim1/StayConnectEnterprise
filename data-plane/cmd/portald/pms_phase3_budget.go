package main

// The uniform body is only half the contract: response time also leaks (a wrong room takes the PMS's tens of
// ms, a malformed request microseconds, an unreachable PMS seconds, a throttled attempt is instant). So every
// guest-visible non-success leaves at one fixed offset from arrival, whatever happened internally.
//
// The budget is a ceiling too: unfinished work is abandoned and the uniform non-success goes out at exactly
// the budget. That is safe because resolution retries are idempotent by request id and a grant retry returns
// the abandoned attempt's session. Success is not padded; its body already differs.

import (
	"context"
	"net/http"
	"time"
)

// phase3FailureBudget is the one fixed offset for every guest-visible non-success. It must exceed the
// slowest healthy connect, which waits for real enforcement: up to one producer tick (1s) + the pass's
// database and kernel work + scd's 100ms promotion polling + two local hops. A shorter budget abandons
// healthy guests mid-convergence. It must not be split (short for identity failures, long for connection
// failures): the difference would enumerate the property just as a distinguishing message would.
// TestIntegration_EnforcementCycle_ConvergesWellInsideTheGuestBudget measures the cycle at ~66ms.
const phase3FailureBudget = 2500 * time.Millisecond

// phase3EnforcementReserve is held back from upstream hops so an abandoned attempt still answers at the
// budget, not after it.
const phase3EnforcementReserve = 200 * time.Millisecond

// phase3Clock is the test seam, so deadline arithmetic can be asserted without wall-clock flakiness.
type phase3Clock interface {
	Now() time.Time
	SleepUntil(ctx context.Context, t time.Time)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// SleepUntil honours cancellation so a closed page does not pin a goroutine.
func (realClock) SleepUntil(ctx context.Context, t time.Time) {
	d := time.Until(t)
	if d <= 0 {
		return
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
	case <-ctx.Done():
	}
}

// phase3Budget is one request's clock, created before any work so the offset runs from arrival.
type phase3Budget struct {
	clock    phase3Clock
	deadline time.Time
	// ctx carries the (earlier) upstream deadline to every hop; the handler defers cancel.
	ctx    context.Context
	cancel context.CancelFunc
}

func (h *handler) newPhase3Budget(r *http.Request) *phase3Budget {
	clk := h.clock
	if clk == nil {
		clk = realClock{}
	}
	deadline := clk.Now().Add(phase3FailureBudget)
	// The upstream deadline is earlier than the guest-visible one; scd derives its enforcement wait from it.
	ctx, cancel := context.WithDeadline(r.Context(), deadline.Add(-phase3EnforcementReserve))
	return &phase3Budget{clock: clk, deadline: deadline, ctx: ctx, cancel: cancel}
}

// wait runs on every guest-visible non-success and no other path. It waits on the request's context, not the
// budget's, which is already expired in the abandoned case and would return instantly.
func (b *phase3Budget) wait(r *http.Request) {
	b.clock.SleepUntil(r.Context(), b.deadline)
}
