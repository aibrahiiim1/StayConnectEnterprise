package main

// The applier authorizes an address (nft element is (bridge, IP); tc class is selected by IP), so a lapsed lease
// reissued to another guest would keep the old guest's entitlement and meter the new holder onto it. The
// producer cannot see a device that simply left; DHCP can, and netd owns it. So before an entitled session is
// renewed the applier asks Kea whose address this is.

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"
)

type addressOwnership int

const (
	// addressOwned: DHCP says the address is leased to this session's MAC.
	addressOwned addressOwnership = iota
	// addressForeign: leased to a different device, or not leased at all; authorization must stop.
	addressForeign
	// addressUnknown: the evidence is unavailable (Kea down, socket unreadable, plan carries no MAC). Not proof of loss.
	addressUnknown
)

type leaseSource interface {
	Leases() ([]KeaLease, error)
}

// leaseStability is implemented by a lease source that can say whether the ABSENCE of a lease is believable. Kea answers "no lease for this address" in the
// same words whether it was given up or Kea has just been started, reconfigured or pointed at a fresh lease file; reading that as "given up" would end
// every session at once, and ending a session is not undone by the evidence coming back.
type leaseStability interface {
	// AbsenceTrusted reports whether "no lease" can be taken at face value for ip, and why not when it cannot.
	AbsenceTrusted(ip net.IP) (trusted bool, why string)
}

// leaseEpoch is implemented by a source that can say when the Kea instance it reads started; a different start time means Kea restarted.
type leaseEpoch interface {
	KeaEpoch() (started time.Time, ok bool)
}

// addressOwner answers the ownership question from DHCP leases; now is injectable so lease currency is testable.
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

// current reports whether a lease row is held, not merely still carried: memfile keeps a lapsed lease row with
// state 0 until the address is reissued or the file is compacted, so state alone would call abandoned addresses owned.
func (a addressOwner) current(l KeaLease, at time.Time) bool {
	if l.State == keaLeaseStateExpired || l.ValidLft <= 0 || l.CLTT <= 0 {
		return false
	}
	return l.CLTT+int64(l.ValidLft) > at.Unix()
}

// keaLeaseStateExpired is Kea's state for a released or expired lease, which is history, not ownership.
const keaLeaseStateExpired = 2

// ownershipEvidence is what the lease table says about one address.
type ownershipEvidence struct {
	State addressOwnership
	// Empty: the table held no lease at all. Missing: it held none for this address (Empty implies Missing).
	Empty, Missing bool
	// Until is when the matching lease lapses, set only when State is addressOwned.
	Until time.Time
	// Epoch is when the Kea instance that answered started; zero when the source cannot say.
	Epoch time.Time
}

// Owns reports whether ip is currently leased to mac. A missing lease is foreign, not unknown (nobody holds the
// address, and an authorization for it would serve whoever picks it up next). Only "we could not ask" is unknown,
// and only unknown may leave a guest online on the producer's word.
func (a addressOwner) Owns(ctx context.Context, ip net.IP, mac string) addressOwnership {
	return a.Evidence(ctx, ip, mac).State
}

// Evidence is Owns with what the caller needs to bridge a lease table that has been lost: whether the lease was missing or the table empty, when the owned
// lease lapses, and which Kea instance answered.
func (a addressOwner) Evidence(ctx context.Context, ip net.IP, mac string) ownershipEvidence {
	if a.src == nil || ip == nil || strings.TrimSpace(mac) == "" {
		return ownershipEvidence{State: addressUnknown}
	}
	leases, err := a.src.Leases()
	if err != nil {
		// Kea unreachable: unknown, since tearing every guest down on a control-socket hiccup would manufacture an outage.
		return ownershipEvidence{State: addressUnknown}
	}
	var epoch time.Time
	if ep, ok := a.src.(leaseEpoch); ok {
		epoch, _ = ep.KeaEpoch()
	}
	want, at := ip.String(), a.clock()
	for _, l := range leases {
		if l.IPAddress != want || !a.current(l, at) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(l.HWAddr), strings.TrimSpace(mac)) {
			return ownershipEvidence{State: addressOwned, Until: time.Unix(l.CLTT+int64(l.ValidLft), 0), Epoch: epoch}
		}
		// A live lease naming a different device is positive evidence the address has been reassigned.
		return ownershipEvidence{State: addressForeign, Epoch: epoch}
	}
	// No lease for this address. Unless the source vouches that Kea is stable and serves the subnet, that is UNKNOWN rather than "given up": access is not
	// renewed (it lapses with its current bounded lease) but no session is ended, so the evidence returning restores service with no re-authentication,
	// and nothing about the Entitlement or its quota is touched.
	ev := ownershipEvidence{State: addressForeign, Empty: len(leases) == 0, Missing: true, Epoch: epoch}
	if st, ok := a.src.(leaseStability); ok {
		if trusted, why := st.AbsenceTrusted(ip); !trusted {
			slog.Warn("netd: DHCP has no lease for this address, but that answer is not trusted yet; ownership is treated as unknown, not as lost",
				"ip", want, "why", why)
			ev.State = addressUnknown
		}
	}
	return ev
}

