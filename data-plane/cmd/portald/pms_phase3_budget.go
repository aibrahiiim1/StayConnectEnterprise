package main

// THE RESPONSE-TIME BUDGET.
//
// pms_phase3.go makes every guest-visible non-success identical in CONTENT. That is only half the contract,
// because the other half is observable without reading a single byte of the body:
//
//   a malformed request           fails in microseconds, before anything is contacted;
//   a device not on the network   fails in microseconds, from a local neighbour lookup;
//   a wrong room                  fails in whatever the PMS takes to say "no" — tens of milliseconds;
//   an unreachable PMS            fails in whatever the upstream timeout is — seconds;
//   a throttled attempt           fails instantly, and its instantness IS the confirmation that the previous
//                                 attempts were interesting enough to throttle.
//
// An attacker who cannot read a difference in the answer can still read it in the clock, and the clock is
// enough: "this room number takes 400ms and every other room takes 4µs" enumerates the occupied rooms of the
// property without ever seeing a distinguishing message. The uniform body would be doing nothing.
//
// So every guest-visible non-success leaves at a FIXED wall-clock offset from when the request arrived. Not a
// minimum, not a jittered range — the same offset, whatever happened internally.
//
// Two consequences worth being explicit about, because both are deliberate:
//
//   The budget is a CEILING as well as a floor. Work that has not finished when the budget expires is
//   abandoned and the guest gets the uniform non-success at exactly the budget. A padded-but-unbounded design
//   leaks just as much: a 9-second answer is still distinguishable from a 900ms one no matter what floor sits
//   underneath it. Abandoning is safe here because a resolution retry is idempotent by request id (it returns
//   the same live Auth Context, §3.4) and a grant retry returns the session the abandoned attempt created
//   (see scd's grant idempotency), so an abandoned attempt costs the guest a retry, never their access.
//
//   SUCCESS is not padded. A guest who is in gets in immediately. Success is already distinguishable from
//   failure by its content — it names their session — so spending the guest's patience to hide a difference
//   that is published in the body would buy nothing.

import (
	"context"
	"net/http"
	"time"
)

// phase3FailureBudget is the fixed wall-clock every guest-visible non-success takes.
//
// It has to sit above the slowest HEALTHY resolve — otherwise ordinary guests get abandoned mid-flight on a
// property whose PMS is merely unexciting — and below the point where a captive portal reads as broken. The
// 5s upstream client timeout in main.go is the wrong scale for a page a guest is staring at; 1200ms covers a
// healthy PMS round trip through scd with room to spare, and a property whose PMS cannot answer inside it has
// an operational problem that the transport/continuity health surfaces name directly.
const phase3FailureBudget = 1200 * time.Millisecond

// phase3Clock is the seam the tests drive. Production uses the real one; a test can make the budget observable
// without spending 1.2 real seconds per case, and — more importantly — can assert the DEADLINE arithmetic
// rather than a wall-clock measurement that would be flaky on a loaded CI runner.
type phase3Clock interface {
	Now() time.Time
	SleepUntil(ctx context.Context, t time.Time)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// SleepUntil waits out the remainder of the budget. It honours cancellation so a guest who closes the page
// does not pin a goroutine for the rest of the budget.
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

// phase3Budget is one request's clock. It is created at the top of the handler, before any work, so the offset
// is measured from the guest's arrival rather than from whatever point the code happened to reach.
type phase3Budget struct {
	clock    phase3Clock
	deadline time.Time
	// ctx carries the same deadline to every upstream hop, which is what makes the budget a ceiling rather
	// than a hope. Cancelling it is the caller's job (the handler defers it).
	ctx    context.Context
	cancel context.CancelFunc
}

// newPhase3Budget starts the budget for one request and derives the deadline-bearing context the upstream hops
// run under.
func (h *handler) newPhase3Budget(r *http.Request) *phase3Budget {
	clk := h.clock
	if clk == nil {
		clk = realClock{}
	}
	deadline := clk.Now().Add(phase3FailureBudget)
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	return &phase3Budget{clock: clk, deadline: deadline, ctx: ctx, cancel: cancel}
}

// wait blocks until the budget is spent. It is called on EVERY guest-visible non-success and on no other path.
//
// It deliberately waits on the REQUEST's context, not the budget's: the budget context is already past its
// deadline in the abandoned case, so waiting on it would return instantly and hand back the very timing
// difference this exists to remove.
func (b *phase3Budget) wait(r *http.Request) {
	b.clock.SleepUntil(r.Context(), b.deadline)
}
