package main

// A guest is network-active only when durable authorization and accountable traffic enforcement are both
// confirmed in the kernel. The order is the design:
//
//	allocate durable class generation
//	→ prepare tc classes WITHOUT classification filters   (nothing metered, nft still denies)
//	→ read the prepared counters, register the accounting origin through the controlled operation
//	→ activate BOTH tc classification filters             (metered, still not authorized)
//	→ verify the class is classifying
//	→ AUTHORIZE on a short provisional lease              (the guest reaches the internet only here)
//	→ verify the authorization took, persist managed inventory
//	→ prove the Session durably active, then extend the lease to full length
//
// The provisional lease makes an activation that is never confirmed expire in the kernel by itself.
// Every failure fails closed with the nft gate first (see failClosed); the pending generation is kept so a
// retry does not re-baseline the origin.

import (
	"context"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// provCtx is what the checks that precede any kernel work established about one session.
type provCtx struct {
	ip      net.IP
	key     string
	lease   time.Duration
	ev      ownershipEvidence
	nowBoot int64
}

// Caller holds p.mu.
func (p *phase3Shaping) provisionSession(ctx context.Context, bridge string, minor int, s shapeplan.Session, view gateView, now time.Time, res *shapingPlanResponse) {
	pc, ok := p.provisionPrecheck(ctx, bridge, minor, s, view, now, res)
	if !ok {
		return
	}
	p.provisionTail(ctx, bridge, minor, s, view, now, res, pc)
}

// provisionPrecheck is everything that decides, from memory and the DHCP evidence alone, whether a session may be provisioned or renewed at all: the
// access boundary, address ownership, the monotonic clock and the quarantine. It installs nothing; the fail-closed outcomes it reaches deny access.
// ok is false when the session has been dealt with (failed closed, held, or left unrenewed).
func (p *phase3Shaping) provisionPrecheck(ctx context.Context, bridge string, minor int, s shapeplan.Session, view gateView, now time.Time, res *shapingPlanResponse) (provCtx, bool) {
	ip := net.ParseIP(s.IP)
	key := classKey(bridge, s.SessionID)
	if p.epochs == nil {
		p.epochs = map[string]int64{}
	}
	if p.pending == nil {
		p.pending = map[string]int64{}
	}

	// The plan may still list a session past its deadline; that is not authority to forward it.
	lease, leasable, why := leaseFor(s, now)
	if !leasable {
		// The plan still lists a session whose window has run out (or has under a second left): the ordinary end of access, which the next plan lists as ended.
		p.endClosed(ctx, bridge, ip, res, "shape "+s.SessionID+": "+why+"; not authorized")
		return provCtx{}, false
	}

	// Before anything is installed or renewed, DHCP is asked whose address this is. A reassigned or released
	// address fails closed and the Session is closed. UNKNOWN (Kea unreadable) is not a failure: tearing every
	// guest down over an unreadable socket would manufacture an outage.
	ev := p.owner.Evidence(ctx, ip, s.MAC)
	switch {
	case ev.State == addressOwned:
		p.rememberLease(key, s.IP, s.MAC, ev.Until, ev.Epoch, now)
	case ev.Missing:
		// Kea has no record of this address. A lease verified earlier for this same address and device is still valid on the client's side until it
		// lapses, so the address stays authorized for exactly that long: never longer than the lease and never past leaseMemoryCap.
		if left := p.bridgedLeaseLeft(key, s.IP, s.MAC, ev, now); left > 0 {
			if left < lease {
				lease = left
			}
			ev.State = addressOwned
		}
	}
	switch ev.State {
	case addressOwned:
		p.clearUnverified(bridge, s.SessionID)
	case addressForeign:
		p.failClosed(ctx, bridge, ip, res,
			"shape "+s.SessionID+": "+s.IP+" is no longer leased to this session's device; authorization withdrawn")
		p.markEnded(ctx, s.SessionID, endReasonAddressNotOwned)
		p.endSeries(bridge, s.SessionID)
		return provCtx{}, false
	case addressUnknown:
		// Missing evidence is not permission to continue: durable state alone must never renew an address
		// (that is how a reassigned address stayed authorized). Withhold the renewal and let the bounded
		// lease run out unless ownership is confirmed first; nothing else is destroyed.
		if s.MAC != "" {
			res.Problems = append(res.Problems,
				"shape "+s.SessionID+": address ownership could not be verified (DHCP evidence unavailable); "+
					"the authorization is NOT renewed and will expire with its current lease unless ownership "+
					"is confirmed first")
			p.noteUnverified(bridge, s.SessionID)
			return provCtx{}, false
		}
	}

	// The device address DHCP just vouched for (or, when Kea could not be asked, the one the producer recorded) is what the
	// MAC-bound authorization is keyed on (AD-23 a). Bound before any Authorize below.
	if mb, ok := p.gate.(macBinder); ok && s.MAC != "" {
		mb.BindMAC(bridge, ip, s.MAC)
	}

	// Everything below uses boot-relative monotonic milliseconds, never the wall clock (see
	// phase3_securitytime.go). An unreadable clock means the bound cannot be measured.
	nowBoot, clockErr := p.bootNow()
	if clockErr != nil {
		p.failClosed(ctx, bridge, ip, res,
			"shape "+s.SessionID+": the monotonic security clock is unreadable ("+clockErr.Error()+
				"); the activation bound cannot be measured, so no provisional access is granted")
		return provCtx{}, false
	}
	// An untrustworthy boot identity is not failed closed here: that would disconnect sessions already proven
	// active. It is carried as "", which every consumer treats as untrusted (beginAttempt refuses,
	// crossBoot() is true, proveActive fails closed), so no provisional access is granted meanwhile.
	curBootID, bootErr := p.currentBootID()
	if bootErr != nil {
		curBootID = ""
		res.Problems = append(res.Problems, "shape "+s.SessionID+
			": the boot identity is not trustworthy ("+bootErr.Error()+
			"); no provisional access will be granted or renewed until it is")
	}

	// Quarantine: the session is in every plan, so without it each pass would re-admit the guest
	// provisionally, leaving them online most of the time with durable state never active. An attempt that
	// exhausts the grace denies re-admission for a backoff that doubles per strike, and a fresh attempt is
	// made only if durable state is readable.
	//
	// A cross-boot record is stricter: its readings are incomparable, so it never yields a fresh grace and
	// stays denied until durable state proves the Session already active.
	if q := p.quarantined[key]; q != nil {
		if q.crossBoot(curBootID) {
			resolved := false
			if p.enforcement != nil {
				if active, cerr := p.enforcement.Confirm(ctx, s.SessionID); cerr == nil && active {
					resolved = true
					p.clearAttempt(key)
				}
			}
			if !resolved {
				p.holdDeny(ctx, bridge, ip, res, "cross-boot hold "+s.SessionID)
				res.Failed++
				res.Problems = append(res.Problems, "shape "+s.SessionID+
					": an unproven activation survived a reboot; its monotonic bound cannot be carried across "+
					"boots and durable ACTIVE is not proven, so the session stays denied")
				return provCtx{}, false
			}
		}
	}
	if q := p.quarantined[key]; q != nil && q.BackoffUntilBootMs != 0 && !q.freshAttempt {
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
			// denyAccess still runs: the quarantine may have begun while an element was installed.
			p.holdDeny(ctx, bridge, ip, res, "quarantine "+s.SessionID)
			res.Failed++
			res.Problems = append(res.Problems, "shape "+s.SessionID+
				": quarantined after "+itoa(q.Strikes)+" unproven activation(s); not re-admitting for another "+
				itoa(int((q.BackoffUntilBootMs-nowBoot)/1000))+"s")
			return provCtx{}, false
		}
		q.freshAttempt = true // the backoff has elapsed and durable state is readable: one fresh attempt
	}
	return provCtx{ip: ip, key: key, lease: lease, ev: ev, nowBoot: nowBoot}, true
}

