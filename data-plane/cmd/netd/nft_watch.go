package main

// The ruleset is re-asserted while netd runs, not only at start: `systemctl restart nftables` (its conf begins
// with `flush ruleset`), an operator's `nft -f` or a package upgrade would otherwise leave netd writing
// authorizations into a set that no longer exists. The engine (internal/nftconverge) issues no mutation when the
// live fingerprint matches (one `nft list set` per tick); otherwise it installs the render atomically, carrying
// live authorizations across. The same pass carries the management pin (NETD_MGMT_ADDR=auto), so a WAN address
// change is followed within one tick.

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"
)

// mgmtAddrAuto pins the console's port 443 to the management interface's current IPv4, re-read at every render;
// a literal address would lock the console out when the WAN address changes.
const mgmtAddrAuto = "auto"

// resolveMgmtAddr: an "auto" pin whose interface has no IPv4 yet renders no pin (interface-wide accept); fail
// towards reachability so the operator can still reach the appliance.
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

// reassertNftStructure stands aside while an apply, rollback or boot reconcile runs, and on a factory-clean appliance.
func (a *applier) reassertNftStructure(ctx context.Context, trigger string) {
	if a.dryRun || a.busy.Load() > 0 {
		return
	}
	a.busy.Add(1)
	defer a.busy.Add(-1)
	// Read the intent under the converge lock so a concurrent apply (which publishes its intent first) is never overwritten with the old one.
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
