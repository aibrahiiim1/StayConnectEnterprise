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
//	nft — PACKET AUTHORIZATION. The only thing that gives or denies internet access.
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
//	→ AUTHORIZE the guest in the Phase-3 nft gate         (now, and only now, the guest reaches the internet)
//	→ verify the authorization took
//	→ persist managed inventory
//	→ promote the Session to active through the controlled writer
//
// Every failure fails closed, and fail-closed means the NFT GATE FIRST: packet authorization is removed and
// proven gone before tc is touched, because until that element is gone the guest is online whatever tc says.
// Nothing is left authorized, no epoch is exposed, no Session claims active, `Shaped` does not advance, the
// plan is admitted (anti-replay) but NOT converged (health degrades), and a retry reuses the pending
// generation so the accounting origin is not re-baselined.

import (
	"context"
	"net"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// provisionSession puts ONE desired session's class in force. Caller holds p.mu.
func (p *phase3Shaping) provisionSession(ctx context.Context, bridge string, minor int, s shapeplan.Session, res *shapingPlanResponse) {
	ip := net.ParseIP(s.IP)
	key := classKey(bridge, s.SessionID)
	if p.epochs == nil {
		p.epochs = map[string]int64{}
	}
	if p.pending == nil {
		p.pending = map[string]int64{}
	}

	// ---- ORDINARY RE-RATE of an already-active class ----------------------------------------------------
	// Same session, same kernel class, same durable generation. It must not allocate a new generation, must
	// not re-register the origin, and MUST NOT reset the counters — so it is a `tc class change` in place, not
	// a delete+recreate. This is the path an entitled session takes when only its rate moved.
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
				// The class is in force and metering. Re-assert the PACKET AUTHORIZATION too: this path also
				// runs on the retry after a lost admission, on the first pass after a restart, and whenever a
				// stray sweep removed an element it should not have. Authorize is idempotent, so re-asserting
				// costs nothing and closes the case where tc is correct but the gate is not.
				if p.gate != nil {
					authorized, aerr := p.gate.Authorized(ctx, bridge, ip)
					if aerr != nil {
						res.Failed++
						res.Problems = append(res.Problems, "re-rate "+s.SessionID+": authorization unreadable: "+aerr.Error())
						return
					}
					if !authorized {
						if err := p.gate.Authorize(ctx, bridge, ip, 0); err != nil {
							p.failClosed(ctx, bridge, ip, res, "re-authorize "+s.SessionID+": "+err.Error())
							return
						}
						if ok, verr := p.gate.Authorized(ctx, bridge, ip); verr != nil || !ok {
							p.failClosed(ctx, bridge, ip, res, "re-authorization verification failed for "+s.SessionID)
							return
						}
					}
				}
				// Converge durable state as well. A Session whose activation acknowledgement was lost is
				// enforced in the kernel but still says PENDING_ENFORCEMENT, and this is the pass that must
				// finish the job — the promotion is idempotent, so a session already active is untouched.
				if problem := p.markActive(ctx, s.SessionID, bridge, minor, epoch); problem != "" {
					res.Failed++
					res.Problems = append(res.Problems, problem)
				}
				res.Shaped++
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
	if problem := p.registerOrigin(ctx, s, minor, epoch); problem != "" {
		p.failClosed(ctx, bridge, ip, res, problem)
		return
	}

	// ---- (e) ACTIVATE both tc classification filters ---------------------------------------------------
	// This makes the class METER the guest's packets. It does not admit them to the internet: that is the
	// nft gate in step (h), which is still closed.
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

	// ---- (g) AUTHORIZE the guest in the Phase-3 nft gate, and verify it took ---------------------------
	//
	// THE ORDER IS THE WHOLE POINT. Accountable metering is proven first, and only then is the guest given
	// the internet. Reversing these two steps — or admitting before (f) — opens exactly the window this
	// design exists to close: packets flowing through the bridge's DEFAULT class, forwarded but attributed
	// to nobody, with no counter series to bill them against and nothing anywhere reporting a problem.
	if p.gate != nil {
		if err := p.gate.Authorize(ctx, bridge, ip, 0); err != nil {
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

	// ---- (i) RECORD the confirmed kernel result, so durable state can honestly say ACTIVE --------------
	// Only now is "this guest is authorized and metered" a true statement, so only now may the Session say so.
	if problem := p.markActive(ctx, s.SessionID, bridge, minor, epoch); problem != "" {
		// The kernel is correct and proven; only the status write failed. Report it (health degrades and the
		// next pass retries) but do NOT tear down working, accountable enforcement over a bookkeeping lag.
		res.Failed++
		res.Problems = append(res.Problems, problem)
	}
	res.Shaped++
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
