package main

// STAGED, ACCOUNTABLE-BEFORE-AUTHORIZED SESSION PROVISIONING.
//
// The invariant this file exists to hold:
//
//	A guest is network-active only when durable authorization AND accountable traffic enforcement are both
//	confirmed in the kernel.
//
// Three things must agree, and none of them substitutes for another:
//
//	nft — PACKET AUTHORIZATION. The only thing that gives or denies internet access. It is always a BOUNDED
//	      LEASE (phase3_lease.go): a healthy pass renews it, silence expires it.
//	tc  — ACCOUNTABLE METERING. Classifies an authorized guest's packets so they are shaped and counted.
//	      Removing a tc filter does NOT deny access; the packets fall back to the bridge's DEFAULT class and
//	      keep flowing, now unmetered.
//	DB  — DURABLE RUNTIME STATE. A Session may only claim `active` once both kernel halves are proven.
//
// So provisioning is staged, and the order is the whole design:
//
//	allocate durable class generation
//	→ prepare tc classes WITHOUT classification filters   (nothing is metered, and nft still denies access)
//	→ read the prepared class's absolute counters
//	→ register the accounting origin through the controlled PostgreSQL operation
//	→ activate BOTH tc classification filters             (now metered — still not authorized)
//	→ verify the class is actually classifying
//	→ AUTHORIZE the guest, on a SHORT PROVISIONAL LEASE   (now, and only now, the guest reaches the internet)
//	→ verify the authorization took
//	→ persist managed inventory
//	→ prove the Session durably active through the controlled writer
//	→ only then extend the lease to its full length
//
// The provisional lease closes the last window in the sequence. Between "the kernel is enforcing" and "durable
// state says so" there is a real interval, and a crash or an unreadable database can make it permanent — a
// guest online forever whose Session says PENDING_ENFORCEMENT. Authorizing provisionally means an activation
// that is never confirmed expires by itself, in the kernel, with nothing running.
//
// Every failure fails closed, and fail-closed means the NFT GATE FIRST: packet authorization is removed and
// proven gone before tc is touched, because until that element is gone the guest is online whatever tc says.
// Nothing is left authorized, no epoch is exposed, no Session claims active, `Shaped` does not advance, the
// plan is admitted (anti-replay) but NOT converged (health degrades), and a retry reuses the pending
// generation so the accounting origin is not re-baselined.

