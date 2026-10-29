package main

// AD-53 (a): lease renewal is split from the plan walk.
//
// acctd submits the whole desired state every few seconds. Walking it every time (ownership lookup, class inventory, set
// scan, one nft read per session) is wasted work when nothing changed. So:
//
//   - A submission whose DesiredStateHash equals the last CLEAN full walk's, inside phase3FullWalkEvery, only refreshes the
//     validity of the accepted plan. Nothing is walked.
//   - Any change of hash (a session added, ended, re-rated, an access boundary moved) walks at once; a walk that reported any
//     problem is never skipped; and a full walk runs at least every phase3FullWalkEvery regardless, so kernel drift is found.
//   - The kernel leases of the sessions that walk proved are kept alive by netd's own timer (renewDue), never beyond the
//     plan's validity, the session's access boundary or the DHCP lease it was verified against. A dead producer therefore
//     still stops renewals when its last plan expires, exactly as before.
//
// OFF SWITCH: NETD_PLAN_FASTPATH=false walks every submission (the previous behaviour).

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

const (
	// phase3FullWalkEvery bounds how long drift (a class or element changed behind netd's back) can go unnoticed.
	phase3FullWalkEvery = 30 * time.Second
	// phase3RenewEvery is how often the lease timer looks; it renews only leases running down (needsRenewal).
	phase3RenewEvery = 5 * time.Second
)

// renewSession is what the last full walk proved about one active session: enough to keep its lease alive, nothing more.
type renewSession struct {
	bridge     string
	ip         net.IP
	sessionID  string
	accessEnds *time.Time
	// ownedUntil is the DHCP lease the address was verified against; zero when the walk had no lease end to bind to.
	ownedUntil time.Time
}

// walkMemo is the outcome of the last full walk.
type walkMemo struct {
	hash      string
	at        time.Time
	clean     bool
	res       shapingPlanResponse
	renewable map[string]renewSession
}

// noteRenewable records a session the current walk proved active. Caller holds p.mu.
func (p *phase3Shaping) noteRenewable(key, bridge string, ip net.IP, s shapeplan.Session, ownedUntil, walkedAt time.Time) {
	if p.walkRenewable == nil {
		return
	}
	// Ownership was verified at this walk and is not re-checked until the next one, so the timer may extend the lease no further
	// than one lease length past it (and never past the DHCP lease itself): a reassigned address is exposed for that long at most.
	if bound := walkedAt.Add(phase3LeaseTTL); ownedUntil.IsZero() || ownedUntil.After(bound) {
		ownedUntil = bound
	}
	p.walkRenewable[key] = renewSession{bridge: bridge, ip: ip, sessionID: s.SessionID, accessEnds: s.AccessEndsAt, ownedUntil: ownedUntil}
}

// fastPathApplies reports whether this submission only refreshes validity. Caller holds p.mu.
func (p *phase3Shaping) fastPathApplies(env shapeplan.Envelope, now time.Time) bool {
	return p.fastPath && p.hasConverged && p.memo.clean &&
		env.DesiredStateHash == p.memo.hash && env.DesiredStateHash == p.lastConverged.Hash &&
		now.Sub(p.memo.at) < phase3FullWalkEvery && now.Sub(p.memo.at) >= 0
}

// renewDue renews the kernel leases that are running down, for the sessions the last clean full walk proved, under the
// last accepted plan's validity. It reports how many leases it refreshed. Safe to call at any time.
func (p *phase3Shaping) renewDue(ctx context.Context, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.fastPath || p.gate == nil || !p.hasConverged || !p.memo.clean || p.memo.hash != p.lastConverged.Hash {
		return 0 // the full walk owns renewal whenever the fast path is not in force
	}
	planLeft := p.lastConverged.ExpiresAt.Sub(now)
	if planLeft < time.Second {
		return 0 // the producer went quiet: leases are left to lapse, as they always were
	}
	view := p.snapshotGate(ctx)
	renewed := 0
	var due []renewSession
	var reqs []gateLease
	for _, r := range p.memo.renewable {
		want, ok, _ := leaseFor(shapeplan.Session{AccessEndsAt: r.accessEnds}, now)
		if !ok {
			continue
		}
		if want > planLeft {
			want = planLeft
		}
		if !r.ownedUntil.IsZero() {
			if left := r.ownedUntil.Sub(now); left < want {
				want = left
			}
		}
		want = want.Truncate(time.Second)
		if want < time.Second {
			continue
		}
		el, present, known := view.lookup(r.bridge, r.ip)
		if known && present && !needsRenewal(el.Expires, want) {
			continue
		}
		due = append(due, r)
		reqs = append(reqs, gateLease{Bridge: r.bridge, IP: r.ip, TTL: want})
	}
	// One transaction for every due lease (a gate that cannot batch is asked one by one, as before).
	for i, err := range p.authorizeBatch(ctx, reqs) {
		if err != nil {
			// The next full walk (at most phase3FullWalkEvery away) re-derives and fails closed if need be.
			slog.Warn("netd: lease renewal between walks failed", "session", due[i].sessionID, "bridge", due[i].bridge, "err", err)
			continue
		}
		renewed++
	}
	return renewed
}

// renewLoop runs renewDue until ctx ends.
func (p *phase3Shaping) renewLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			p.renewDue(ctx, now)
		}
	}
}
