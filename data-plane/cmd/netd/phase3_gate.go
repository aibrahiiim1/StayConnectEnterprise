package main

// THE PHASE-3 PACKET-AUTHORIZATION GATE.
//
// Three different things decide whether a guest is on the internet, and conflating any two of them is how a
// guest ends up either unaccounted or unexpectedly cut off:
//
//	nft  — PACKET AUTHORIZATION. The forward chain accepts a guest's traffic only when its (bridge, IP) is in
//	       an authorization set, and the captive-portal DNAT rules redirect it when it is not. This, and only
//	       this, is what gives or denies internet access.
//	tc   — ACCOUNTABLE SHAPING/METERING. Classifies an authorized guest's packets into a managed HTB class so
//	       they are rate-limited and counted. Removing a tc filter does NOT deny access: the packets fall back
//	       to the bridge's default class and keep flowing, now unmetered.
//	DB   — DURABLE RUNTIME AUTHORIZATION STATE. What the appliance believes it granted, and the only record
//	       that survives a restart.
//
// netd owns the two kernel halves. Phase 3 writes ONLY the phase3_auth_ipv4 set; legacy scd writes only
// auth_ipv4. That separation is what lets Phase-3 reconciliation delete every stray it finds without ever
// being able to remove a live legacy guest's authorization.

import (
	"context"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// gate is the packet-authorization surface netd owns for Phase 3. It is an interface so the ordering
// properties (authorize only after accountable tc; deny before teardown) can be proven without a kernel.
type gate interface {
	// Authorize admits (bridge, ip) to the internet.
	Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error
	// Revoke removes the authorization. It must be safe to call when absent.
	Revoke(ctx context.Context, bridge string, ip net.IP) error
	// Authorized reports whether the authorization is ACTUALLY present. An Authorize that returned nil but
	// did not take is otherwise indistinguishable from success.
	Authorized(ctx context.Context, bridge string, ip net.IP) (bool, error)
	// List enumerates every Phase-3 authorization, for stray removal. It never enumerates the legacy set.
	List(ctx context.Context) ([]gateElem, error)
}

// gateElem is one authorization currently installed in the Phase-3 set.
type gateElem struct {
	Bridge string
	IP     net.IP
}

// nftGate is the real gate, scoped to the Phase-3 set.
type nftGate struct{ c *nft.Client }

func newNFTGate() *nftGate { return &nftGate{c: nft.New()} }

func (g *nftGate) Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error {
	return g.c.AllowIn(ctx, nft.Phase3AuthV4, bridge, ip, ttl)
}

func (g *nftGate) Revoke(ctx context.Context, bridge string, ip net.IP) error {
	return g.c.DenyIn(ctx, nft.Phase3AuthV4, bridge, ip)
}

func (g *nftGate) Authorized(ctx context.Context, bridge string, ip net.IP) (bool, error) {
	return g.c.AuthorizedIn(ctx, nft.Phase3AuthV4, bridge, ip)
}

func (g *nftGate) List(ctx context.Context) ([]gateElem, error) {
	elems, err := g.c.ListIn(ctx, nft.Phase3AuthV4)
	if err != nil {
		return nil, err
	}
	out := make([]gateElem, 0, len(elems))
	for _, e := range elems {
		out = append(out, gateElem{Bridge: e.Iface, IP: e.IP})
	}
	return out, nil
}

// denyAccess is THE fail-closed primitive: it removes packet authorization first and confirms it is gone.
//
// It is deliberately not named after tc. Stripping a tc filter leaves the guest forwarding through the
// bridge's default class — unshaped and uncounted, but still online — so "deny" can only ever mean the nft
// gate. Everything that must stop a guest's traffic calls this, and only then touches tc.
func (p *phase3Shaping) denyAccess(ctx context.Context, bridge string, ip net.IP) error {
	if p.gate == nil {
		return nil // no gate wired (older unit tests drive tc directly); nothing to deny
	}
	if err := p.gate.Revoke(ctx, bridge, ip); err != nil {
		return err
	}
	// Verify. A Revoke that reported success but left the element behind would leave the guest online while
	// every record said otherwise — the exact state this ordering exists to prevent.
	still, err := p.gate.Authorized(ctx, bridge, ip)
	if err != nil {
		return err
	}
	if still {
		return errStillAuthorized
	}
	return nil
}

// errStillAuthorized means packet authorization could not be proven removed.
var errStillAuthorized = errAuthz("packet authorization is still present after revocation")

type errAuthz string

func (e errAuthz) Error() string { return string(e) }