// provisionTail is the kernel half: re-rate or renew an active class, or admit a new one through the staged sequence described at the top of this file.
func (p *phase3Shaping) provisionTail(ctx context.Context, bridge string, minor int, s shapeplan.Session, view gateView, now time.Time, res *shapingPlanResponse, pc provCtx) {
	ip, key, lease, ev, nowBoot := pc.ip, pc.key, pc.lease, pc.ev, pc.nowBoot
	if p.epochs == nil {
		p.epochs = map[string]int64{}
	}
	if p.pending == nil {
		p.pending = map[string]int64{}
	}
	if p.pb != nil {
		// The checks above ran earlier in the pass for this session; the clock the bound is measured on is read again.
		var cerr error
		if nowBoot, cerr = p.bootNow(); cerr != nil {
			p.failClosed(ctx, bridge, ip, res, "shape "+s.SessionID+": the monotonic security clock is unreadable ("+cerr.Error()+
				"); the activation bound cannot be measured, so no provisional access is granted")
			return
		}
	}

	// Re-rate of an already-active class: a `tc class change` in place (no new generation, no new origin,
	// counters not reset). This is also the lease renewal path for an unchanged session.
	if epoch, active := p.epochs[key]; active {
		if _, held := p.classes[key]; held {
			present, err := p.classPresentCached(ctx, bridge, minor)
			if err == nil && present {
				groupCid, gproblem := p.reRateGroup(ctx, bridge, s)
				if gproblem != "" {
					// A failed group re-rate leaves the last authorised rates in force.
					res.Failed++
					res.Problems = append(res.Problems, "re-rate "+s.SessionID+": "+gproblem)
					return
				}
				// An unchanged class is not rewritten: `tc class change` to the values already installed changes nothing, but it is one forked process
				// per device per session per pass. Only a class whose installed parent, rate and ceiling are read and equal the plan's is skipped.
				if !(p.pb != nil && p.installedMatchesPlan(ctx, bridge, minor, s, groupCid)) {
					if rerr := p.shp.ReRateSessionIn(ctx, bridge, ip, s.DownKbps, s.UpKbps, groupCid); rerr != nil {
						// A failed re-rate leaves the last authorised rate in force.
						res.Failed++
						res.Problems = append(res.Problems, "re-rate "+s.SessionID+": "+rerr.Error())
						return
					}
				}
				p.minorOwner[minorKey(bridge, minor)] = s.SessionID
				// Finishes a promotion whose acknowledgement was lost; idempotent for an active session.
				outcome, problem := p.proveActive(ctx, key, s.SessionID, bridge, minor, epoch, nowBoot)
				if problem != "" {
					res.Failed++
					res.Problems = append(res.Problems, problem)
				}
				if outcome == activationEnded {
					p.endClosed(ctx, bridge, ip, res, "session "+s.SessionID+": durable state has ended it; access withdrawn")
					p.endSeries(bridge, s.SessionID)
					return
				}
				if outcome == activationFailed {
					// proveActive already recorded the strike and backoff; endSeries leaves that record alone.
					p.failClosed(ctx, bridge, ip, res, "session "+s.SessionID+": activation never proven")
					p.endSeries(bridge, s.SessionID)
					return
				}
				// Also runs on retry after a lost admission, after a restart, and after a stray sweep.
				want := lease
				if outcome != activationProven {
					want = provisionalOrLess(lease)
				}
				if !p.assertLease(ctx, bridge, ip, want, view, res, s.SessionID) {
					return
				}
				if outcome == activationProven {
					res.Shaped++
					p.noteRenewable(key, bridge, ip, s, ev.Until, now)
				}
				return
			}
			// The kernel lost the class (mid-boot flush): end the series so the successor gets a fresh generation.
			p.endSeries(bridge, s.SessionID)
		}
	}

	// A pending generation from a failed attempt is reused so a retry does not re-baseline the origin
	// (register_class_origin returns ORIGIN_UNCHANGED for the same epoch).
	epoch, havePending := p.pending[key]
	if !havePending {
		alloc, err := p.allocEpoch(ctx)
		if err != nil {
			// Nothing installed yet, so nothing to tear down.
			res.Failed++
			res.Problems = append(res.Problems,
				"shape "+s.SessionID+": no class generation could be allocated; not made accountable")
			return
		}
		epoch = alloc
		p.pending[key] = epoch
	}

	// End a previous occupant's series before preparing the new class, so it cannot forward under another identity.
	if prev, held := p.minorOwner[minorKey(bridge, minor)]; held && prev != s.SessionID {
		p.endSeries(bridge, prev)
	}

	// SHARED allocation puts the class under the entitlement's aggregate class. The group is ensured
	// idempotently first: it belongs to the entitlement, and a second device must find it, not replace it.
	groupCid := ""
	if s.SpeedAllocation == speedAllocationShared {
		groupCid = shape.GroupClassid(s.EntitlementID)
		if groupCid == "" {
			p.failClosed(ctx, bridge, ip, res,
				"shape "+s.SessionID+": SHARED allocation with no entitlement to group by")
			return
		}
		if err := p.shp.EnsureGroupClass(ctx, bridge, groupCid, s.DownKbps); err != nil {
			p.failClosed(ctx, bridge, ip, res, "shared group (download) "+s.SessionID+": "+err.Error())
			return
		}
		if err := p.shp.EnsureGroupClass(ctx, shape.IFBName(bridge), groupCid, s.UpKbps); err != nil {
			p.failClosed(ctx, bridge, ip, res, "shared group (upload) "+s.SessionID+": "+err.Error())
			return
		}
	}
	if err := p.shp.PrepareSessionIn(ctx, bridge, ip, s.DownKbps, s.UpKbps, groupCid); err != nil {
		p.failClosed(ctx, bridge, ip, res, "prepare "+s.SessionID+": "+err.Error())
		return
	}

	// The origin is recorded before any forwarding filter exists so the first packet counts from a real
	// baseline; activate_session_enforcement verifies this checkpoint before promoting the Session.
	if problem, ended := p.registerOrigin(ctx, s, minor, epoch); ended {
		p.endClosed(ctx, bridge, ip, res, "session "+s.SessionID+": durable state has ended it; access withdrawn")
		p.dropEnded(bridge, s.SessionID)
		return
	} else if problem != "" {
		p.failClosed(ctx, bridge, ip, res, problem)
		return
	}

	// Metering only; the nft gate below is what admits the guest.
	if err := p.shp.ActivateSession(ctx, bridge, ip); err != nil {
		p.failClosed(ctx, bridge, ip, res, "activate "+s.SessionID+": "+err.Error())
		return
	}

	forwarding, ferr := p.shp.SessionForwarding(ctx, bridge, ip)
	present, perr := p.classPresent(ctx, bridge, minor)
	if ferr != nil || perr != nil || !forwarding || !present {
		p.failClosed(ctx, bridge, ip, res, "post-activation verification failed for "+s.SessionID)
		return
	}

	// Metering is proven before the guest gets the internet; admitting earlier would forward through the
	// default class, attributed to nobody. The lease stays short until durable ACTIVE is proven.
	if p.gate != nil {
		// Write-ahead: the attempt is fsynced before the element goes in (see phase3_journal.go).
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
			p.failClosed(ctx, bridge, ip, res, "authorization verification failed for "+s.SessionID)
			return
		}
	}

	p.completeAdmission(ctx, bridge, minor, s, view, now, res, pc, epoch)
}

