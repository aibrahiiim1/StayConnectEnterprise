package main

// Only the nft set gives or denies internet access. Removing a tc filter does not deny access: packets fall
// back to the bridge's default class and keep flowing, unmetered.

import (
	"context"
	"log/slog"
	"net"
	"os"
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

// setOps is the slice of the nft client the gate uses; *nft.Client satisfies it and a test substitutes a map.
type setOps interface {
	LeaseIn(ctx context.Context, set, iface string, ip net.IP, ttl time.Duration) error
	DenyIn(ctx context.Context, set, iface string, ip net.IP) error
	AuthorizedIn(ctx context.Context, set, iface string, ip net.IP) (bool, error)
	ListIn(ctx context.Context, set string) ([]nft.Element, error)
	LeaseMAC(ctx context.Context, iface string, ip net.IP, mac string, ttl time.Duration) error
	DenyMAC(ctx context.Context, iface string, ip net.IP) error
}

// macBinder is implemented by a gate that can bind an authorization to the device's hardware address (AD-23 a).
type macBinder interface {
	BindMAC(bridge string, ip net.IP, mac string)
}

// nftGate keeps TWO sets in step whenever it knows the device's MAC: the IP-only set (always) and the MAC-bound set.
// Which one the forward chain accepts on is a render decision (Topology.MACBinding), so flipping the policy needs no
// re-population and the IP-only fallback is always warm.
type nftGate struct {
	c    setOps
	macs sync.Map // gateKey(bridge, ip) -> normalised MAC, bound by the shaping pass after DHCP ownership is verified
	// macOn reports whether the ruleset currently accepts on the MAC-bound set. nil = off.
	macOn func() bool
}

func newNFTGate() *nftGate { return &nftGate{c: nft.New()} }

func (g *nftGate) bound() bool { return g.macOn != nil && g.macOn() }

func (g *nftGate) BindMAC(bridge string, ip net.IP, mac string) {
	if n := nft.NormalizeMAC(mac); n != "" && ip != nil {
		g.macs.Store(gateKey(bridge, ip), n)
	}
}

func (g *nftGate) macFor(bridge string, ip net.IP) string {
	if v, ok := g.macs.Load(gateKey(bridge, ip)); ok {
		return v.(string)
	}
	return ""
}

// LeaseIn, not AllowIn: a repeated `add element` does not restart an existing element's timer.
func (g *nftGate) Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error {
	if err := g.c.LeaseIn(ctx, nft.Phase3AuthV4, bridge, ip, ttl); err != nil {
		return err
	}
	mac := g.macFor(bridge, ip)
	if mac == "" {
		if g.bound() {
			slog.Warn("netd: MAC binding is on and this address has no known device address; it is not authorized by the MAC-bound set",
				"bridge", bridge, "ip", ip.String())
		}
		return nil
	}
	if err := g.c.LeaseMAC(ctx, bridge, ip, mac, ttl); err != nil {
		if g.bound() {
			return err
		}
		slog.Warn("netd: the MAC-bound authorization could not be written (MAC binding is off, so access is unaffected)",
			"bridge", bridge, "ip", ip.String(), "err", err)
	}
	return nil
}

func (g *nftGate) Revoke(ctx context.Context, bridge string, ip net.IP) error {
	err := g.c.DenyIn(ctx, nft.Phase3AuthV4, bridge, ip)
	if merr := g.c.DenyMAC(ctx, bridge, ip); merr != nil && err == nil {
		// A leftover MAC element grants access only while binding is on; then the revocation is not proven.
		if g.bound() {
			err = merr
		} else {
			slog.Warn("netd: a MAC-bound authorization could not be removed (MAC binding is off)", "bridge", bridge, "ip", ip.String(), "err", merr)
		}
	}
	if err == nil && ip != nil {
		g.macs.Delete(gateKey(bridge, ip))
	}
	return err
}

func (g *nftGate) Authorized(ctx context.Context, bridge string, ip net.IP) (bool, error) {
	ok, err := g.c.AuthorizedIn(ctx, nft.Phase3AuthV4, bridge, ip)
	if err != nil || !ok || !g.bound() {
		return ok, err
	}
	// With binding on, the IP-only element is not what admits the device: the MAC-bound one is.
	elems, err := g.c.ListIn(ctx, nft.Phase3MACV4)
	if err != nil {
		return false, err
	}
	for _, e := range elems {
		if e.IP.Equal(ip) && e.Iface == bridge {
			return true, nil
		}
	}
	return false, nil
}

func (g *nftGate) List(ctx context.Context) ([]gateElem, error) {
	elems, err := g.c.ListIn(ctx, nft.Phase3AuthV4)
	if err != nil {
		return nil, err
	}
	out := make([]gateElem, 0, len(elems))
	have := map[string]bool{}
	for _, e := range elems {
		have[gateKey(e.Iface, e.IP)] = true
		out = append(out, gateElem{Bridge: e.Iface, IP: e.IP, Expires: e.Expires})
	}
	// The MAC-bound set is read on its own: if it is unreadable the IP-only view still stands (binding off), and with binding
	// on an unreadable set is an unknown state, never an empty one.
	macElems, merr := g.c.ListIn(ctx, nft.Phase3MACV4)
	if merr != nil {
		if g.bound() {
			return nil, merr
		}
		return out, nil
	}
	inMAC := map[string]bool{}
	for _, e := range macElems {
		k := gateKey(e.Iface, e.IP)
		inMAC[k] = true
		if !have[k] {
			// A MAC-bound element with no IP-only twin has no desired session behind it: surfaced so the stray sweep revokes it.
			out = append(out, gateElem{Bridge: e.Iface, IP: e.IP, Expires: e.Expires})
		}
	}
	// A device whose MAC is known but whose MAC-bound element is missing is renewed on this pass (Expires 0 forces a refresh).
	for i, ge := range out {
		if !inMAC[gateKey(ge.Bridge, ge.IP)] && g.macFor(ge.Bridge, ge.IP) != "" {
			out[i].Expires = 0
		}
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

	// seen is the authorizations the previous successful List showed (AD-24 b). An address that was there and is not now has LEFT
	// the set without a Revoke through this gate: its lease lapsed in the kernel (the producer stopped, a plan was lost, a restart
	// dropped a renewal). The forward chain accepts established flows before the authorization sets, so removing the element did
	// not end its open flows; they are ended here, scoped to those addresses only, never the whole table.
	seen map[string]net.IP
	// flushLapsed is the off-switch (NETD_FLUSH_LAPSED=false).
	flushLapsed bool
}

func newFlushingGate(g gate, f conntrack.Flusher) *flushingGate {
	return &flushingGate{gate: g, flows: f, clean: map[string]bool{}, seen: map[string]net.IP{}, flushLapsed: os.Getenv("NETD_FLUSH_LAPSED") != "false"}
}

// List is the inner gate's, plus the AD-24 (b) sweep of addresses that left the set since the previous list.
func (g *flushingGate) List(ctx context.Context) ([]gateElem, error) {
	elems, err := g.gate.List(ctx)
	if err != nil {
		return nil, err // an unreadable set is unknown: nothing is concluded from it
	}
	now := make(map[string]net.IP, len(elems))
	liveIPs := map[string]bool{}
	for _, e := range elems {
		now[gateKey(e.Bridge, e.IP)] = e.IP
		liveIPs[e.IP.String()] = true
	}
	g.mu.Lock()
	var lapsed []net.IP
	var lapsedKeys []string
	for k, ip := range g.seen {
		if _, still := now[k]; still || liveIPs[ip.String()] {
			continue
		}
		if g.clean[k] {
			continue // revoked through this gate, and its flows were already ended
		}
		lapsed = append(lapsed, ip)
		lapsedKeys = append(lapsedKeys, k)
	}
	g.seen = now
	enabled, flows := g.flushLapsed, g.flows
	g.mu.Unlock()
	if len(lapsed) == 0 || !enabled || flows == nil {
		return elems, nil
	}
	n, ferr := flows.FlushAddrs(ctx, lapsed)
	if ferr != nil {
		// Retried on the next list: the keys go back so the sweep sees them as "still to flush".
		slog.Error("netd: authorizations lapsed but the addresses' open flows could not be ended; retrying next pass", "addresses", len(lapsed), "err", ferr)
		g.mu.Lock()
		for i, k := range lapsedKeys {
			g.seen[k] = lapsed[i]
		}
		g.mu.Unlock()
		return elems, nil
	}
	if n > 0 {
		slog.Info("netd: ended the open flows of addresses whose authorization lapsed", "addresses", len(lapsed), "flows", n)
	}
	g.mu.Lock()
	for _, k := range lapsedKeys {
		g.clean[k] = true
	}
	g.mu.Unlock()
	return elems, nil
}

// BindMAC passes the device address to a wrapped gate that can use it.
func (g *flushingGate) BindMAC(bridge string, ip net.IP, mac string) {
	if mb, ok := g.gate.(macBinder); ok {
		mb.BindMAC(bridge, ip, mac)
	}
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
	// aged is how long ago the snapshot was taken: the lease an element had left then is that much shorter now.
	aged time.Duration
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
	if ok && e.Expires > 0 && v.aged > 0 {
		if e.Expires -= v.aged; e.Expires <= 0 {
			e.Expires = 1 // lapsed since the snapshot (or about to): due for renewal, never "no expiry reported"
		}
	}
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
