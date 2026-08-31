package main

// WHO OWNS THIS ADDRESS RIGHT NOW.
//
// The applier authorizes an ADDRESS: an nft element is (bridge, IP), and a tc class is selected by IP. The
// producer names the session that address belongs to, and the applier has taken that on trust and renewed the
// authorization for as long as durable state said the session was live.
//
// Trust was the defect. A guest device is switched off; its DHCP lease lapses; the address is handed to a
// different guest; the appliance goes on authorizing it, and meters the new holder's traffic onto the old
// guest's entitlement. On the PRE-LIVE appliance the lease record shows exactly that: 192.168.77.139 was
// reissued to a different MAC while a live Session still held kernel authorization for it.
//
// The producer's own rule retires an attachment when the device turns up somewhere newer. It cannot see the
// other case — the device that simply left — because durable state contains no evidence of an absence. DHCP
// does: it is the authority that assigned the address in the first place, and netd owns it.
//
// So before an entitled session is renewed, the applier asks Kea whose address this is.

import (
	"context"
	"net"
	"strings"
	"time"
)

// addressOwnership is the answer to "does this address still belong to this device".
type addressOwnership int

const (
	// addressOwned — DHCP says this address is leased to this session's MAC.
	addressOwned addressOwnership = iota
	// addressForeign — DHCP says the address is leased to a DIFFERENT device, or is not leased at all. Either
	// way this session no longer owns it, and authorization must stop.
	addressForeign
	// addressUnknown — the evidence is unavailable: Kea is down, the control socket is unreadable, the plan
	// carries no MAC. NOT a proof of loss, and never treated as one.
	addressUnknown
)

// leaseSource is the DHCP evidence netd already owns. It is an interface so the ownership rules can be proven
// without a DHCP server, and so an evidence outage is a case the tests can stage rather than a scenario nobody
// exercises until it happens at four in the morning.
type leaseSource interface {
	Leases() ([]KeaLease, error)
}

// addressOwner answers the ownership question from DHCP leases.
//
// now is injectable because lease currency is a question about TIME as much as about identity, and a rule
// that decides who is on the network must be provable at a chosen instant rather than only at whatever
// instant the test happens to run.
type addressOwner struct {
	src leaseSource
	now func() time.Time
}

func (a addressOwner) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// current reports whether a lease row is a lease somebody HOLDS, as opposed to one Kea is still carrying.
//
// This distinction was found on the PRE-LIVE appliance, and it is not academic. memfile keeps a lease row after
// it lapses, with state 0, until the address is handed to somebody else or the file is compacted: the appliance
// was holding ninety-two such rows, days past their six hundred second lifetime, every one of them still
// labelled state 0. Believing them would mean an address that was abandoned last Tuesday still counts as owned
// today — the precise failure the ownership check exists to stop, restored through the evidence rather than
// through durable state.
func (a addressOwner) current(l KeaLease, at time.Time) bool {
	if l.State == keaLeaseStateExpired || l.ValidLft <= 0 || l.CLTT <= 0 {
		return false
	}
	return l.CLTT+int64(l.ValidLft) > at.Unix()
}

// keaLeaseStateExpired is Kea's state for a lease that has been released or has expired. Such a lease is
// history: it does not make its former holder the current owner of the address.
const keaLeaseStateExpired = 2

// Owns reports whether ip is currently leased to mac.
//
// A MISSING LEASE IS NOT OWNERSHIP. Guests on this appliance are addressed by DHCP, so an address with no live
// lease is an address nobody holds — and an authorization for it protects nobody while remaining available to
// whoever picks the address up next, statically or otherwise. It is therefore FOREIGN, not unknown.
//
// The distinction that matters is between "DHCP told us something" and "we could not ask". Only the second is
// unknown, and only unknown is allowed to leave a guest online on the strength of the producer's word.
func (a addressOwner) Owns(ctx context.Context, ip net.IP, mac string) addressOwnership {
	if a.src == nil || ip == nil || strings.TrimSpace(mac) == "" {
		return addressUnknown
	}
	leases, err := a.src.Leases()
	if err != nil {
		// Kea unreachable. The honest answer is that we do not know, and tearing every guest down because the
		// DHCP control socket hiccupped would be an outage manufactured out of a missing answer.
		return addressUnknown
	}
	want, at := ip.String(), a.clock()
	for _, l := range leases {
		if l.IPAddress != want {
			continue
		}
		if !a.current(l, at) {
			continue // history, not a current holder
		}
		if strings.EqualFold(strings.TrimSpace(l.HWAddr), strings.TrimSpace(mac)) {
			return addressOwned
		}
		// A live lease naming a different device. This is the case that had a stranger on a guest's
		// entitlement, and it is the least ambiguous evidence there is.
		return addressForeign
	}
	// No live lease for the address at all. Nobody holds it, so this session does not either.
	return addressForeign
}

// noteUnverified records that a pass declined to renew a session because ownership could not be checked.
//
// It exists so the state is VISIBLE rather than merely absent. A guest silently dropping off the network when
// the DHCP control socket goes quiet is the kind of failure that gets diagnosed twice: once as a network
// problem and once, much later, as this. The health surface reports the count, and a pass that verifies the
// session again clears it.
func (p *phase3Shaping) noteUnverified(bridge, sessionID string) {
	if p.unverified == nil {
		p.unverified = map[string]bool{}
	}
	p.unverified[classKey(bridge, sessionID)] = true
}

// clearUnverified records that ownership was positively confirmed again.
func (p *phase3Shaping) clearUnverified(bridge, sessionID string) {
	delete(p.unverified, classKey(bridge, sessionID))
}

// unverifiedCount is how many sessions this writer is currently declining to renew for want of evidence.
func (p *phase3Shaping) unverifiedCount() int { return len(p.unverified) }
