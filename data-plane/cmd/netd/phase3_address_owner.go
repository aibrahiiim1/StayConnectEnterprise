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

// leaseStability is implemented by a lease source that can say whether an EMPTY answer is believable. Kea answers "no leases" in the same words whether
// nobody holds one or it has just been started, reconfigured or pointed at a fresh lease file; reading that as "every address has been given up"
// would end every session at once, and ending a session is not undone by the evidence coming back.
type leaseStability interface {
	// EmptyAnswerTrusted reports whether "no leases" can be taken at face value for ip, and why not when it cannot.
	EmptyAnswerTrusted(ip net.IP) (trusted bool, why string)
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

// Owns reports whether ip is currently leased to mac. A missing lease is foreign, not unknown (nobody holds the
// address, and an authorization for it would serve whoever picks it up next). Only "we could not ask" is unknown,
// and only unknown may leave a guest online on the producer's word.
func (a addressOwner) Owns(ctx context.Context, ip net.IP, mac string) addressOwnership {
	if a.src == nil || ip == nil || strings.TrimSpace(mac) == "" {
		return addressUnknown
	}
	leases, err := a.src.Leases()
	if err != nil {
		// Kea unreachable: unknown, since tearing every guest down on a control-socket hiccup would manufacture an outage.
		return addressUnknown
	}
	// An empty table is the one answer that can mean "Kea lost its leases" as easily as "nobody holds one". Unless the source vouches that it is
	// stable and serves this address's subnet, it is UNKNOWN: access is not renewed (it lapses with its current bounded lease) but no session
	// is ended, so the evidence returning restores service with no re-authentication, and nothing about the Entitlement or its quota is touched.
	if len(leases) == 0 {
		if st, ok := a.src.(leaseStability); ok {
			if trusted, why := st.EmptyAnswerTrusted(ip); !trusted {
				slog.Warn("netd: DHCP reports no leases at all, but that answer is not trusted yet; address ownership is treated as unknown, not as lost",
					"ip", ip.String(), "why", why)
				return addressUnknown
			}
		}
	}
	want, at := ip.String(), a.clock()
	for _, l := range leases {
		if l.IPAddress != want {
			continue
		}
		if !a.current(l, at) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(l.HWAddr), strings.TrimSpace(mac)) {
			return addressOwned
		}
		// A live lease naming a different device.
		return addressForeign
	}
	return addressForeign
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
