package main

// STAGED, ACCOUNTABLE-BEFORE-FORWARDING CLASS PROVISIONING.
//
// The invariant this file exists to hold:
//
//	No managed Phase-3 traffic may be forwarded before its counter series has a durable, authoritative
//	accounting origin.
//
// The old path installed the class AND its forwarding filter in one step, then allocated the generation and
// registered the origin afterwards. So a guest could push traffic through the class before it was accountable,
// and if the generation or origin failed the class was left forwarding unaccounted bytes. This stages it:
//
//	allocate durable generation
//	→ prepare download/upload classes WITHOUT guest forwarding filters   (no packet is classified into them)
//	→ read the prepared class's absolute counters
//	→ register the class origin through the controlled PostgreSQL operation
//	→ activate BOTH forwarding filters                                   (now, and only now, it forwards)
//	→ verify the class is actually forwarding
//	→ persist managed inventory
//	→ mark shaped
//
// Every failure fails closed: nothing is left forwarding, no state records the class as active, `Shaped` does
// not advance, the plan is admitted (so anti-replay still cannot reinstate revoked access) but NOT converged
// (health reports ACTIVE_DEGRADED), and a retry of the same plan can complete — reusing the pending generation
// so the origin is not re-baselined.

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
				_ = epoch // unchanged, by design
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

	// ---- (e) ACTIVATE both forwarding filters ----------------------------------------------------------
	if err := p.shp.ActivateSession(ctx, bridge, ip); err != nil {
		p.failClosed(ctx, bridge, ip, res, "activate "+s.SessionID+": "+err.Error())
		return
	}

	// ---- (f) VERIFY the class is actually forwarding in both directions --------------------------------
	forwarding, ferr := p.shp.SessionForwarding(ctx, bridge, ip)
	present, perr := p.classPresent(ctx, bridge, minor)
	if ferr != nil || perr != nil || !forwarding || !present {
		p.failClosed(ctx, bridge, ip, res, "post-activation verification failed for "+s.SessionID)
		return
	}

	// ---- (g) PERSIST managed state — the class is in force only now ------------------------------------
	p.minorOwner[minorKey(bridge, minor)] = s.SessionID
	p.epochs[key] = epoch
	if p.classes == nil {
		p.classes = map[string]managedClass{}
	}
	p.classes[key] = managedClass{
		SessionID: s.SessionID, DeviceID: s.DeviceID, Bridge: bridge, Minor: minor,
		Epoch: epoch, BootID: p.bootID}
	delete(p.pending, key) // the generation is now durable; it is no longer merely pending
	res.Shaped++
}

// failClosed removes anything a partial provisioning installed, so nothing is left forwarding, and records a
// truthful degraded problem. If removal cannot be PROVEN, it escalates to the forwarding-denial quarantine
// (strip the filters even if the class will not delete). It never records the class as active, never advances
// Shaped, and deliberately keeps the pending generation so a retry reuses it.
func (p *phase3Shaping) failClosed(ctx context.Context, bridge string, ip net.IP, res *shapingPlanResponse, problem string) {
	if err := p.shp.AbortSession(ctx, bridge, ip); err != nil {
		if denyErr := p.shp.DenyForwarding(ctx, bridge, ip); denyErr != nil {
			problem += " (cleanup failed AND forwarding denial failed: " + err.Error() + "; " + denyErr.Error() + ")"
		} else {
			problem += " (cleanup unproven; forwarding denied and class quarantined non-forwarding: " + err.Error() + ")"
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