// completeAdmission records the class as managed once it is in force in the kernel (both tc halves and the provisional authorization), then proves the
// Session durably active and gives it its full lease. Shared by the per-session path and the batched admission.
func (p *phase3Shaping) completeAdmission(ctx context.Context, bridge string, minor int, s shapeplan.Session, view gateView, now time.Time, res *shapingPlanResponse, pc provCtx, epoch int64) {
	ip, key, lease, ev, nowBoot := pc.ip, pc.key, pc.lease, pc.ev, pc.nowBoot
	if p.epochs == nil {
		p.epochs = map[string]int64{}
	}
	p.minorOwner[minorKey(bridge, minor)] = s.SessionID
	p.epochs[key] = epoch
	if p.classes == nil {
		p.classes = map[string]managedClass{}
	}
	p.classes[key] = managedClass{
		SessionID: s.SessionID, DeviceID: s.DeviceID, Bridge: bridge, Minor: minor,
		Epoch: epoch, BootID: p.bootID}
	delete(p.pending, key)

	// The guest holds a full-length lease only once the Session durably says active.
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
		p.noteRenewable(key, bridge, ip, s, ev.Until, now)
	case activationUnproven:
		// Keeps the provisional lease; the next pass retries the promotion.
	case activationEnded:
		p.endClosed(ctx, bridge, ip, res, "session "+s.SessionID+": durable state has ended it; access withdrawn")
		p.endSeries(bridge, s.SessionID)
	default:
		p.failClosed(ctx, bridge, ip, res, "session "+s.SessionID+": activation never proven")
		p.endSeries(bridge, s.SessionID)
	}
}

