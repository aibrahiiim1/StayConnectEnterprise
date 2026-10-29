package main

// Sustained churn: new sessions arriving and short sessions ending every few seconds while a thousand long ones keep forwarding.
//
// What the lab showed (docs/reviews/NETD_ENFORCEMENT_SCALING.md, section 7): once the kernel work was batched, a pass was still only as fast as its slowest
// database call, because it made them one after another and held the pass lock while it waited:
//
//   - a re-prove of every active session asked the promotion function, which takes the session's row lock and commits a write even for a session that is
//     already active;
//   - every ended session of the last hour (hundreds of them, because a churned client keeps its address and so "hands it over" to its successor) was
//     recorded as ended again on every pass, each a locking write;
//   - one slow call (a row lock held elsewhere, a class-generation allocation that took 106 s) stalled everything behind it, including the renewal of
//     the long sessions' leases, which therefore lapsed.
//
// The rules that follow: kernel work and lease renewal never wait for bookkeeping; bookkeeping that is independent per session runs a few at a time
// with a bound on each call; a database answer that can be had by a read is not asked for by a write.

import (
	"context"
	"fmt"

	"sync"
	"sync/atomic"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

const (
	// phase3DBConcurrency is how many independent per-session database calls a pass runs at once.
	phase3DBConcurrency = 6
	// phase3EndBookkeepingReserve is the part of the budget the durable end records leave alone; they are the last thing a pass does.
	phase3EndBookkeepingReserve = 3 * time.Second
	// phase3EndFailureLimit stops a pass's end records after this many failures: a database that is not answering is not asked once per ended session.
	phase3EndFailureLimit = 3
)

// phase3DBCallTimeout bounds one database call of the pass. A call that waits longer is a failure of that session, retried next pass, not a stall of
// every session behind it. A variable only so a test can shorten it.
var phase3DBCallTimeout = 3 * time.Second

// fanout runs fn(0..n-1) with at most width calls at once and returns when all are done. fn must not touch netd state: it is for database calls.
func fanout(n, width int, fn func(i int)) {
	if n == 0 {
		return
	}
	if width > n {
		width = n
	}
	if width <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < width; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}

// ---- promotion results fetched ahead ----

type activationResult struct {
	outcome string
	err     error
}

// takeActivation hands out (once) a promotion result fetched ahead for this class. Safe on a nil receiver.
func (b *passBatch) takeActivation(key string) (activationResult, bool) {
	if b == nil {
		return activationResult{}, false
	}
	r, ok := b.activations[key]
	if ok {
		delete(b.activations, key)
	}
	return r, ok
}

type activationAsk struct {
	key, sessionID, bridge string
	minor                  int
	epoch                  int64
}

// prefetchActivations asks the database for many promotions at once. The results are consumed, in order, by proveActive's state machine.
func (p *phase3Shaping) prefetchActivations(ctx context.Context, asks []activationAsk) {
	if p.pb == nil || p.enforcement == nil || len(asks) == 0 {
		return
	}
	out := make([]activationResult, len(asks))
	fanout(len(asks), phase3DBConcurrency, func(i int) {
		a := asks[i]
		out[i].outcome, out[i].err = p.enforcement.Activate(ctx, a.sessionID, a.bridge, a.minor, a.epoch)
	})
	for i, a := range asks {
		p.pb.activations[a.key] = out[i]
	}
}

// dropEnded forgets a class whose session durable state has ended: no pending generation to retry, no proof, no quarantine, no series.
func (p *phase3Shaping) dropEnded(bridge, sessionID string) {
	key := classKey(bridge, sessionID)
	delete(p.pending, key)
	delete(p.provenAt, key)
	p.clearAttempt(key)
	p.endSeries(bridge, sessionID)
}

// ---- durable end records ----

type pendingEnd struct {
	key       string // class key to mark as done in endedTorn when the record is written; "" for none
	sessionID string
	reason    string
}

// queueEnd defers a durable end record to the end of the pass (batched pass) or writes it now. It reports whether it is already written.
func (p *phase3Shaping) queueEnd(ctx context.Context, key, sessionID, reason string) bool {
	if p.pb == nil {
		return p.markEnded(ctx, sessionID, reason)
	}
	p.pb.ends = append(p.pb.ends, pendingEnd{key: key, sessionID: sessionID, reason: reason})
	return false
}

// finishEnds writes the durable end records the pass deferred, a few at a time. It is the last thing a pass does, after every denial, admission and
// renewal: the access of these sessions is already withdrawn, so this is bookkeeping, and it must not delay anything that is not.
func (p *phase3Shaping) finishEnds(ctx context.Context, res *shapingPlanResponse) {
	if p.pb == nil || len(p.pb.ends) == 0 {
		return
	}
	items := p.pb.ends
	p.pb.ends = nil
	if dl, ok := ctx.Deadline(); ctx.Err() != nil || p.pastSoftBudget() || (ok && time.Until(dl) < phase3EndBookkeepingReserve) {
		// Left for the next pass: the plan lists these sessions until their window closes, and an unrecorded end is simply recorded then.
		p.count("ends_deferred", len(items))
		return
	}
	done := make([]bool, len(items))
	var failures atomic.Int32
	fanout(len(items), phase3DBConcurrency, func(i int) {
		if failures.Load() >= phase3EndFailureLimit {
			return // the database is not answering; the remaining records wait for the next pass
		}
		cctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
		defer cancel()
		done[i] = p.markEnded(cctx, items[i].sessionID, items[i].reason)
		if !done[i] {
			failures.Add(1)
		}
	})
	skipped := 0
	for i, it := range items {
		if done[i] {
			if it.key != "" {
				p.endedTorn[it.key] = true
			}
		} else if failures.Load() >= phase3EndFailureLimit {
			skipped++
		}
	}
	if f := int(failures.Load()); f > 0 {
		p.count("ends_failed", f)
	}
	if skipped > 0 {
		p.count("ends_deferred", skipped)
	}
	_ = res
}

// ---- routine work ----

// steadyLease keeps an unchanged, already-proven session forwarding: its lease is asserted (written in the pass's one renewal transaction when it is
// due) and it is remembered as renewable for the between-walks timer. Nothing here waits for the database or runs a process.
func (p *phase3Shaping) steadyLease(ctx context.Context, bridge string, c batchCand, nowBoot int64, view gateView, now time.Time, res *shapingPlanResponse) {
	pc := c.pc
	p.minorOwner[minorKey(bridge, c.minor)] = c.s.SessionID
	want := pc.lease
	if !p.proofFresh(pc.key, p.epochs[pc.key], nowBoot) {
		// Not proven on this boot yet (a restart), or not re-proven for longer than the proof is trusted (the database has not answered): the session keeps
		// its access on the short lease only, as one whose activation is unproven does, until a read confirms it.
		want = provisionalOrLess(want)
	}
	if !p.assertLease(ctx, bridge, pc.ip, want, view, res, c.s.SessionID) {
		return
	}
	res.Shaped++
	p.noteRenewable(pc.key, bridge, pc.ip, c.s, pc.ev.Until, now)
}

// phase3MidPassRenewAfter: a pass that has run longer than this renews the leases of the unchanged sessions a second time before it takes on more, judging
// them by the time they have left NOW. The renewal at the start of a pass leaves alone a lease with more than phase3LeaseRenewBelow left, which is
// safe only while a pass is shorter than phase3LeaseTTL - phase3LeaseRenewBelow; this keeps it safe when the database makes a pass longer.
const phase3MidPassRenewAfter = 8 * time.Second

// renewAgain is steadyLease's second look: it only asserts the lease, against the snapshot aged by the time the pass has taken, and skips the
// sessions this pass already renewed.
func (p *phase3Shaping) renewAgain(ctx context.Context, bridge string, steady []batchCand, view gateView, res *shapingPlanResponse) {
	if len(steady) == 0 || p.passStart.IsZero() || time.Since(p.passStart) < phase3MidPassRenewAfter {
		return
	}
	aged := view
	aged.aged = time.Since(p.passStart)
	nowBoot, _ := p.bootNow()
	n := 0
	for _, c := range steady {
		if p.pb.renewed[gateKey(bridge, c.pc.ip)] {
			continue
		}
		want := c.pc.lease
		if !p.proofFresh(c.pc.key, p.epochs[c.pc.key], nowBoot) {
			want = provisionalOrLess(want)
		}
		// the lease the pass computed at its start has run down by the time the pass has taken
		if want -= aged.aged; want < time.Second {
			continue // the access boundary is closer than the pass is long: nothing to extend
		}
		before := len(p.pb.renew)
		p.assertLease(ctx, bridge, c.pc.ip, want.Truncate(time.Second), aged, res, c.s.SessionID)
		if len(p.pb.renew) > before {
			n++
		}
	}
	p.count("renewed_again", n)
	p.flushRenewals(ctx, res)
	p.flushDenies(ctx, res)
}

// reproveSteady asks durable state, with plain reads a few at a time, whether the sessions whose last proof has aged are still active. A session that
// is confirmed is proven again; one that is not (ended, unreadable) goes through the full per-session path, which withdraws or holds its access by the
// activation rules. The leases of these sessions were already written before this ran.
func (p *phase3Shaping) reproveSteady(ctx context.Context, bridge string, steady []batchCand, view gateView, now time.Time, res *shapingPlanResponse) {
	if p.enforcement == nil {
		return
	}
	if p.pastSoftBudget() {
		p.count("reprove_deferred", len(steady))
		return
	}
	nowBoot, err := p.bootNow()
	if err != nil {
		return // the full path below reports an unreadable clock per session
	}
	var due []batchCand
	for _, c := range steady {
		epoch := p.epochs[c.pc.key]
		if !p.recentlyProven(c.pc.key, epoch, nowBoot) {
			due = append(due, c)
		}
	}
	if len(due) == 0 {
		return
	}
	p.count("reprove", len(due))
	confirmed := make([]bool, len(due))
	fanout(len(due), phase3DBConcurrency, func(i int) {
		cctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
		defer cancel()
		active, cerr := p.enforcement.Confirm(cctx, due[i].s.SessionID)
		confirmed[i] = cerr == nil && active
	})
	for i, c := range due {
		if confirmed[i] {
			p.noteProven(c.pc.key, p.epochs[c.pc.key], nowBoot)
			continue
		}
		// Not confirmed: the per-session path decides (it asks the promotion call and applies the grace and quarantine rules).
		p.provisionTail(ctx, bridge, c.minor, c.s, view, now, res, c.pc)
	}
}

// runIndividual runs the per-session path for the sessions that cannot be batched. Those that already have a class and are only waiting for their
// durable ACTIVE are promoted by a few concurrent calls first (their answers are consumed in order by proveActive), so one that waits on a lock held
// elsewhere costs its own timeout, not the sum of them.
func (p *phase3Shaping) runIndividual(ctx context.Context, bridge string, indiv []batchCand, view gateView, now time.Time, res *shapingPlanResponse) {
	if len(indiv) == 0 {
		return
	}
	var asks []activationAsk
	for _, c := range indiv {
		epoch, active := p.epochs[c.pc.key]
		if _, held := p.classes[c.pc.key]; active && held && p.enforcement != nil {
			asks = append(asks, activationAsk{key: c.pc.key, sessionID: c.s.SessionID, bridge: bridge, minor: c.minor, epoch: epoch})
		}
	}
	p.prefetchActivations(ctx, asks)
	// Resume where the previous pass stopped, so a few sessions that always wait on the database cannot take every pass's time and starve the rest.
	start := 0
	if p.indivCursor != nil {
		start = p.indivCursor[bridge] % len(indiv)
	}
	done := 0
	for n := 0; n < len(indiv); n++ {
		c := indiv[(start+n)%len(indiv)]
		if ctx.Err() != nil {
			res.Failed++
			res.Problems = append(res.Problems, fmt.Sprintf("pass budget exhausted on %s: %d sessions were not handled and are retried next pass", bridge, len(indiv)-n))
			break
		}
		if done > 0 && p.pastSoftBudget() {
			p.count("individual_deferred", len(indiv)-n)
			break
		}
		p.provisionTail(ctx, bridge, c.minor, c.s, view, now, res, c.pc)
		done++
	}
	if p.indivCursor == nil {
		p.indivCursor = map[string]int{}
	}
	p.indivCursor[bridge] = start + done
}

// phase3SoftBudget is how much work a pass takes on after it has done the urgent part (denials, lease renewal, the first batch of admissions): past
// it, what is left waits for the next pass. A variable only so a test can shorten it.
var phase3SoftBudget = 15 * time.Second

func (p *phase3Shaping) pastSoftBudget() bool {
	return !p.softEnd.IsZero() && time.Now().After(p.softEnd)
}

// admissionAllocs allocates the class generations a chunk of new sessions needs, a few at a time. An allocation that fails leaves its session out of
// the chunk with the usual report; nothing is installed for it.
func (p *phase3Shaping) admissionAllocs(ctx context.Context, bridge string, chunk []batchCand, res *shapingPlanResponse) []liveCand {
	type slot struct {
		epoch int64
		err   error
		need  bool
	}
	slots := make([]slot, len(chunk))
	var need []int
	for i, c := range chunk {
		if e, have := p.pending[c.pc.key]; have {
			slots[i].epoch = e
			continue
		}
		slots[i].need = true
		need = append(need, i)
	}
	fanout(len(need), phase3DBConcurrency, func(j int) {
		i := need[j]
		cctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
		defer cancel()
		slots[i].epoch, slots[i].err = p.allocEpoch(cctx)
	})
	var lv []liveCand
	for i, c := range chunk {
		if slots[i].need {
			if slots[i].err != nil {
				res.Failed++
				res.Problems = append(res.Problems, "shape "+c.s.SessionID+": no class generation could be allocated; not made accountable")
				continue
			}
			p.pending[c.pc.key] = slots[i].epoch
		}
		// End a previous occupant's series before preparing the new class, so it cannot forward under another identity.
		if prev, held := p.minorOwner[minorKey(bridge, c.minor)]; held && prev != c.s.SessionID {
			p.endSeries(bridge, prev)
		}
		lv = append(lv, liveCand{c, slots[i].epoch})
	}
	return lv
}

// admissionOrigins registers the accounting origins of a chunk, a few at a time, from the counters read once for the chunk.
func (p *phase3Shaping) admissionOrigins(ctx context.Context, bridge string, lv []liveCand, up, down map[int]shape.ClassBytes, res *shapingPlanResponse) (reg []liveCand) {
	type verdict struct {
		problem string
		ended   bool
	}
	out := make([]verdict, len(lv))
	if p.origins != nil {
		fanout(len(lv), phase3DBConcurrency, func(i int) {
			l := lv[i]
			if l.s.DeviceID == "" {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
			defer cancel()
			outcome, err := p.callOrigin(cctx, l.s, l.minor, l.epoch, up, down)
			out[i].problem, out[i].ended = p.originVerdict(l.s, l.epoch, outcome, err)
		})
	}
	for i, l := range lv {
		switch {
		case p.origins != nil && l.s.DeviceID == "":
			p.failClosed(ctx, bridge, l.pc.ip, res, "class origin not registered for "+l.s.SessionID+": the plan carries no device identity")
		case out[i].ended:
			p.endClosed(ctx, bridge, l.pc.ip, res, "session "+l.s.SessionID+": durable state has ended it; access withdrawn")
			p.dropEnded(bridge, l.s.SessionID)
		case out[i].problem != "":
			p.failClosed(ctx, bridge, l.pc.ip, res, out[i].problem)
		default:
			reg = append(reg, l)
		}
	}
	return reg
}
