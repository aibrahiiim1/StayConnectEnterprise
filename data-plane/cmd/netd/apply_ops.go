package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// applyBundle brings the live system to the target state:
//  1. surgically create/destroy VLAN sub-interfaces + bridges (additive/rev),
//  2. write + validate the persistence netplan (generate, not apply),
//  3. load the rendered nftables ruleset,
//  4. push the Kea config via the control socket (config-test then config-set),
//  5. install + reload the Unbound fragment.
//
// It never touches the management/WAN/legacy interfaces.
func (a *applier) applyBundle(ctx context.Context, revID string, intent []netcfg.GuestNetwork, bundle string) error {
	managed := a.netdManaged(intent)

	// Desired bridges (enabled managed networks) vs live bridges.
	desired := map[string]netcfg.GuestNetwork{}
	for _, n := range managed {
		if n.Enabled {
			desired[n.BridgeName] = n
		}
	}
	live := a.liveGuestBridges()

	if !a.dryRun {
		// Create missing bridges/VLANs.
		for br, n := range desired {
			if !live[br] {
				if err := a.createNetwork(ctx, n); err != nil {
					return fmt.Errorf("create %s: %w", br, err)
				}
			}
		}
		// Remove bridges no longer desired (managed ones only).
		for br := range live {
			if _, want := desired[br]; !want {
				if err := a.destroyBridge(ctx, br); err != nil {
					return fmt.Errorf("destroy %s: %w", br, err)
				}
			}
		}
	}
	a.st.Event(ctx, revID, "l2l3", true, map[string]any{"bridges": len(desired)})

	// Persistence: write the netplan file + validate (no apply — the live
	// state is already correct via ip commands).
	if err := os.WriteFile(a.netplanFile, netcfg.RenderNetplan(managed), 0o600); err != nil {
		return fmt.Errorf("write netplan: %w", err)
	}
	if !a.dryRun {
		if err := a.run(ctx, "netplan", "generate"); err != nil {
			return fmt.Errorf("netplan generate: %w", err)
		}
	}

	// nftables — converge to the render of THIS intent, through the same path boot reconciliation uses, so an
	// operator applying a network change does not deauthorize the guests currently online.
	if !a.dryRun {
		if _, err := a.ensureNftStructure(ctx, intent, "apply"); err != nil {
			return fmt.Errorf("nft load: %w", err)
		}
	}
	a.st.Event(ctx, revID, "nft", true, nil)

	// Kea — config-set re-detects interfaces (so freshly-created bridges are
	// seen), validates, and applies atomically. We do NOT gate on config-test
	// here because Kea caches its interface list from startup, so config-test
	// cannot see a bridge created moments ago; config-set fails cleanly and
	// atomically (no partial apply) if the config is bad, which is the gate we
	// want. Structural validation already ran via netcfg.ValidateSet.
	dhcp4 := netcfg.RenderKeaDhcp4(intent, a.topo, a.keaLeaseCSV, a.keaSocket)
	if !a.dryRun {
		if err := a.kea.ConfigSet(dhcp4); err != nil {
			return err
		}
	}
	a.st.Event(ctx, revID, "kea", true, nil)

	// Unbound fragment + apply.
	if err := os.WriteFile(a.unboundFrag, netcfg.RenderUnbound(intent), 0o644); err != nil {
		return fmt.Errorf("write unbound: %w", err)
	}
	a.applyUnbound(ctx)
	a.st.Event(ctx, revID, "unbound", true, nil)
	return nil
}

// applyUnbound makes unbound serve the current guest fragment. It RESTARTS
// unbound rather than `unbound-control reload`, because reload re-reads the config
// and flushes the cache but does NOT bind newly-added listen interfaces — so a
// guest network on a NEW gateway IP would get its config written yet never gain a
// DNS listener, leaving guests unable to resolve (DHCP works, but the captive
// portal never triggers because captive-detection DNS fails). Guarded by
// unbound-checkconf so a bad fragment never takes DNS down; falls back to a reload
// if the restart command is unavailable.
func (a *applier) applyUnbound(ctx context.Context) {
	if a.dryRun {
		return
	}
	if err := a.run(ctx, "unbound-checkconf"); err != nil {
		return
	}
	if err := a.run(ctx, "systemctl", "restart", "unbound"); err != nil {
		_ = a.run(ctx, "unbound-control", "reload")
	}
}