import (
	"context"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// provisionSession puts ONE desired session's class in force. Caller holds p.mu.
func (p *phase3Shaping) provisionSession(ctx context.Context, bridge string, minor int, s shapeplan.Session, view gateView, now time.Time, res *shapingPlanResponse) {
	ip := net.ParseIP(s.IP)
	key := classKey(bridge, s.SessionID)
	if p.epochs == nil {
		p.epochs = map[string]int64{}
	}
	if p.pending == nil {
		p.pending = map[string]int64{}
	}

	// ---- THE HARD ACCESS BOUNDARY, before anything is installed ------------------------------------------
	// A session whose entitlement window has already elapsed gets no lease at all, and no class. The plan may
	// still list it — the producer derives from durable state and the expiry sweep runs on its own schedule —
	// but "the plan still mentions them" is not authority to forward a guest past their own deadline.
	lease, leasable, why := leaseFor(s, now)
	if !leasable {
		p.failClosed(ctx, bridge, ip, res, "shape "+s.SessionID+": "+why+"; not authorized")
		return
	}

	// ---- SECURITY TIME: the bound is measured against a clock nobody can move ---------------------------
	//
	// Everything below compares BOOT-RELATIVE MONOTONIC milliseconds, never the wall clock. A wall clock that
	// moves backwards — an NTP correction, a wrong RTC after a power cut, a resumed snapshot — makes
	// `now - began` smaller or negative, and a 30-second grace becomes as long as the jump. If the clock
	// cannot be read at all, the bound cannot be measured, and a bound that cannot be measured must not be
	// assumed unspent.
	nowBoot, clockErr := p.bootNow()
	if clockErr != nil {
		p.failClosed(ctx, bridge, ip, res,
			"shape "+s.SessionID+": the monotonic security clock is unreadable ("+clockErr.Error()+
				"); the activation bound cannot be measured, so no provisional access is granted")
		return
	}
	// AND THE BOOT IDENTITY. The monotonic reading alone is not enough: it only means anything relative to a
	// known boot. Without a trustworthy identity a reboot cannot be detected, and a previous boot's deadline
	// would be compared against this boot's uptime — the bound silently gone, with nothing in the logs.
	//
	// An untrustworthy identity is NOT failed closed here, for one specific reason: it would disconnect
	// sessions that are already proven durably active and need no bound at all. It is instead carried as an
	// empty identity, which every consumer treats as untrusted:
	//
	//	beginAttempt REFUSES, so no new provisional access is granted;
	//	crossBoot() is TRUE for any record, so an outstanding attempt is held until durable ACTIVE proves it;
	//	proveActive fails closed, so an unproven attempt is not renewed.
	//
	// The result is exactly the required behaviour — no provisional access while reboot detection is broken,
	// and no manufactured outage for a guest whose access is already durably justified.
	curBootID, bootErr := p.currentBootID()
	if bootErr != nil {
		curBootID = ""
		res.Problems = append(res.Problems, "shape "+s.SessionID+
			": the boot identity is not trustworthy ("+bootErr.Error()+
			"); no provisional access will be granted or renewed until it is")
	}

	// ---- QUARANTINE: a session whose activation could not be proven --------------------------------------
	//
	// Failing closed once is not enough on its own. The session is still in every plan, so the very next pass
	// would prepare a fresh class and admit the guest provisionally all over again — and the pass after that,
	// and the one after that. The outcome would be a guest online most of the time, disconnected for a moment
	// every half minute, with durable state never once saying they are active. Bounded in the kernel, yes;
	// fail-closed, no.
	//
	// So an attempt that exhausts the activation grace also DENIES RE-ADMISSION for a backoff that doubles
	// with each strike, and even when that backoff elapses a fresh attempt is only made if durable state can
	// be read at all. The two conditions cover the two different failures: a database that is simply down
	// yields no access whatsoever after the first grace, and an activation that can never succeed yields a
	// rapidly shrinking fraction of one.
	//
	// A CROSS-BOOT record is handled separately and more strictly. Its readings belong to a monotonic timeline
	// that no longer exists, so they are not old — they are incomparable. The only honest way to bridge them
	// would be to ask the RTC how long the unit was down, which is the very clock this design refuses to
	// trust. So a boot change never yields a fresh grace: the session stays denied until AUTHORITATIVE durable
	// state resolves it, and the one thing that resolves it is proof that the Session is already active.
	if q := p.quarantined[key]; q != nil {
		if q.crossBoot(curBootID) {
			resolved := false
			if p.enforcement != nil {
				if active, cerr := p.enforcement.Confirm(ctx, s.SessionID); cerr == nil && active {
					// Durable state proves the guest really is active. The attempt is over; enforcement can be
					// rebuilt normally, and the stale record is cleared.
					resolved = true
					p.clearAttempt(key)
				}
			}
			if !resolved {
				if derr := p.denyAccess(ctx, bridge, ip); derr != nil {
					res.Problems = append(res.Problems,
						"cross-boot hold "+s.SessionID+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+derr.Error())
				}
				res.Failed++
				res.Problems = append(res.Problems, "shape "+s.SessionID+
					": an unproven activation survived a reboot; its monotonic bound cannot be carried across "+
					"boots and durable ACTIVE is not proven, so the session stays denied")
				return
			}
		}
	}
	if q := p.quarantined[key]; q != nil && q.BackoffUntilBootMs != 0 {
		hold := nowBoot < q.BackoffUntilBootMs
		if !hold && p.enforcement != nil {
			if _, err := p.enforcement.Confirm(ctx, s.SessionID); err != nil {
				q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
				if perr := p.persistAttempts(); perr != nil {
					res.Problems = append(res.Problems,
						"quarantine "+s.SessionID+": the extended backoff could not be persisted: "+perr.Error())
				}
				hold = true
			}
		}
		if hold {
			// Nothing is prepared and nothing is authorized. denyAccess is still called because the quarantine
			// may have been entered while an element was installed, and a fail-closed hold that leaves the
			// element behind is not a hold at all.
			if derr := p.denyAccess(ctx, bridge, ip); derr != nil {
				res.Problems = append(res.Problems,
					"quarantine "+s.SessionID+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+derr.Error())
			}
			res.Failed++
			res.Problems = append(res.Problems, "shape "+s.SessionID+
				": quarantined after "+itoa(q.Strikes)+" unproven activation(s); not re-admitting for another "+
				itoa(int((q.BackoffUntilBootMs-nowBoot)/1000))+"s")
			return
		}
		q.BackoffUntilBootMs = 0 // the backoff has elapsed and durable state is readable: one fresh attempt
	}

	// ---- ORDINARY RE-RATE of an already-active class ----------------------------------------------------
	// Same session, same kernel class, same durable generation. It must not allocate a new generation, must
	// not re-register the origin, and MUST NOT reset the counters — so it is a `tc class change` in place, not
	// a delete+recreate. This is the path an entitled session takes when only its rate moved, and it is also
	// THE RENEWAL PATH: a healthy pass over an unchanged session is what refreshes its kernel lease.
	if epoch, active := p.epochs[key]; active {
		if _, held := p.classes[key]; held {
			present, err := p.classPresent(ctx, bridge, minor)
			if err == nil && present {
				if rerr := p.shp.ReRateSession(ctx, bridge, ip, s.DownKbps, s.UpKbps); rerr != nil {
					// A failed re-rate leaves the class forwarding at its last AUTHORISED rate — not an
					// unauthorised one — which is the least-harm outcome, but the plan has not converged.
					res.Failed++
					res.Problems = append(res.Problems, "re-rate "+s.SessionID+": "+rerr.Error())
					return
				}
				p.minorOwner[minorKey(bridge, minor)] = s.SessionID
				// Converge durable state. A Session whose activation acknowledgement was lost is enforced in
				// the kernel but still says PENDING_ENFORCEMENT, and this is the pass that must finish the job
				// — the promotion is idempotent, so a session already active is untouched.
				outcome, problem := p.proveActive(ctx, key, s.SessionID, bridge, minor, epoch, nowBoot)
				if problem != "" {
					res.Failed++
					res.Problems = append(res.Problems, problem)
				}
				if outcome == activationFailed {
					// The grace is spent: an unprovable activation may not keep being renewed into a permanent
					// grant. Deny first, then tear the class down. proveActive has already recorded the strike
					// and the backoff, and endSeries deliberately leaves that record alone.
					p.failClosed(ctx, bridge, ip, res, "session "+s.SessionID+": activation never proven")
					p.endSeries(bridge, s.SessionID)
					return
				}
				// Re-assert the PACKET AUTHORIZATION at the lease length this outcome justifies. This path also
				// runs on the retry after a lost admission, on the first pass after a restart, and whenever a
				// stray sweep removed an element it should not have.
				want := lease
				if outcome != activationProven {
					want = provisionalOrLess(lease)
				}
				if !p.assertLease(ctx, bridge, ip, want, view, res, s.SessionID) {
					return
				}
				if outcome == activationProven {
					res.Shaped++
				}
				return
			}
			// The kernel lost the class since restore (a mid-boot flush). That is a genuinely new class: end
			// the old series so its successor allocates a fresh generation rather than inheriting a checkpoint
			// that now describes nothing.
			p.endSeries(bridge, s.SessionID)
		}
	}

	// ---- NEW class: (a) durable generation FIRST --------------------------------------------------------
	// A pending generation from an earlier failed attempt is REUSED, so a retry does not waste a generation or
	// re-baseline the origin (register_class_origin returns ORIGIN_UNCHANGED for the same epoch).
	epoch, havePending := p.pending[key]
	if !havePending {
		alloc, err := p.allocEpoch(ctx)
		if err != nil {
			// No generation means no accountable class. Nothing has been installed yet, so there is nothing to
			// tear down — the guest simply stays on the default (unshaped, unmanaged) path.
			res.Failed++
			res.Problems = append(res.Problems,
				"shape "+s.SessionID+": no class generation could be allocated; not made accountable")
			return
		}
		epoch = alloc
		p.pending[key] = epoch
	}

	// If a DIFFERENT session held this minor, end its series BEFORE the new class is prepared, so the old
	// occupant's class cannot be left forwarding under someone else's identity.
	if prev, held := p.minorOwner[minorKey(bridge, minor)]; held && prev != s.SessionID {
		p.endSeries(bridge, prev)
	}

	// ---- (b) PREPARE both classes WITHOUT forwarding filters -------------------------------------------
	if err := p.shp.PrepareSession(ctx, bridge, ip, s.DownKbps, s.UpKbps); err != nil {
		p.failClosed(ctx, bridge, ip, res, "prepare "+s.SessionID+": "+err.Error())
		return
	}

	// ---- (c)+(d) READ the prepared counters and REGISTER the origin, still with NO filter installed -----
	// registerOrigin reads the class counters (which are the prepared baseline, since nothing is classified
	// into the class yet) and records them through the controlled operation. Recording the origin BEFORE any
	// forwarding filter exists is the whole point: the first guest packet is counted from a real baseline.
	// It is also what makes the Session's later promotion possible at all: activate_session_enforcement
	// verifies this exact checkpoint before it will move a Session to active.
	if problem := p.registerOrigin(ctx, s, minor, epoch); problem != "" {
		p.failClosed(ctx, bridge, ip, res, problem)
		return
	}

	// ---- (e) ACTIVATE both tc classification filters ---------------------------------------------------
	// This makes the class METER the guest's packets. It does not admit them to the internet: that is the
	// nft gate in step (g), which is still closed.
	if err := p.shp.ActivateSession(ctx, bridge, ip); err != nil {
		p.failClosed(ctx, bridge, ip, res, "activate "+s.SessionID+": "+err.Error())
		return
	}

	// ---- (f) VERIFY the class is actually classifying in both directions -------------------------------
	forwarding, ferr := p.shp.SessionForwarding(ctx, bridge, ip)
	present, perr := p.classPresent(ctx, bridge, minor)
	if ferr != nil || perr != nil || !forwarding || !present {
		p.failClosed(ctx, bridge, ip, res, "post-activation verification failed for "+s.SessionID)
		return
	}

	// ---- (g) AUTHORIZE the guest, PROVISIONALLY, and verify it took ------------------------------------
	//
	// THE ORDER IS THE WHOLE POINT. Accountable metering is proven first, and only then is the guest given
	// the internet. Reversing these two steps — or admitting before (f) — opens exactly the window this
	// design exists to close: packets flowing through the bridge's DEFAULT class, forwarded but attributed
	// to nobody, with no counter series to bill them against and nothing anywhere reporting a problem.
	//
	// The lease is the SHORT one until durable state is proven in step (i). If this process dies between here
	// and there, the kernel drops the authorization on its own within that bound.
	if p.gate != nil {
		// WRITE-AHEAD. The attempt and its deadline are fsynced — file AND directory — before the element goes
		// in. Recording it afterwards, with the rest of the inventory at the end of the pass, leaves a window
		// in which a crash loses the bound entirely: the next process finds a valid older file, or none at
		// all, which looks exactly like a clean first run, and awards a brand-new grace. Repeat the crash and
		// the guest holds provisional access forever with every individual bound still correct.
		//
		// If the record cannot be proven durable, the guest is not authorized. There is nothing to recover
		// from that, because nothing was granted.
		if problem := p.beginAttempt(ctx, key, s.SessionID, nowBoot); problem != "" {
			p.failClosed(ctx, bridge, ip, res, "authorize "+s.SessionID+": "+problem)
			return
		}
		if err := p.gate.Authorize(ctx, bridge, ip, provisionalOrLess(lease)); err != nil {
			p.failClosed(ctx, bridge, ip, res, "authorize "+s.SessionID+": "+err.Error())
			return
		}
		authorized, aerr := p.gate.Authorized(ctx, bridge, ip)
		if aerr != nil || !authorized {
			// The gate did not take (or cannot be read). Fail closed: the guest must not be left in a state
			// where nobody can tell whether they are online.
			p.failClosed(ctx, bridge, ip, res, "authorization verification failed for "+s.SessionID)
			return
		}
	}

	// ---- (h) PERSIST managed state — the class is in force only now ------------------------------------
	p.minorOwner[minorKey(bridge, minor)] = s.SessionID
	p.epochs[key] = epoch
	if p.classes == nil {
		p.classes = map[string]managedClass{}
	}
	p.classes[key] = managedClass{
		SessionID: s.SessionID, DeviceID: s.DeviceID, Bridge: bridge, Minor: minor,
		Epoch: epoch, BootID: p.bootID}
	delete(p.pending, key) // the generation is now durable; it is no longer merely pending

	// ---- (i) PROVE durable ACTIVE, then extend the lease -----------------------------------------------
	// Only now is "this guest is authorized and metered" a true statement, so only now may the Session say so
	// — and only once the Session DOES say so may the guest hold a full-length lease.
	outcome, problem := p.proveActive(ctx, key, s.SessionID, bridge, minor, epoch, nowBoot)
	if problem != "" {
		res.Failed++
		res.Problems = append(res.Problems, problem)
	}
	switch outcome {
	case activationProven:
		if !p.assertLease(ctx, bridge, ip, lease, view, res, s.SessionID) {
			return
		}
		res.Shaped++
	case activationUnproven:
		// The guest keeps the provisional lease already installed. Nothing further is asserted: the next pass
		// retries the promotion, and if none ever succeeds the kernel expires the authorization by itself.
	default:
		p.failClosed(ctx, bridge, ip, res, "session "+s.SessionID+": activation never proven")
		p.endSeries(bridge, s.SessionID)
	}
}

// provisionalOrLess is the lease for a guest whose durable ACTIVE is not proven: the short provisional one, or
// the remaining time to their hard boundary if that is shorter still. A provisional lease must never be an
// excuse to overshoot a deadline the entitlement already states.
func provisionalOrLess(lease time.Duration) time.Duration {
	if lease < phase3ProvisionalLease {
		return lease
	}
	return phase3ProvisionalLease
}

// assertLease installs or refreshes the kernel lease for a session that should be online, using this pass's
// snapshot to decide whether a refresh is due. It returns false when the guest could not be left correctly
// authorized, having already failed closed.
//
// The snapshot is what keeps the steady state cheap: an unchanged, healthy guest costs a map lookup on most
// passes and one nft transaction roughly every thirty seconds, rather than a transaction every tick.
func (p *phase3Shaping) assertLease(ctx context.Context, bridge string, ip net.IP, want time.Duration, view gateView, res *shapingPlanResponse, sessionID string) bool {
	if p.gate == nil {
		return true
	}
	el, present, known := view.lookup(bridge, ip)
	if !known {
		// The set could not be enumerated this pass. Refresh unconditionally: an unreadable set must never be
		// read as "the lease is fine", which is how a whole property would quietly expire together.
		if err := p.gate.Authorize(ctx, bridge, ip, want); err != nil {
			p.failClosed(ctx, bridge, ip, res, "renew "+sessionID+": "+err.Error())
			return false
		}
		return true
	}
	if present && !needsRenewal(el.Expires, want) {
		return true
	}
	if err := p.gate.Authorize(ctx, bridge, ip, want); err != nil {
		p.failClosed(ctx, bridge, ip, res, "authorize "+sessionID+": "+err.Error())
		return false
	}
	if !present {
		// The element was MISSING while the class was in force — a stray sweep, a manual flush, an expired
		// lease that nothing renewed in time. Re-adding it is a genuine admission, so verify it as one.
		ok, verr := p.gate.Authorized(ctx, bridge, ip)
		if verr != nil || !ok {
			p.failClosed(ctx, bridge, ip, res, "re-authorization verification failed for "+sessionID)
			return false
		}
	}
	return true
}

// failClosed is the ONE fail-closed path, and its order is deliberate.
//
//  1. DENY PACKET AUTHORIZATION FIRST, and prove it. Until the nft element is gone the guest is on the
//     internet, whatever tc says. Cleaning up tc first would leave a window in which the guest is authorized
//     but no longer metered — forwarding through the bridge's default class, unaccounted.
//  2. Then abort tc. If the classes cannot be proven removed, strip their classification filters so the
//     leftover cannot silently meter (or mis-meter) anything.
//
// It never records the class as active, never advances Shaped, and deliberately keeps the pending generation
// so a retry reuses it rather than re-baselining the accounting origin.
func (p *phase3Shaping) failClosed(ctx context.Context, bridge string, ip net.IP, res *shapingPlanResponse, problem string) {
	// 1. packet authorization
	if err := p.denyAccess(ctx, bridge, ip); err != nil {
		// The guest may still be authorized. This is the most serious outcome available here, and it is
		// reported as such rather than folded into a tc cleanup message.
		problem += " (PACKET AUTHORIZATION NOT PROVEN REMOVED: " + err.Error() + ")"
	}
	// 2. tc
	if err := p.shp.AbortSession(ctx, bridge, ip); err != nil {
		if clsErr := p.shp.RemoveClassification(ctx, bridge, ip); clsErr != nil {
			problem += " (tc cleanup failed AND classification removal failed: " + err.Error() + "; " + clsErr.Error() + ")"
		} else {
			problem += " (tc cleanup unproven; classification removed so the leftover class meters nothing: " + err.Error() + ")"
		}
	}
	res.Failed++
	res.Problems = append(res.Problems, problem)
}

// classPresent reports whether a managed minor's class is installed on BOTH the bridge (download) and its ifb
// (upload). A class present on only one side is not a usable managed class.
func (p *phase3Shaping) classPresent(ctx context.Context, bridge string, minor int) (bool, error) {
	down, err := p.shp.ReadClasses(ctx, bridge)
	if err != nil {
		return false, err
	}
	up, err := p.shp.ReadClasses(ctx, shape.IFBName(bridge))
	if err != nil {
		return false, err
	}
	_, d := down[minor]
	_, u := up[minor]
	return d && u, nil
}
