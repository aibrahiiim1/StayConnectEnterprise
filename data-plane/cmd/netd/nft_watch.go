package main

// THE RULESET IS RE-ASSERTED WHILE NETD RUNS, NOT ONLY WHEN IT STARTS (audit B-26).
//
// The structure converged at boot and at apply/rollback only. Anything else that replaced the ruleset in between
// -- `systemctl restart nftables` (whose /etc/nftables.conf begins with `flush ruleset`), an operator's `nft -f`,
// a package upgrade -- left netd writing authorizations into a set that no longer existed, the captive redirect
// gone and the guest forward policy whatever the replacement said, until the next reboot or network change.
//
// So the in-force intent is re-converged on a short cadence. The engine (internal/nftconverge) reads one marker
// set and issues NO mutation when the live fingerprint already matches, so the steady-state cost is one `nft
// list set` per tick. When it does not match, the current render is installed atomically with the live
// authorizations carried across.
//
// The same re-assertion carries the MANAGEMENT PIN (NETD_MGMT_ADDR=auto, below): the pinned address is part of
// the render, so a WAN address change made through System network is followed by the firewall within one tick --
// and immediately, because the system-network apply asks for a re-assertion as soon as netplan has applied.

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"
)

// mgmtAddrAuto asks netd to pin the Admin Console's port 443 to the management interface's CURRENT primary IPv4
// address, re-read at every render. A literal address pins to that address forever, which locks the console out
// the moment System network changes the WAN address; "auto" follows it.
const mgmtAddrAuto = "auto"

// resolveMgmtAddr turns the configured pin into the address the render uses. An "auto" pin whose interface has
// no IPv4 address yet renders NO pin (the interface-wide accept): failing towards reachability is the only safe
// direction for the rule that keeps the operator able to reach the appliance at all.
func (a *applier) resolveMgmtAddr(configured string) string {
	if strings.TrimSpace(configured) != mgmtAddrAuto {
		return configured
	}
	lookup := a.mgmtAddrFn
	if lookup == nil {
		lookup = func(iface string) string { ip, _ := primaryV4(iface); return ip }
	}
	ip := net.ParseIP(lookup(a.topo.MgmtInterface)).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

// reassertNftStructure re-converges the in-force intent. It stands aside while an apply, rollback or boot
// reconcile is running (they converge themselves), and does nothing on a factory-clean appliance.
func (a *applier) reassertNftStructure(ctx context.Context, trigger string) {
	if a.dryRun || a.busy.Load() > 0 {
		return
	}
	a.busy.Add(1)
	defer a.busy.Add(-1)
	// The intent is read UNDER the converge lock, so a concurrent apply or rollback -- which publishes its intent
	// before it converges -- can never be overwritten by this pass with the intent it replaced: either this pass
	// already sees the new intent, or the apply's own converge runs after it.
	a.nftMu.Lock()
	defer a.nftMu.Unlock()
	intent, _, known := a.inForce()
	if !known {
		return
	}
	if _, err := a.ensureNftStructureLocked(ctx, intent, trigger); err != nil {
		slog.Error("netd: the live ruleset does not match the in-force configuration and could not be re-asserted",
			"trigger", trigger, "err", err)
	}
}

// nftStructureLoop is the periodic re-assertion.
func (a *applier) nftStructureLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reassertNftStructure(ctx, "periodic")
		}
	}
}