// createNetwork brings up one guest network's L2/L3 surgically.
func (a *applier) createNetwork(ctx context.Context, n netcfg.GuestNetwork) error {
	member := n.ParentInterface
	if n.NetworkType == "vlan" {
		member = netcfg.VLANIfaceName(n.ParentInterface, n.VLANID)
		// VLAN sub-interface (idempotent)
		if !ifaceExists(member) {
			if err := a.run(ctx, "ip", "link", "add", "link", n.ParentInterface, "name", member, "type", "vlan", "id", itoa(n.VLANID)); err != nil {
				return err
			}
		}
	}
	// bridge
	if !ifaceExists(n.BridgeName) {
		if err := a.run(ctx, "ip", "link", "add", "name", n.BridgeName, "type", "bridge"); err != nil {
			return err
		}
	}
	// enslave member
	if err := a.run(ctx, "ip", "link", "set", member, "master", n.BridgeName); err != nil {
		return err
	}
	// gateway address (idempotent-ish: ignore "exists")
	cidr := fmt.Sprintf("%s/%d", n.GatewayIP, n.PrefixLen)
	_ = a.run(ctx, "ip", "addr", "add", cidr, "dev", n.BridgeName)
	if err := a.run(ctx, "ip", "link", "set", member, "up"); err != nil {
		return err
	}
	if err := a.run(ctx, "ip", "link", "set", n.BridgeName, "up"); err != nil {
		return err
	}
	// prime tc root on the bridge for download shaping (best-effort).
	_ = a.run(ctx, "tc", "qdisc", "replace", "dev", n.BridgeName, "root", "handle", "1:", "htb", "default", "1")
	return nil
}

// destroyBridge tears down a managed guest bridge and its VLAN sub-interface.
func (a *applier) destroyBridge(ctx context.Context, bridge string) error {
	// find VLAN member (if any) before deleting the bridge
	member := bridgeMember(bridge)
	_ = a.run(ctx, "ip", "link", "del", bridge)
	if member != "" && strings.Contains(member, ".") {
		_ = a.run(ctx, "ip", "link", "del", member)
	}
	return nil
}

