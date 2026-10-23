package main

// Only the nft set gives or denies internet access. Removing a tc filter does not deny access: packets fall
// back to the bridge's default class and keep flowing, unmetered.

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/conntrack"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// gate is the packet-authorization surface; an interface so ordering can be tested without a kernel.
type gate interface {
	// Authorize admits (bridge, ip) for a bounded, renewable lease. ttl must be positive: a permanent
	// authorization would outlive every process that maintains it. Re-calling refreshes the lease.
	Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error
	// Revoke must be safe to call when absent.
	Revoke(ctx context.Context, bridge string, ip net.IP) error
	// Authorized reports whether the authorization is actually present; a nil Authorize may not have taken.
	Authorized(ctx context.Context, bridge string, ip net.IP) (bool, error)
	// List enumerates the Phase-3 set only, never the legacy set.
	List(ctx context.Context) ([]gateElem, error)
}

type gateElem struct {
	Bridge string
	IP     net.IP
	// Expires is the lease remaining; renewal refreshes only leases running down.
	Expires time.Duration
}

type nftGate struct{ c *nft.Client }

func newNFTGate() *nftGate { return &nftGate{c: nft.New()} }

// LeaseIn, not AllowIn: a repeated `add element` does not restart an existing element's timer.
func (g *nftGate) Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error {
	return g.c.LeaseIn(ctx, nft.Phase3AuthV4, bridge, ip, ttl)
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
		out = append(out, gateElem{Bridge: e.Iface, IP: e.IP, Expires: e.Expires})
	}
	return out, nil
}

// The forward chain still accepts whatever the legacy auth_ipv4 set holds, so a stray element there would be
// unaccounted internet access. It is drained every pass by this separate sweeper so the gate can only ever
// touch the Phase-3 set.
type legacySet interface {
	// Drain removes every element and reports how many.
	Drain(ctx context.Context) (int, error)
}

type nftLegacySet struct{ c *nft.Client }

func newNFTLegacySet() *nftLegacySet { return &nftLegacySet{c: nft.New()} }

func (l *nftLegacySet) Drain(ctx context.Context) (int, error) {
	elems, err := l.c.ListIn(ctx, nft.AuthV4)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range elems {
		if err := l.c.DenyIn(ctx, nft.AuthV4, e.Iface, e.IP); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// flushingGate ends an address's open flows on revoke. The forward chain accepts established/related before
// the authorization sets, so removing an element alone stops only new connections. denyAccess runs before any
// tc class is removed, so the flows are ended while still metered.
// A flush dumps conntrack, so it is not repeated until the address is authorized again.
type flushingGate struct {
	gate
	flows conntrack.Flusher

	mu sync.Mutex
	// clean marks bridge|ip keys flushed since their last revocation; absent means unknown, and unknown is flushed.
	clean map[string]bool
}

func newFlushingGate(g gate, f conntrack.Flusher) *flushingGate {
	return &flushingGate{gate: g, flows: f, clean: map[string]bool{}}
}

func (g *flushingGate) Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error {
	g.mu.Lock()
	delete(g.clean, gateKey(bridge, ip))
	g.mu.Unlock()
	return g.gate.Authorize(ctx, bridge, ip, ttl)
}

func (g *flushingGate) Revoke(ctx context.Context, bridge string, ip net.IP) error {
	if err := g.gate.Revoke(ctx, bridge, ip); err != nil {
		return err
	}
	key := gateKey(bridge, ip)
	g.mu.Lock()
	done := g.clean[key]
	g.mu.Unlock()
	if done || g.flows == nil || ip == nil {
		return nil
	}
	n, err := g.flows.FlushAddrs(ctx, []net.IP{ip})
	if err != nil {
		// The revocation stands; only already-open flows survive, and the next revocation retries.
		slog.Error("netd: authorization revoked but the address's open flows could not be ended",
			"bridge", bridge, "ip", ip.String(), "err", err)
		return nil
	}
	if n > 0 {
		slog.Info("netd: ended the open flows of a revoked address", "bridge", bridge, "ip", ip.String(), "flows", n)
	}
	g.mu.Lock()
	g.clean[key] = true
	g.mu.Unlock()
	return nil
}

// gateView is one pass's snapshot of the Phase-3 set, so stray removal and renewal judge the same set and the
// steady state costs one nft transaction per pass.
type gateView struct {
	// ok is false when the set could not be enumerated: the state is then unknown, never empty, which would
	// skip due renewals.
	ok    bool
	byKey map[string]gateElem
}

func gateKey(bridge string, ip net.IP) string {
	if ip == nil {
		return bridge + "|"
	}
	return bridge + "|" + ip.String()
}

// Caller holds p.mu.
func (p *phase3Shaping) snapshotGate(ctx context.Context) gateView {
	v := gateView{byKey: map[string]gateElem{}}
	if p.gate == nil {
		v.ok = true // no gate wired: an empty view is the truth
		return v
	}
	elems, err := p.gate.List(ctx)
	if err != nil {
		return v // ok stays false
	}
	for _, e := range elems {
		v.byKey[gateKey(e.Bridge, e.IP)] = e
	}
	v.ok = true
	return v
}

// lookup's last result is whether the answer is known; never read "not present" from an unreadable set.
func (v gateView) lookup(bridge string, ip net.IP) (gateElem, bool, bool) {
	if !v.ok {
		return gateElem{}, false, false
	}
	e, ok := v.byKey[gateKey(bridge, ip)]
	return e, ok, true
}

// denyAccess is the fail-closed primitive: revoke nft authorization and confirm it is gone. Stripping a tc
// filter leaves the guest online, so anything that must stop traffic calls this before touching tc.
func (p *phase3Shaping) denyAccess(ctx context.Context, bridge string, ip net.IP) error {
	if p.gate == nil {
		return nil // no gate wired (older unit tests drive tc directly); nothing to deny
	}
	if err := p.gate.Revoke(ctx, bridge, ip); err != nil {
		return err
	}
	// Verify: a Revoke that succeeded but left the element would leave the guest online unnoticed.
	still, err := p.gate.Authorized(ctx, bridge, ip)
	if err != nil {
		return err
	}
	if still {
		return errStillAuthorized
	}
	return nil
}

var errStillAuthorized = errAuthz("packet authorization is still present after revocation")

type errAuthz string

func (e errAuthz) Error() string { return string(e) }