// reRateGroup re-rates a SHARED session's group class to the plan's allowance, once per pass, and returns the group the
// session is re-rated under ("" for PER_DEVICE). A SHARED session keeps its floor under the group; the group alone
// carries the allowance (audit Phase 8 M4 / Phase 6 M4).
func (p *phase3Shaping) reRateGroup(ctx context.Context, bridge string, s shapeplan.Session) (string, string) {
	if s.SpeedAllocation != speedAllocationShared {
		return "", ""
	}
	groupCid := shape.GroupClassid(s.EntitlementID)
	if groupCid == "" {
		return "", "SHARED allocation with no entitlement to group by"
	}
	k := bridge + "|" + groupCid
	if p.passGroups != nil && p.passGroups[k] {
		return groupCid, ""
	}
	if err := p.shp.EnsureGroupClass(ctx, bridge, groupCid, s.DownKbps); err != nil {
		return "", "shared group (download): " + err.Error()
	}
	if err := p.shp.EnsureGroupClass(ctx, shape.IFBName(bridge), groupCid, s.UpKbps); err != nil {
		return "", "shared group (upload): " + err.Error()
	}
	if p.passGroups != nil {
		p.passGroups[k] = true
	}
	return groupCid, ""
}

// provisionalOrLess caps the lease at the provisional one without overshooting the hard boundary.
func provisionalOrLess(lease time.Duration) time.Duration {
	if lease < phase3ProvisionalLease {
		return lease
	}
	return phase3ProvisionalLease
}