// ReconcileActiveOnBoot brings the live OS back in line with the DB source of truth after a restart or reboot.
// This closes the gap where nftables loads the static /etc/nftables.conf on boot (the IP-only auth set) instead
// of netd's generated concatenated ruleset. netplan persists on its own and Kea reloads its written config; the
// nftables ruleset and Unbound fragment are re-installed here, and any managed bridge/VLAN that netplan did not
// recreate is brought up surgically. It never touches mgmt/WAN/legacy interfaces.
//
// The nftables half is reconciled against a FRESH RENDER, never against the stored bundle — see the commentary
// in nft_reconcile.go for why replaying the bundle silently deleted structure the current software requires.
// When the live ruleset already matches this binary's render, no nft command is issued at all.
func (a *applier) ReconcileActiveOnBoot(ctx context.Context) {
	id, bundle, err := a.st.CurrentActive(ctx)
	if err != nil || id == "" {
		return
	}
	if a.dryRun {
		return
	}
	intent, intentErr := a.st.LoadIntent(ctx)
	if intentErr != nil {
		// Without intent there is no defensible desired structure. Doing nothing keeps whatever is live —
		// which is strictly safer than reconstructing from a stale artifact.
		a.st.Event(ctx, id, "boot_reconcile", false, map[string]any{"intent": intentErr.Error()})
		return
	}
	// Ensure managed bridges/VLANs exist (netplan should recreate them on boot,
	// but a surgical create is idempotent and covers a generate-only apply).
	for _, n := range a.netdManaged(intent) {
		if n.Enabled && !ifaceExists(n.BridgeName) {
			_ = a.createNetwork(ctx, n)
		}
	}
	// nftables: converge to what THIS binary renders.
	res, nftErr := a.ensureNftStructure(ctx, intent, "boot_reconcile")
	if nftErr != nil {
		a.st.Event(ctx, id, "boot_reconcile", false, map[string]any{"nft": nftErr.Error()})
		return
	}
	// Keep the stored bundle honest: once the live ruleset has been rebuilt by the current renderer, the
	// artifact on disk should say the same thing rather than remain a record of an older structure.
	if res.Changed && bundle != "" {
		if _, statErr := os.Stat(bundle); statErr == nil {
			_ = os.WriteFile(filepath.Join(bundle, "stayconnect.nft"), netcfg.RenderNftables(intent, a.topo), 0o640)
		}
	}
	if bundle != "" {
		if _, statErr := os.Stat(bundle); statErr == nil {
			// Re-push Kea config from the bundle (idempotent; ensures live == intent).
			if raw, err := os.ReadFile(filepath.Join(bundle, "kea-dhcp4.json")); err == nil {
				_ = a.pushKeaFile(raw)
			}
			// Re-install the Unbound fragment.
			if raw, err := os.ReadFile(filepath.Join(bundle, "stayconnect-guest.conf")); err == nil {
				_ = os.WriteFile(a.unboundFrag, raw, 0o644)
				a.applyUnbound(ctx)
			}
		}
	}
	a.st.Event(ctx, id, "boot_reconcile", true, map[string]any{
		"bundle": bundle, "nft_changed": res.Changed, "carried_elements": res.Carried,
		"desired_fp": res.DesiredFP, "live_fp_before": res.LiveFP,
	})
}

