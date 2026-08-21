package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
		// FIRST CONFIGURATION HAS TO START KEA, NOT TALK TO IT.
		//
		// config-set reaches Kea through its control socket, and that socket exists only while Kea is
		// running. On a factory-clean appliance Kea has never run — provisioning installs it stopped and
		// disabled on purpose, because it binds the guest bridge and that bridge does not exist yet. So
		// the first apply deadlocked: Kea could not start without configuration, and netd would not
		// configure it without the socket. Every first guest network failed with
		// "Kea has no control socket at /run/kea/kea4-ctrl-socket", and no amount of retrying changed it.
		//
		// The bridge exists by now (l2l3 ran above), so this is the moment Kea can legitimately start.
		if err := a.bootstrapKeaIfStopped(ctx, revID, intent); err != nil {
			return err
		}
		if err := a.kea.ConfigSet(dhcp4); err != nil {
			return err
		}
		// AND CHECK IT IS ACTUALLY SERVING. Accepting a config is not the same as listening.
		if err := a.ensureKeaServing(ctx, intent); err != nil {
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
	if a.dryRun {
		return
	}
	// THE CONFIRMED ACTIVE REVISION'S OWN INTENT SNAPSHOT — never the live guest_networks rows, which the
	// Hotel-Admin UI edits directly and which may hold a draft nobody has applied or confirmed. See
	// store.CurrentActiveIntent for why reconciling from those rows would let an unapplied edit take effect on
	// the next reboot with no apply record, no health check and no watchdog.
	id, bundle, intent, err := a.currentActiveIntent(ctx)
	if err != nil {
		if !errors.Is(err, ErrNoConfirmedActiveRevision) {
			// An active revision exists but its intent could not be read. Reconstructing from anything else
			// would be a guess about what this appliance is supposed to be forwarding.
			a.event(ctx, id, "boot_reconcile", false, map[string]any{"intent": err.Error()})
			slog.Error("netd boot reconcile: active revision intent unusable; live state left untouched", "err", err)
		}
		return
	}
	if id == "" {
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
		a.event(ctx, id, "boot_reconcile", false, map[string]any{"nft": nftErr.Error()})
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
	a.event(ctx, id, "boot_reconcile", true, map[string]any{
		"bundle": bundle, "nft_changed": res.Changed, "carried_elements": res.Carried,
		"desired_fp": res.DesiredFP, "live_fp_before": res.LiveFP,
	})
}

// rollback restores the previous active revision (its bundle re-applied) or, if
// none, removes everything netd added. Management connectivity is preserved
// throughout (mgmt/legacy interfaces are never touched here).
func (a *applier) rollback(ctx context.Context, failedID, reason string) {
	a.event(ctx, failedID, "rollback", true, map[string]any{"reason": reason})
	prevBundle, _ := a.previousBundle(ctx, failedID)

	if prevBundle == "" {
		// No prior good revision: tear down everything managed and clear.
		for br := range a.liveGuestBridges() {
			_ = a.destroyBridge(ctx, br)
		}
		_ = os.Remove(a.netplanFile)
		_ = os.Remove(a.unboundFrag)
		// AND PUT KEA BACK. This branch is the first-ever apply, which is exactly the case that may have
		// started Kea for the first time. Without this the appliance kept a running, enabled DHCP server
		// configured for a guest network that had just been torn down — not the factory-clean state it
		// began in, and not a state anyone chose.
		if !a.dryRun {
			a.restoreKeaPreBootstrap(ctx)
		}
		_ = a.markRolledBack(ctx, failedID, reason)
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
	// renderer.
	//
	// THERE IS NO FALLBACK TO THE STORED FILE, DELIBERATELY. An earlier version dropped back to
	// `nft -f <prevBundle>/stayconnect.nft` whenever the safe path could not be completed, which meant the
	// worst moment — a failed apply, on a live appliance, with the operator already in trouble — was the one
	// moment the code chose to execute a full-table replacement rendered by some earlier binary. That file
	// begins with `delete table inet stayconnect`: it deletes every authorization set and every structure the
	// current software requires, and it is exactly the artifact that caused the Live Increment-9 blocker.
	//
	// A rollback that cannot be done safely must stay unfinished and say so. An unfinished rollback leaves the
	// operator with the live ruleset they already had and a recorded blocker; a "successful" one that ran the
	// legacy path leaves them with a silently deauthorized property.
	if !a.dryRun {
		nftRolledBack := false
		prevIntent, ierr := a.previousActiveIntent(ctx, failedID)
		switch {
		case ierr != nil:
			a.event(ctx, failedID, "rollback_nft", false, map[string]any{
				"blocker": "the previous active revision's intent could not be read: " + ierr.Error(),
				"action":  "nft structure was NOT rolled back; the live ruleset is unchanged and needs operator attention",
			})
			slog.Error("netd rollback: previous intent unreadable; nft structure left as-is", "err", ierr)
		case prevIntent == nil:
			a.event(ctx, failedID, "rollback_nft", false, map[string]any{
				"blocker": "there is no previous confirmed revision to render",
				"action":  "nft structure was NOT rolled back; the live ruleset is unchanged and needs operator attention",
			})
			slog.Error("netd rollback: no previous confirmed revision; nft structure left as-is")
		default:
			if _, nerr := a.ensureNftStructure(ctx, prevIntent, "rollback"); nerr != nil {
				a.event(ctx, failedID, "rollback_nft", false, map[string]any{
					"blocker": "safe reconciliation to the previous revision failed: " + nerr.Error(),
					"action":  "nft structure was NOT rolled back; the live ruleset is unchanged and needs operator attention",
				})
				slog.Error("netd rollback: safe nft reconciliation failed; NOT falling back to the stored ruleset file", "err", nerr)
			} else {
				nftRolledBack = true
			}
		}
		a.event(ctx, failedID, "rollback_nft", nftRolledBack, nil)
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
	_ = a.markRolledBack(ctx, failedID, reason)
}

// keaListeningOn reports whether a UDP socket is bound to ip:67 (or to 0.0.0.0:67).
//
// It reads /proc/net/udp rather than shelling out to `ss`, so it adds no binary dependency and cannot be
// defeated by output formatting. Addresses there are little-endian hex.
func keaListeningOn(ip string) bool {
	raw, err := os.ReadFile("/proc/net/udp")
	if err != nil {
		return false
	}
	want := hexLE(ip)
	anyAddr := "00000000"
	for _, line := range strings.Split(string(raw), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		parts := strings.Split(f[1], ":") // local_address is HEXIP:HEXPORT
		if len(parts) != 2 || !strings.EqualFold(parts[1], "0043") {
			continue // 0x43 == 67
		}
		if strings.EqualFold(parts[0], want) || strings.EqualFold(parts[0], anyAddr) {
			return true
		}
	}
	return false
}

// hexLE renders a dotted-quad as the little-endian hex /proc/net/udp uses.
func hexLE(ip string) string {
	var b [4]int
	if _, err := fmt.Sscanf(ip, "%d.%d.%d.%d", &b[0], &b[1], &b[2], &b[3]); err != nil {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X%02X", b[3], b[2], b[1], b[0])
}

// ensureKeaServing verifies Kea is LISTENING for each guest gateway, and restarts it if it is not.
//
// THE FAILURE THIS CATCHES. config-set does not reliably rebind Kea's DHCP sockets when the interface set
// changes underneath it. Roll a guest network back and re-apply it: the bridge is destroyed and recreated
// with a new ifindex, Kea accepts the new configuration, reports status-get healthy — and never opens a
// socket. DHCP is dead while every indicator says it is fine. A client sends DHCPDISCOVER and hears
// nothing, which looks like a cabling fault, not a control-plane one.
//
// status-get proves the control channel works, not that guests can get an address. This asserts the thing
// that actually matters, and repairs it the one way that reliably works — a restart, which rebinds from a
// config already written to disk.
func (a *applier) ensureKeaServing(ctx context.Context, intent []netcfg.GuestNetwork) error {
	missing := a.keaGatewaysNotServed(intent)
	if len(missing) == 0 {
		return nil
	}
	slog.Warn("kea: accepted the configuration but is not listening; restarting to rebind",
		"gateways", missing, "unit", a.keaUnit)
	if err := a.run(ctx, "systemctl", "restart", a.keaUnit); err != nil {
		return fmt.Errorf("restart %s to bind DHCP: %w", a.keaUnit, err)
	}
	wait := a.keaStartTimeout
	if wait <= 0 {
		wait = 20 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		if still := a.keaGatewaysNotServed(intent); len(still) == 0 {
			slog.Info("kea: DHCP listening on every guest gateway")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("kea is not listening on %v after a restart — guests would get no address "+
				"(check: journalctl -u %s)", a.keaGatewaysNotServed(intent), a.keaUnit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// keaGatewaysNotServed lists the enabled guest gateways Kea has no socket for.
func (a *applier) keaGatewaysNotServed(intent []netcfg.GuestNetwork) []string {
	var missing []string
	for _, n := range a.netdManaged(intent) {
		if !n.Enabled || n.DHCPMode != netcfg.DHCPLocal || n.GatewayIP == "" {
			continue // only networks this appliance serves DHCP for
		}
		if !keaListeningOn(n.GatewayIP) {
			missing = append(missing, n.GatewayIP)
		}
	}
	return missing
}

// writeFileAtomic writes via a temp file in the same directory and renames, so a reader (or a Kea that is
// starting) never observes a half-written config.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// keaPreBootstrap is Kea's state BEFORE netd started it for the first time, so a first apply that never
// gets confirmed can put the appliance back exactly as it was.
//
// It lives on disk, not in memory. The rollback that needs it is often triggered by the confirmation
// watchdog, and that watchdog also runs at boot to recover from a crash during pending_confirmation — by
// which time the process that did the bootstrap is gone. A marker held in a variable would be lost in
// precisely the case it exists for.
type keaPreBootstrap struct {
	RevisionID   string    `json:"revision_id"`
	CapturedAt   time.Time `json:"captured_at"`
	UnitEnabled  bool      `json:"unit_enabled"`
	UnitActive   bool      `json:"unit_active"`
	ConfigExists bool      `json:"config_exists"`
}

func (a *applier) keaPreBootstrapPath() string {
	return filepath.Join(a.generatedDir, "kea-pre-bootstrap.json")
}
func (a *applier) keaPreBootstrapConf() string {
	return filepath.Join(a.generatedDir, "kea-pre-bootstrap.conf")
}

// captureKeaPreBootstrap records what to return to, and copies any existing config aside. It runs before a
// single byte of Kea's state is changed.
func (a *applier) captureKeaPreBootstrap(ctx context.Context, revID string) error {
	st := keaPreBootstrap{RevisionID: revID, CapturedAt: time.Now().UTC()}

	// `systemctl is-enabled/is-active` exit non-zero for disabled/inactive units, which is an answer, not
	// a failure — so the output is what matters, not the error.
	if out, _ := a.output(ctx, "systemctl", "is-enabled", a.keaUnit); strings.TrimSpace(string(out)) == "enabled" {
		st.UnitEnabled = true
	}
	if out, _ := a.output(ctx, "systemctl", "is-active", a.keaUnit); strings.TrimSpace(string(out)) == "active" {
		st.UnitActive = true
	}
	if raw, err := os.ReadFile(a.keaConfFile); err == nil {
		st.ConfigExists = true
		if err := writeFileAtomic(a.keaPreBootstrapConf(), raw, 0o644); err != nil {
			return fmt.Errorf("back up existing kea config: %w", err)
		}
	} else {
		_ = os.Remove(a.keaPreBootstrapConf()) // no stale backup from an earlier attempt
	}

	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.generatedDir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(a.keaPreBootstrapPath(), b, 0o644); err != nil {
		return fmt.Errorf("record kea pre-bootstrap state: %w", err)
	}
	slog.Info("kea: recorded pre-bootstrap state for rollback",
		"enabled", st.UnitEnabled, "active", st.UnitActive, "config_existed", st.ConfigExists)
	return nil
}

// restoreKeaPreBootstrap puts Kea back exactly as it was found, and is a no-op when netd never started it.
//
// This is the half that was missing. The first apply has no previous confirmed revision to fall back to,
// so rollback tore down the bridge, the netplan and the unbound fragment — and left Kea running, enabled
// and serving DHCP for a guest network that no longer exists. A factory-clean appliance has to come back
// factory-clean, or the next attempt starts from a state nobody chose.
func (a *applier) restoreKeaPreBootstrap(ctx context.Context) {
	raw, err := os.ReadFile(a.keaPreBootstrapPath())
	if err != nil {
		return // netd did not bootstrap Kea; its state is not ours to change
	}
	var st keaPreBootstrap
	if json.Unmarshal(raw, &st) != nil {
		slog.Warn("kea: pre-bootstrap marker unreadable; leaving Kea as-is for operator attention")
		return
	}

	// Order matters: stop before restoring the config, so Kea is never running against a file that is
	// half-way between two states.
	if !st.UnitActive {
		if err := a.run(ctx, "systemctl", "stop", a.keaUnit); err != nil {
			slog.Warn("kea: could not stop during rollback", "unit", a.keaUnit, "err", err)
		}
	}
	if !st.UnitEnabled {
		if err := a.run(ctx, "systemctl", "disable", a.keaUnit); err != nil {
			slog.Warn("kea: could not disable during rollback", "unit", a.keaUnit, "err", err)
		}
	}
	if st.ConfigExists {
		if prev, rerr := os.ReadFile(a.keaPreBootstrapConf()); rerr == nil {
			if werr := writeFileAtomic(a.keaConfFile, prev, 0o644); werr != nil {
				slog.Warn("kea: could not restore the previous config", "err", werr)
			}
		}
	} else {
		// There was no config before this apply, so leaving ours behind would be leaving guest-network
		// configuration active on an appliance that has none.
		_ = os.Remove(a.keaConfFile)
	}
	slog.Info("kea: restored pre-bootstrap state",
		"enabled", st.UnitEnabled, "active", st.UnitActive, "config_restored", st.ConfigExists)
	a.clearKeaPreBootstrap()
}

// clearKeaPreBootstrap discards the saved state once it can no longer be needed — the revision is
// confirmed, so Kea stays enabled and running, which is the point of confirming it.
func (a *applier) clearKeaPreBootstrap() {
	_ = os.Remove(a.keaPreBootstrapPath())
	_ = os.Remove(a.keaPreBootstrapConf())
}

// bootstrapKeaIfStopped brings Kea up the first time guest networking is configured.
//
// It is a no-op whenever Kea is already answering, so the steady state is unchanged: every later apply
// goes through the control socket exactly as before. This runs only to break the one deadlock — a service
// that cannot start without configuration it can only receive while running.
//
// Writing the file AND enabling the unit are both required. The file is what lets Kea start at all; the
// enable is what makes it come back after a reboot, since provisioning deliberately left it disabled.
// Both are recorded first, so both can be undone.
func (a *applier) bootstrapKeaIfStopped(ctx context.Context, revID string, intent []netcfg.GuestNetwork) error {
	if a.kea.Healthy() {
		return nil // already running — nothing to bootstrap
	}
	// CAPTURE BEFORE CHANGING ANYTHING. If this fails we have not touched Kea yet, so refusing here leaves
	// the appliance exactly as it was rather than starting something we could not undo.
	if err := a.captureKeaPreBootstrap(ctx, revID); err != nil {
		return err
	}
	raw, err := netcfg.RenderKeaFile(intent, a.topo, a.keaLeaseCSV, a.keaSocket)
	if err != nil {
		return fmt.Errorf("render kea config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.keaConfFile), 0o755); err != nil {
		return fmt.Errorf("kea config dir: %w", err)
	}
	if err := writeFileAtomic(a.keaConfFile, raw, 0o644); err != nil {
		return fmt.Errorf("write kea config: %w", err)
	}
	// The socket directory is Kea's, and a missing one stops it before it can report why.
	if err := os.MkdirAll(filepath.Dir(a.keaSocket), 0o755); err != nil {
		return fmt.Errorf("kea socket dir: %w", err)
	}
	slog.Info("kea: first guest network — starting DHCP", "config", a.keaConfFile, "unit", a.keaUnit)
	// a.run, NOT a.runFn. runFn is the test seam and is nil in production, so calling it directly panicked
	// with a nil pointer dereference the first time this ran on the appliance — inside an HTTP handler,
	// where chi's Recoverer turned it into a 500 and the apply never reached its own error path. The
	// revision was left stuck in "applying" with no kea event and no rollback recorded.
	if err := a.run(ctx, "systemctl", "enable", "--now", a.keaUnit); err != nil {
		return fmt.Errorf("start %s: %w", a.keaUnit, err)
	}
	// Wait for the control socket. Kea creates it once it has bound its interfaces, so this is also the
	// signal that it accepted the bridge — and without it the config-set below would fail for a reason
	// that reads like the deadlock we just removed.
	wait := a.keaStartTimeout
	if wait <= 0 {
		wait = 20 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		if a.kea.Healthy() {
			slog.Info("kea: DHCP is running", "socket", a.keaSocket)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("kea did not come up within %s after starting %s (check: journalctl -u %s)",
				wait, a.keaUnit, a.keaUnit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
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

	// kea_running — LISTENING, not merely answering its control socket.
	//
	// This used to be a.kea.Healthy(), which is status-get over the control channel. That passed while Kea
	// held no DHCP socket at all, so an apply reported every health check green on a network where no guest
	// could ever get an address. A health check that cannot fail when the service is not doing its job is
	// not a health check.
	keaOK := a.dryRun
	keaDetail := ""
	if !a.dryRun {
		if missing := a.keaGatewaysNotServed(intent); len(missing) == 0 {
			keaOK = a.kea.Healthy()
			if !keaOK {
				keaDetail = "control socket not answering"
			}
		} else {
			keaDetail = "not listening on " + strings.Join(missing, ", ")
		}
	}
	add("kea_running", keaOK, keaDetail)

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
