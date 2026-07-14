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

	// nftables — load the full generated ruleset.
	if !a.dryRun {
		if err := a.run(ctx, "nft", "-f", filepath.Join(bundle, "stayconnect.nft")); err != nil {
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

	// Unbound fragment + reload.
	if err := os.WriteFile(a.unboundFrag, netcfg.RenderUnbound(intent), 0o644); err != nil {
		return fmt.Errorf("write unbound: %w", err)
	}
	if !a.dryRun {
		if err := a.run(ctx, "unbound-checkconf"); err == nil {
			_ = a.run(ctx, "unbound-control", "reload")
		}
	}
	a.st.Event(ctx, revID, "unbound", true, nil)
	return nil
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

// ReconcileActiveOnBoot re-applies the active revision's rendered artifacts so
// the live OS matches the DB source of truth after a reboot. This closes the
// gap where nftables loads the static /etc/nftables.conf on boot (the IP-only
// auth set) instead of netd's generated concatenated ruleset. netplan persists
// on its own and Kea reloads its written config, but the generated nftables set
// and Unbound fragment are re-installed here idempotently, and any managed
// bridge/VLAN that netplan did not recreate is brought up surgically. It never
// touches mgmt/WAN/legacy interfaces.
func (a *applier) ReconcileActiveOnBoot(ctx context.Context) {
	id, bundle, err := a.st.CurrentActive(ctx)
	if err != nil || id == "" || bundle == "" {
		return
	}
	if a.dryRun {
		return
	}
	if _, statErr := os.Stat(bundle); statErr != nil {
		return // bundle dir gone; leave the live state untouched
	}
	// Ensure managed bridges/VLANs exist (netplan should recreate them on boot,
	// but a surgical create is idempotent and covers a generate-only apply).
	if intent, err := a.st.LoadIntent(ctx); err == nil {
		for _, n := range a.netdManaged(intent) {
			if n.Enabled && !ifaceExists(n.BridgeName) {
				_ = a.createNetwork(ctx, n)
			}
		}
	}
	// Re-apply the generated nftables ruleset (restores the concatenated set).
	nftFile := filepath.Join(bundle, "stayconnect.nft")
	if _, statErr := os.Stat(nftFile); statErr == nil {
		if err := a.run(ctx, "nft", "-f", nftFile); err != nil {
			a.st.Event(ctx, id, "boot_reconcile", false, map[string]any{"nft": err.Error()})
			return
		}
	}
	// Re-push Kea config from the bundle (idempotent; ensures live == intent).
	if raw, err := os.ReadFile(filepath.Join(bundle, "kea-dhcp4.json")); err == nil {
		_ = a.pushKeaFile(raw)
	}
	// Re-install the Unbound fragment.
	if raw, err := os.ReadFile(filepath.Join(bundle, "stayconnect-guest.conf")); err == nil {
		_ = os.WriteFile(a.unboundFrag, raw, 0o644)
		_ = a.run(ctx, "unbound-control", "reload")
	}
	a.st.Event(ctx, id, "boot_reconcile", true, map[string]any{"bundle": bundle})
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
	// Re-apply the previous good bundle's nft + kea + unbound + netplan file.
	if !a.dryRun {
		_ = a.run(ctx, "nft", "-f", filepath.Join(prevBundle, "stayconnect.nft"))
		if raw, err := os.ReadFile(filepath.Join(prevBundle, "kea-dhcp4.json")); err == nil {
			_ = a.pushKeaFile(raw)
		}
		if raw, err := os.ReadFile(filepath.Join(prevBundle, "stayconnect-guest.conf")); err == nil {
			_ = os.WriteFile(a.unboundFrag, raw, 0o644)
			_ = a.run(ctx, "unbound-control", "reload")
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