// rollback restores the previous active revision (its bundle re-applied) or, if
// none, removes everything netd added. Management connectivity is preserved
// throughout (mgmt/legacy interfaces are never touched here).
func (a *applier) rollback(ctx context.Context, failedID, reason string) {
	a.st.Event(ctx, failedID, "rollback", true, map[string]any{"reason": reason})
	prevBundle, _ := a.st.ActiveBundlePath(ctx, failedID)

	if prevBundle == "" {
		// No prior good revision: tear down everything managed and clear.
		for br := range a.liveGuestBridges() {
			_ = a.destroyBridge(ctx, br)
		}
		_ = os.Remove(a.netplanFile)
		_ = os.Remove(a.unboundFrag)
		_ = a.st.MarkRolledBack(ctx, failedID, reason)
		return
	}
	// Reconcile bridges to the previous good revision's managed set: destroy any
	// managed guest bridge that the failed apply created but the previous good
	// revision does not include. The legacy/mgmt/WAN interfaces are never in
	// liveGuestBridges(), so they cannot be touched here.
	prevBridges := a.bridgesInBundle(prevBundle)
	if !a.dryRun {
		for br := range a.liveGuestBridges() {
			if !prevBridges[br] {
				_ = a.destroyBridge(ctx, br)
			}
		}
	}
	// Restore the previous good revision's nft structure by RENDERING ITS STORED INTENT with the current
	// renderer, falling back to its bundle file only if the intent cannot be read. Replaying the file
	// unconditionally would reinstate whatever structure the binary of that era produced — the same staleness
	// that made a routine restart delete phase3_auth_ipv4.
	if !a.dryRun {
		if prevIntent, ierr := a.st.ActiveIntent(ctx, failedID); ierr == nil && prevIntent != nil {
			if _, nerr := a.ensureNftStructure(ctx, prevIntent, "rollback"); nerr != nil {
				_ = a.run(ctx, "nft", "-f", filepath.Join(prevBundle, "stayconnect.nft"))
			}
		} else {
			_ = a.run(ctx, "nft", "-f", filepath.Join(prevBundle, "stayconnect.nft"))
		}
		if raw, err := os.ReadFile(filepath.Join(prevBundle, "kea-dhcp4.json")); err == nil {
			_ = a.pushKeaFile(raw)
		}
		if raw, err := os.ReadFile(filepath.Join(prevBundle, "stayconnect-guest.conf")); err == nil {
			_ = os.WriteFile(a.unboundFrag, raw, 0o644)
			a.applyUnbound(ctx)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(prevBundle, "50-stayconnect-guest.yaml")); err == nil {
		_ = os.WriteFile(a.netplanFile, raw, 0o600)
	}
	_ = a.st.MarkRolledBack(ctx, failedID, reason)
}

func (a *applier) pushKeaFile(raw []byte) error {
	// raw is {"Dhcp4": {...}} — extract and config-set (re-detects interfaces).
	dhcp4, err := extractDhcp4(raw)
	if err != nil {
		return err
	}
	return a.kea.ConfigSet(dhcp4)
}

// healthChecks runs post-apply verification. mgmt_reachable and kea_running are
// critical (failure => rollback); the rest are informational but recorded.
func (a *applier) healthChecks(ctx context.Context, revID string, intent []netcfg.GuestNetwork) []healthResult {
	var out []healthResult
	add := func(name string, ok bool, detail string) {
		out = append(out, healthResult{Name: name, OK: ok, Detail: detail})
		a.st.Health(ctx, revID, name, ok, detail)
	}

	// mgmt_reachable: the management interface must still carry its address.
	mgmtOK := a.dryRun || ifaceHasAnyIP(a.topo.MgmtInterface)
	add("mgmt_reachable", mgmtOK, a.topo.MgmtInterface)

	// gateway_up: each enabled managed bridge must have its gateway IP.
	gwOK := true
	for _, n := range a.netdManaged(intent) {
		if !n.Enabled {
			continue
		}
		if !a.dryRun && !ifaceHasIP(n.BridgeName, n.GatewayIP) {
			gwOK = false
			add("gateway_up:"+n.BridgeName, false, "missing "+n.GatewayIP)
		}
	}
	if gwOK {
		add("gateway_up", true, "")
	}

	// kea_running.
	keaOK := a.dryRun || a.kea.Healthy()
	add("kea_running", keaOK, "")

	// portal_listen: portald must still be listening on the HTTP portal port.
	portalOK := a.dryRun || tcpListening(a.topo.PortalHTTPPort)
	add("portal_listen", portalOK, itoa(a.topo.PortalHTTPPort))

	return out
}

// --- low-level interface helpers (read-only) ---

// bridgesInBundle reads a revision bundle's netplan to recover the set of
// managed guest bridge names it declared (used to reconcile on rollback).
func (a *applier) bridgesInBundle(bundle string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(filepath.Join(bundle, "50-stayconnect-guest.yaml"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		// bridge entries render as "    <name>:" under "  bridges:"
		if strings.HasPrefix(t, "br-g") && strings.HasSuffix(t, ":") {
			out[strings.TrimSuffix(t, ":")] = true
		}
	}
	return out
}

func (a *applier) liveGuestBridges() map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "br-g") && name != a.legacyBridge {
			out[name] = true
		}
	}
	return out
}

func ifaceExists(name string) bool {
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

func ifaceHasIP(name, ip string) bool {
	out, err := exec.Command("ip", "-o", "-4", "addr", "show", "dev", name).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), " "+ip+"/")
}

func ifaceHasAnyIP(name string) bool {
	out, err := exec.Command("ip", "-o", "-4", "addr", "show", "dev", name).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "inet ")
}

func bridgeMember(bridge string) string {
	entries, _ := os.ReadDir("/sys/class/net/" + bridge + "/brif")
	if len(entries) > 0 {
		return entries[0].Name()
	}
	return ""
}

func tcpListening(port int) bool {
	// Cheap check: dial loopback.
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