// assertLease installs or refreshes the kernel lease using this pass's snapshot (a map lookup on most
// passes). It returns false when it has already failed closed.
func (p *phase3Shaping) assertLease(ctx context.Context, bridge string, ip net.IP, want time.Duration, view gateView, res *shapingPlanResponse, sessionID string) bool {
	if p.gate == nil {
		return true
	}
	el, present, known := view.lookup(bridge, ip)
	if p.pb != nil {
		// Batched pass: leases that need writing are collected and written in one transaction (flushRenewals); a lease with time left is untouched.
		if known && present && !needsRenewal(el.Expires, want) {
			return true
		}
		p.pb.renew[gateKey(bridge, ip)] = pendingRenew{bridge: bridge, ip: ip, want: want, sessionID: sessionID, known: known}
		return true
	}
	if !known {
		// Unreadable set: refresh unconditionally, or a whole property would expire together.
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
		// A missing element re-added is a genuine admission, so verify it.
		ok, verr := p.gate.Authorized(ctx, bridge, ip)
		if verr != nil || !ok {
			p.failClosed(ctx, bridge, ip, res, "re-authorization verification failed for "+sessionID)
			return false
		}
	}
	return true
}

// failClosed is the one fail-closed path. Order matters: deny packet authorization first and prove it (until
// the nft element is gone the guest is online whatever tc says), then abort tc, stripping classification
// filters if the classes cannot be proven removed. It keeps the pending generation for the retry.
func (p *phase3Shaping) failClosed(ctx context.Context, bridge string, ip net.IP, res *shapingPlanResponse, problem string) {
	if p.pb != nil {
		// Batched pass: the denial is proven for every session that failed in one transaction at the next flush point (phase3_batch.go), and the
		// tc teardown follows it there, in the same order.
		p.pb.denies = append(p.pb.denies, pendingDeny{bridge: bridge, ip: ip, problem: problem, abort: true})
		return
	}
	if err := p.denyAccess(ctx, bridge, ip); err != nil {
		// The guest may still be authorized: the most serious outcome, reported as such.
		problem += " (PACKET AUTHORIZATION NOT PROVEN REMOVED: " + err.Error() + ")"
	}
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

// endClosed withdraws the access of a session whose access has simply ended (its window ran out, or durable state says it ended while the plan that
// listed it was in flight). It is failClosed's withdrawal, in the same order, but a withdrawal that is proven is an event, not a failure. In a batched
// pass it is counted (EndedMidPass); the per-session pass reports it as it always did.
func (p *phase3Shaping) endClosed(ctx context.Context, bridge string, ip net.IP, res *shapingPlanResponse, problem string) {
	if p.pb == nil {
		p.failClosed(ctx, bridge, ip, res, problem)
		return
	}
	p.pb.denies = append(p.pb.denies, pendingDeny{bridge: bridge, ip: ip, problem: problem, abort: true, quiet: true})
}

// classPresent requires the class on both the bridge (download) and its ifb (upload).
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

// classPresentCached is for renewal of an already-active class only; verification after creating or
// activating a class must read the kernel afresh (classPresent).
func (p *phase3Shaping) classPresentCached(ctx context.Context, bridge string, minor int) (bool, error) {
	down, err := p.readClassesForPass(ctx, bridge)
	if err != nil {
		return false, err
	}
	up, err := p.readClassesForPass(ctx, shape.IFBName(bridge))
	if err != nil {
		return false, err
	}
	_, d := down[minor]
	_, u := up[minor]
	return d && u, nil
}

// readClassesForPass caches per SHAPE step. Safe because nothing earlier in the step deletes an already-active
// class (teardown and strays run before it) and created classes use other minors. A failed read is never cached.
func (p *phase3Shaping) readClassesForPass(ctx context.Context, device string) (map[int]shape.ClassBytes, error) {
	if p.passClasses != nil {
		if c, ok := p.passClasses[device]; ok {
			return c, nil
		}
	}
	c, err := p.shp.ReadClasses(ctx, device)
	if err != nil {
		return nil, err
	}
	if p.passClasses != nil {
		p.passClasses[device] = c
	}
	return c, nil
}
