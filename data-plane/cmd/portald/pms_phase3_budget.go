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
// It has to sit above the slowest HEALTHY connection attempt — otherwise ordinary guests get abandoned
// mid-flight on a property that is merely unexciting — and below the point where a captive portal reads as
// broken. The 5s upstream client timeout in main.go is the wrong scale for a page a guest is staring at.
//
// WHY IT IS 2500ms AND NOT 1200ms.
//
// It used to be 1200ms, sized against a PMS round trip through scd, which was the whole of the work at the
// time. It is not any more. Guest-visible success now waits for the guest to be genuinely ENFORCED — a
// Session is only reported connected once the kernel is actually authorizing and metering it — and that
// convergence is produced by the enforcement owner's own reconciliation pass, not by the request. So a
// healthy connect costs, in the worst ordinary case:
//
//	up to one full reconciliation interval waiting for the next pass (the producer ticks every second)
//	+ the pass's own database and kernel work
//	+ the polling granularity at which scd notices the promotion
//	+ two local unix-socket hops.
//
// Against a 1200ms budget that arithmetic does not fit, and the failure mode is the ugly one: a guest whose
// grant is perfectly healthy is abandoned mid-convergence and told, in the uniform non-success wording, that
// they are not connected — reproducibly, for anyone unlucky enough to tap Connect just after a tick. 2500ms
// clears the worst healthy case with margin while staying inside what a captive portal can spend.
//
// THE TIMING PROTECTION IS UNCHANGED IN FORM. This is still ONE budget for the whole endpoint, and every
// guest-visible non-success still leaves at exactly this offset — a malformed body, an unknown device, a
// wrong room, an unreachable PMS, a throttled attempt, and now an enforcement that did not converge in time.
// Splitting it — a short budget for identity failures and a long one for connection failures — would have
// been cheaper for the guest and would have handed an attacker the distinction the budget exists to remove:
// "this room answers at 2.5s and every other room answers at 1.2s" enumerates the property just as neatly as
// a distinguishing message would.
// REVIEWED AGAINST THE CORRECTED ENFORCEMENT CYCLE, AND LEFT UNCHANGED.
//
// The first two real Room Logins both failed here — portald abandoned each at its deadline and reported
// scd_unavailable — which looks like a budget that is too short and was not. Nothing was converging at all:
// the enforcement producer and the network writer were both gated on an unrelated feature flag, so the wait
// could never have ended at any budget.
//
// With the plane running, the cycle was measured end to end against a real database and a real socket
// (TestIntegration_EnforcementCycle_ConvergesWellInsideTheGuestBudget): derive the plan, submit it over the
// authenticated socket, apply both kernel halves and promote the Session takes ~66ms. The worst ordinary
// connect is therefore about one producer interval (1s) + that pass + scd's 100ms polling granularity + two
// local hops — comfortably inside the 2300ms handed upstream, with room for a loaded appliance.
//
// So the arithmetic above still holds and the number stays. Raising it would spend real guest patience on a
// margin that is already there; lowering it would re-create the failure mode it was raised to fix.
const phase3FailureBudget = 2500 * time.Millisecond

// phase3EnforcementReserve is how much of the budget is held back from the upstream hops so that a request
// which spends the entire budget upstream can still write its uniform answer at the budget rather than after
// it. Without it the ceiling would be "the budget, plus however long the response takes", which is a
// measurable difference between an abandoned attempt and a fast local refusal.
const phase3EnforcementReserve = 200 * time.Millisecond

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
	// The UPSTREAM deadline is deliberately earlier than the guest-visible one. scd derives its own
	// enforcement wait from the deadline it is handed, so this is also what tells scd how long it may wait for
	// the kernel — and reserving the difference is what keeps the answer landing AT the budget.
	ctx, cancel := context.WithDeadline(r.Context(), deadline.Add(-phase3EnforcementReserve))
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
