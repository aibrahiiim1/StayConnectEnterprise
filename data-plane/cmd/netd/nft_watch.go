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
	"sync"
	"time"
)

// A re-assertion that keeps failing (a render the kernel refuses, a missing binary) is reported in /v1/health and
// retried with a back-off instead of every tick, so a permanent failure is visible, not only logged (audit Phase 8 L13).
const (
	nftReassertBackoffBase = 10 * time.Second
	nftReassertBackoffMax  = 5 * time.Minute
	// nftReassertReportAfter consecutive failures turn the health report to failing.
	nftReassertReportAfter = 3
)

type nftWatchState struct {
	mu       sync.Mutex
	failures int
	lastErr  string
	since    time.Time
	nextTry  time.Time
}

// due reports whether a periodic re-assertion may run now.
func (w *nftWatchState) due(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !now.Before(w.nextTry)
}

func (w *nftWatchState) record(now time.Time, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		if w.failures >= nftReassertReportAfter {
			slog.Info("netd: the firewall re-assertion succeeded again", "after_failures", w.failures)
		}
		w.failures, w.lastErr, w.since, w.nextTry = 0, "", time.Time{}, time.Time{}
		return
	}
	if w.failures == 0 {
		w.since = now
	}
	w.failures++
	w.lastErr = err.Error()
	delay := nftReassertBackoffBase
	for i := 1; i < w.failures && delay < nftReassertBackoffMax; i++ {
		delay *= 2
	}
	if delay > nftReassertBackoffMax {
		delay = nftReassertBackoffMax
	}
	w.nextTry = now.Add(delay)
}

// status is the /v1/health view.
func (w *nftWatchState) status() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]any{"failing": w.failures >= nftReassertReportAfter, "consecutive_failures": w.failures}
	if w.failures > 0 {
		out["last_error"] = w.lastErr
		out["failing_since"] = w.since.UTC().Format(time.RFC3339)
		out["next_attempt_at"] = w.nextTry.UTC().Format(time.RFC3339)
	}
	return out
}

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
	if trigger == "periodic" && !a.nftWatch.due(time.Now()) {
		return
	}
	// The operation lock closes the window between the check above and the read below: an apply publishes its intent while holding it, so
	// this tick either renders the intent in force BEFORE the apply, or waits for the next tick. It can never install an intent that an apply has
	// published but not yet proven, or one that a failed apply has not yet put back.
	if !a.opMu.TryLock() {
		return
	}
	defer a.opMu.Unlock()
	a.busy.Add(1)
	defer a.busy.Add(-1)
	// Read the intent under the converge lock so a concurrent apply (which publishes its intent first) is never overwritten with the old one.
	a.nftMu.Lock()
	defer a.nftMu.Unlock()
	intent, _, known := a.inForce()
	if !known {
		return
	}
	_, err := a.ensureNftStructureLocked(ctx, intent, trigger)
	a.nftWatch.record(time.Now(), err)
	if err != nil {
		slog.Error("netd: the live ruleset does not match the in-force configuration and could not be re-asserted",
			"trigger", trigger, "err", err, "consecutive_failures", a.nftWatch.status()["consecutive_failures"])
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