// leaseMemoryCap bounds how long a lease verified earlier keeps an address authorized when Kea later has no record of it.
const leaseMemoryCap = 4 * time.Hour

// keaRestartTolerance is how far apart two derived Kea start times may be and still be the same instance.
const keaRestartTolerance = 15 * time.Second

// leaseMemory is a lease verified earlier, bound to the exact address, device and Kea instance it was verified for.
type leaseMemory struct {
	ip, mac string
	until   time.Time
	epoch   time.Time
}

// rememberLease records the lease just verified, so that a lease Kea later loses (restarted onto an empty or partial file) does not cut off a client whose lease
// is still valid on its own side and who has simply not reached its renewal time yet. The memory never outlives the lease itself.
func (p *phase3Shaping) rememberLease(key, ip, mac string, until, epoch, now time.Time) {
	if p.leaseMem == nil {
		p.leaseMem = map[string]leaseMemory{}
	}
	if capAt := now.Add(leaseMemoryCap); until.After(capAt) {
		until = capAt
	}
	p.leaseMem[key] = leaseMemory{ip: ip, mac: strings.ToLower(strings.TrimSpace(mac)), until: until, epoch: epoch}
}

// bridgedLeaseLeft is how much longer the remembered lease still covers this exact address and device. It applies only when Kea's record of it is gone because
// the table is empty, or because a different Kea instance now answers than the one that verified it; with the same instance still running, a missing
// lease means it was released or expired, and the address is not bridged.
func (p *phase3Shaping) bridgedLeaseLeft(key, ip, mac string, ev ownershipEvidence, now time.Time) time.Duration {
	m, ok := p.leaseMem[key]
	if !ok || m.ip != ip || m.mac != strings.ToLower(strings.TrimSpace(mac)) {
		return 0
	}
	if !ev.Empty && !keaRestarted(m.epoch, ev.Epoch) {
		return 0
	}
	left := m.until.Sub(now)
	if left < time.Second {
		return 0
	}
	return left.Truncate(time.Second)
}

func keaRestarted(was, now time.Time) bool {
	if was.IsZero() || now.IsZero() {
		return false
	}
	d := now.Sub(was)
	return d > keaRestartTolerance || d < -keaRestartTolerance
}

// pruneLeaseMemory drops memories whose lease has lapsed.
func (p *phase3Shaping) pruneLeaseMemory(now time.Time) {
	for k, m := range p.leaseMem {
		if !m.until.After(now) {
			delete(p.leaseMem, k)
		}
	}
}

// noteUnverified records that a pass declined to renew a session because ownership could not be checked, so the
// health surface can report the count; a pass that verifies the session again clears it.
func (p *phase3Shaping) noteUnverified(bridge, sessionID string) {
	if p.unverified == nil {
		p.unverified = map[string]bool{}
	}
	p.unverified[classKey(bridge, sessionID)] = true
}

func (p *phase3Shaping) clearUnverified(bridge, sessionID string) {
	delete(p.unverified, classKey(bridge, sessionID))
}

func (p *phase3Shaping) unverifiedCount() int { return len(p.unverified) }
