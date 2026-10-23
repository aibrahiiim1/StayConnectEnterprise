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

// applyBundle brings the live system to the target state: create/destroy VLAN sub-interfaces and bridges surgically, write and validate the persistence
// netplan (generate, not apply), load nftables, push the Kea config via the control socket, install and reload Unbound.
// It never touches management/WAN/legacy interfaces.
func (a *applier) applyBundle(ctx context.Context, revID string, intent []netcfg.GuestNetwork, bundle string) error {
	managed := a.netdManaged(intent)

	desired := map[string]netcfg.GuestNetwork{}
	for _, n := range managed {
		if n.Enabled {
			desired[n.BridgeName] = n
		}
	}
	live, err := a.liveGuestBridges()
	if err != nil {
		return err
	}

	// A bridge that already exists must still be what the intent says: applyBundle only calls createNetwork for MISSING bridges, so a changed VLAN id or parent port
	// under an unchanged bridge name would reach the database, bundle and Kea but never the wire. edged refuses such edits (immutable_topology); this guards other writers.
	// Refusing is safe because a replacement always builds a NEW bridge name.
	if !a.dryRun {
		for br, n := range desired {
			if !live[br] {
				continue
			}
			wantMember := n.ParentInterface
			if n.NetworkType == "vlan" {
				wantMember = netcfg.VLANIfaceName(n.ParentInterface, n.VLANID)
			}
			if got := a.bridgeMemberOf(br); got != "" && got != wantMember {
				return fmt.Errorf("bridge %s is attached to %s but this configuration says %s: "+
					"a client network's VLAN or port cannot be changed underneath its bridge, "+
					"and the live network has not been touched", br, got, wantMember)
			}
		}
	}

	if !a.dryRun {
		for br, n := range desired {
			if !live[br] {
				if err := a.createNetwork(ctx, n); err != nil {
					return fmt.Errorf("create %s: %w", br, err)
				}
			}
		}
		for br := range live {
			if _, want := desired[br]; !want {
				if err := a.destroyBridge(ctx, br); err != nil {
					return fmt.Errorf("destroy %s: %w", br, err)
				}
			}
		}
		// ...and converge the addressing of bridges that were already there, which createNetwork never touches.
		if err := a.reconcileAddresses(ctx, revID, intent); err != nil {
			return err
		}
	}
	a.st.Event(ctx, revID, "l2l3", true, map[string]any{"bridges": len(desired)})

	// Persistence: write the netplan file and validate it (no apply; the live state is already correct via ip commands).
	// A dry run writes nothing: these files decide what comes up after the next reboot.
	if !a.dryRun {
		if err := os.WriteFile(a.netplanFile, netcfg.RenderNetplan(managed), 0o600); err != nil {
			return fmt.Errorf("write netplan: %w", err)
		}
		if err := a.run(ctx, "netplan", "generate"); err != nil {
			return fmt.Errorf("netplan generate: %w", err)
		}
	}

	// nftables: converge to the render of THIS intent through the boot-reconciliation path, so applying a change does not deauthorize online guests.
	if !a.dryRun {
		if _, err := a.ensureNftStructure(ctx, intent, "apply"); err != nil {
			return fmt.Errorf("nft load: %w", err)
		}
	}
	a.st.Event(ctx, revID, "nft", true, nil)

	// The portal address comes after the firewall, so the appliance never answers for it under a ruleset that does not know it.
	// A changed address replaces the old one; an empty one removes the interface.
	_, vip, _ := a.inForce()
	if changed, err := a.ensurePortalIface(ctx, vip); err != nil {
		return fmt.Errorf("portal address: %w", err)
	} else if changed {
		a.st.Event(ctx, revID, "portal_address", true, map[string]any{"iface": netcfg.PortalIface, "address": vip})
	}

	// Kea: config-set re-detects interfaces, validates and applies atomically. config-test is not used as a gate because Kea caches its interface list
	// from startup and cannot see a just-created bridge; config-set fails cleanly if the config is bad. Rendered by renderKea, as the runtime portal refresh does.
	dhcp4 := a.renderKea(intent)

	// A bridge that is not IFF_RUNNING yet cannot be bound: Kea would keep running and answer status-get while holding no DHCP socket.
	var wantBridges []string
	for _, n := range managed {
		if n.Enabled && n.BridgeName != "" {
			wantBridges = append(wantBridges, n.BridgeName)
		}
	}
	a.waitBridgesRunning(ctx, wantBridges)

	if !a.dryRun {
		// First guest network on a factory-clean appliance: Kea is installed stopped and disabled and can only be configured through a socket that exists while it runs. No-op once Kea answers.
		// The control socket takes the inner Dhcp4 object; the config FILE needs the whole document, as extractDhcp4 expects to read it back.
		full, ferr := json.MarshalIndent(map[string]any{"Dhcp4": dhcp4}, "", "  ")
		if ferr != nil {
			return fmt.Errorf("render kea config file: %w", ferr)
		}
		if err := a.bootstrapKeaIfStopped(ctx, revID, full); err != nil {
			return err
		}
		if err := a.pushKea(dhcp4); err != nil {
			return err
		}
		// Assert Kea actually bound its sockets: config-set does not reliably rebind when the interface set changes (a re-created bridge gets a new ifindex), and a restart does.
		// Only locally-served, enabled networks are expected to hold a socket; a relayed network has none.
		if missing := a.gatewaysNotListening(managed); len(missing) > 0 {
			slog.Warn("kea accepted the configuration but is not listening on every guest gateway; restarting to rebind",
				"gateways", strings.Join(missing, ","))
			if err := a.run(ctx, "systemctl", "restart", "kea-dhcp4-server"); err != nil {
				return fmt.Errorf("kea is not listening on %s and the rebind restart failed: %w",
					strings.Join(missing, ","), err)
			}
			if still := a.gatewaysNotListening(managed); len(still) > 0 {
				return fmt.Errorf("kea is still not listening on %s after a restart; guests on that network "+
					"would get no address", strings.Join(still, ","))
			}
		}
	}
	a.st.Event(ctx, revID, "kea", true, nil)

	// Unbound fragment + apply. Not written on a dry run, for the reason given at the netplan write above.
	if !a.dryRun {
		if err := os.WriteFile(a.unboundFrag, netcfg.RenderUnbound(intent), 0o644); err != nil {
			return fmt.Errorf("write unbound: %w", err)
		}
	}
	// The portal-names fragment follows the portal address of this apply. The restart below picks it up, and
	// unbound-checkconf there validates both fragments together.
	if _, _, err := a.writeUnboundPortal(vip, a.renderTopo().PortalTLS); err != nil {
		return fmt.Errorf("portal resolver fragment: %w", err)
	}
	if err := a.applyUnbound(ctx); err != nil {
		a.st.Event(ctx, revID, "unbound", false, map[string]any{"error": err.Error()})
		return fmt.Errorf("guest resolver: %w", err)
	}
	a.st.Event(ctx, revID, "unbound", true, nil)
	return nil
}

// applyUnbound restarts unbound rather than `unbound-control reload`, because reload does not bind newly-added listen interfaces
// (a network on a new gateway IP would never gain a DNS listener and captive-portal detection would fail).
// Guarded by unbound-checkconf so a bad fragment never takes DNS down; falls back to a reload if restart is unavailable.
// Every step's failure is returned: no health check looks at DNS, so a swallowed failure would give a green apply with guests unable to resolve.
func (a *applier) applyUnbound(ctx context.Context) error {
	if a.dryRun {
		return nil
	}
	if err := a.run(ctx, "unbound-checkconf"); err != nil {
		return fmt.Errorf("the guest resolver configuration was refused: %w", err)
	}
	if err := a.run(ctx, "systemctl", "restart", "unbound"); err != nil {
		// A reload is the gentler fallback, and its failure is the real answer.
		if rerr := a.run(ctx, "unbound-control", "reload"); rerr != nil {
			return fmt.Errorf("the guest resolver would neither restart (%v) nor reload: %w", err, rerr)
		}
	}
	return nil
}

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
	if !ifaceExists(n.BridgeName) {
		if err := a.run(ctx, "ip", "link", "add", "name", n.BridgeName, "type", "bridge"); err != nil {
			return err
		}
	}
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

// reconcileAddress makes an EXISTING managed bridge carry exactly the gateway address its intent asks for, and reports whether it changed anything.
// createNetwork (where `ip addr add` lives) only runs for missing bridges, so an edited subnet or gateway would otherwise never reach the live bridge.
// Only bridges in the desired managed set are touched; liveGuestBridges() returns br-g* only.
func (a *applier) reconcileAddress(ctx context.Context, n netcfg.GuestNetwork) (bool, error) {
	want := fmt.Sprintf("%s/%d", n.GatewayIP, n.PrefixLen)
	have := a.ifaceAddrsOf(n.BridgeName)
	changed := false
	found := false
	for _, c := range have {
		if c == want {
			found = true
			continue
		}
		changed = true
		// A failed removal is a failed apply: otherwise the bridge keeps both gateways (gateway_up only checks the wanted one is PRESENT) and old leases keep routing through it.
		if err := a.run(ctx, "ip", "addr", "del", c, "dev", n.BridgeName); err != nil {
			return true, fmt.Errorf("remove stale address %s from %s: %w", c, n.BridgeName, err)
		}
	}
	if !found {
		changed = true
		if err := a.run(ctx, "ip", "addr", "add", want, "dev", n.BridgeName); err != nil {
			return true, fmt.Errorf("address %s on %s: %w", want, n.BridgeName, err)
		}
	}
	return changed, nil
}

// reconcileAddresses converges every enabled managed bridge that already exists onto its intent's addressing.
func (a *applier) reconcileAddresses(ctx context.Context, revID string, intent []netcfg.GuestNetwork) error {
	if a.dryRun {
		return nil
	}
	live, err := a.liveGuestBridges()
	if err != nil {
		return err
	}
	var readdressed []string
	for _, n := range a.netdManaged(intent) {
		if !n.Enabled || !live[n.BridgeName] {
			continue
		}
		changed, err := a.reconcileAddress(ctx, n)
		if err != nil {
			return err
		}
		if changed {
			readdressed = append(readdressed, n.BridgeName)
		}
	}
	if len(readdressed) > 0 {
		a.event(ctx, revID, "readdress", true, map[string]any{"bridges": readdressed})
	}
	return nil
}

// destroyBridge tears down a managed guest bridge and its VLAN sub-interface, and reports failure: a swallowed failed `ip link del` leaves a bridge up with its gateway
// but removed from netplan, nftables and Kea, and no health check iterates a bridge that should not exist.
// It refuses to touch the management or WAN interface by name; callers pass br-g* names from liveGuestBridges().
func (a *applier) destroyBridge(ctx context.Context, bridge string) error {
	if bridge == "" || bridge == a.topo.MgmtInterface || bridge == a.topo.WANInterface || bridge == a.legacyBridge {
		return fmt.Errorf("refusing to delete %q: it is not a managed guest bridge", bridge)
	}
	member := a.bridgeMemberOf(bridge)
	if err := a.run(ctx, "ip", "link", "del", bridge); err != nil {
		return fmt.Errorf("delete bridge %s: %w", bridge, err)
	}
	if member != "" && strings.Contains(member, ".") {
		if err := a.run(ctx, "ip", "link", "del", member); err != nil {
			return fmt.Errorf("delete VLAN interface %s of %s: %w", member, bridge, err)
		}
	}
	return nil
}

// ReconcileActiveOnBoot brings the live OS back in line with the DB after a restart: nftables boots from the static /etc/nftables.conf, so the ruleset and Unbound fragment
// are re-installed here and any managed bridge/VLAN netplan did not recreate is brought up surgically. It never touches mgmt/WAN/legacy interfaces.
// nftables is reconciled against a FRESH RENDER, never the stored bundle (see nft_reconcile.go); when the live ruleset already matches, no nft command is issued.
func (a *applier) ReconcileActiveOnBoot(ctx context.Context) {
	if a.dryRun {
		return
	}
	a.busy.Add(1)
	defer a.busy.Add(-1)
	// The confirmed active revision's own intent snapshot, never the live guest_networks rows, which the UI edits directly and may hold an unapplied draft (see store.CurrentActiveIntent).
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
	// The portal address of the confirmed revision comes from its own bundle, never the live setting: a saved-but-unapplied address must not take effect at restart.
	// Only a revision applied before the portal address existed records none; that one upgrade step adopts the configured address, validated as an apply would.
	vip, recorded := readPortalBundle(bundle)
	if !recorded {
		vip = a.adoptPortalIP(ctx, bundle, intent)
	}
	a.setInForce(intent, vip)
	a.setActiveVIP(vip)
	a.refreshPortalTLS(ctx)
	// Ensure managed bridges/VLANs exist (netplan should recreate them on boot,
	// but a surgical create is idempotent and covers a generate-only apply).
	var bootProblems []string
	for _, n := range a.netdManaged(intent) {
		if n.Enabled && !ifaceExists(n.BridgeName) {
			if cerr := a.createNetwork(ctx, n); cerr != nil {
				// Recorded rather than discarded: a boot that could not rebuild a network must not report ok=true.
				bootProblems = append(bootProblems, "create "+n.BridgeName+": "+cerr.Error())
			}
		}
	}
	// ...and remove what should not exist and correct wrong addressing: if netd died between createNetwork and MarkPending the bridge would stay up forever,
	// and a bridge with the previous subnet's gateway was only corrected on the apply path. Both are asserted against the confirmed revision's own intent.
	if live, lerr := a.liveGuestBridges(); lerr != nil {
		bootProblems = append(bootProblems, "list live bridges: "+lerr.Error())
	} else {
		desired := map[string]bool{}
		for _, n := range a.netdManaged(intent) {
			if n.Enabled {
				desired[n.BridgeName] = true
			}
		}
		for br := range live {
			if !desired[br] {
				if derr := a.destroyBridge(ctx, br); derr != nil {
					bootProblems = append(bootProblems, "destroy stray "+br+": "+derr.Error())
				} else {
					bootProblems = append(bootProblems, "destroyed stray bridge "+br+" (no confirmed revision describes it)")
				}
			}
		}
		if aerr := a.reconcileAddresses(ctx, id, intent); aerr != nil {
			bootProblems = append(bootProblems, "addressing: "+aerr.Error())
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
			_ = os.WriteFile(filepath.Join(bundle, "stayconnect.nft"), netcfg.RenderNftables(intent, a.renderTopo()), 0o640)
		}
	}
	// The portal interface, after the ruleset that admits it (same order as an apply).
	if _, perr := a.ensurePortalIface(ctx, vip); perr != nil {
		bootProblems = append(bootProblems, "portal address: "+perr.Error())
	}
	portalFragChanged, portalFragPrev, pferr := a.writeUnboundPortal(vip, a.renderTopo().PortalTLS)
	if pferr != nil {
		bootProblems = append(bootProblems, "portal resolver fragment: "+pferr.Error())
	}
	unboundRestarted := false
	if bundle != "" {
		if _, statErr := os.Stat(bundle); statErr == nil {
			// Re-push the Kea config (idempotent), RENDERED from the confirmed intent by renderKea rather than replayed: the stored file froze option 114 at the HTTPS state of apply time.
			// Only a revision that configured Kea at all (it has the file) is pushed; the bundle is updated to match.
			if _, err := os.Stat(filepath.Join(bundle, "kea-dhcp4.json")); err == nil {
				dhcp4 := a.renderKea(intent)
				if full, merr := json.MarshalIndent(map[string]any{"Dhcp4": dhcp4}, "", "  "); merr == nil {
					_ = os.WriteFile(filepath.Join(bundle, "kea-dhcp4.json"), full, 0o640)
				}
				_ = a.pushKea(dhcp4)
			}
			// Re-install the Unbound fragment from the CURRENT renderer, like the nft structure: replaying the stored copy would keep an upgraded appliance on an older resolver configuration.
			// applyUnbound validates with unbound-checkconf first, so a refused fragment never takes DNS down.
			if _, err := os.ReadFile(filepath.Join(bundle, "stayconnect-guest.conf")); err == nil {
				frag := netcfg.RenderUnbound(intent)
				_ = os.WriteFile(filepath.Join(bundle, "stayconnect-guest.conf"), frag, 0o644)
				_ = os.WriteFile(a.unboundFrag, frag, 0o644)
				if uerr := a.applyUnbound(ctx); uerr != nil {
					bootProblems = append(bootProblems, "guest resolver: "+uerr.Error())
				}
				unboundRestarted = true
			}
		}
	}
	if portalFragChanged && !unboundRestarted {
		if rerr := a.reloadUnboundPortal(ctx, portalFragPrev); rerr != nil {
			bootProblems = append(bootProblems, "portal resolver fragment: "+rerr.Error())
		}
	}
	// The recorded outcome is the real outcome: a boot that failed to rebuild a network or left a stray bridge forwarding must not look clean.
	a.event(ctx, id, "boot_reconcile", len(bootProblems) == 0, map[string]any{
		"bundle": bundle, "nft_changed": res.Changed, "carried_elements": res.Carried,
		"desired_fp": res.DesiredFP, "live_fp_before": res.LiveFP, "problems": bootProblems,
	})
	if len(bootProblems) > 0 {
		slog.Error("netd boot reconcile did not fully converge", "revision", id, "problems", bootProblems)
	}
}

// rollback restores the previous active revision (its bundle re-applied) or, if none, removes everything netd added. Mgmt/legacy interfaces are never touched.
func (a *applier) rollback(ctx context.Context, failedID, reason string) {
	a.busy.Add(1)
	defer a.busy.Add(-1)
	a.event(ctx, failedID, "rollback", true, map[string]any{"reason": reason})

	// Is this revision still rollbackable? Asked first, for every branch, so confirm and rollback are mutually exclusive whichever arrives first.
	// Otherwise the watchdog reads R's predecessor P as active, the operator's /confirm commits (R active, P superseded), and the rollback goes on to destroy R's bridges
	// without being able to rebuild P, leaving NO active revision after telling the operator their change was confirmed.
	switch state, err := a.revisionState(ctx, failedID); {
	case err != nil:
		// Fail closed: not knowing is not permission to change the live network.
		a.event(ctx, failedID, "rollback", false, map[string]any{
			"refused": "the revision state could not be re-read before rolling back", "error": err.Error()})
		return
	case state == "active":
		a.event(ctx, failedID, "rollback", false, map[string]any{
			"refused": "the revision was CONFIRMED between the decision to roll back and this point; " +
				"undoing a configuration an operator has just kept is not a rollback",
			"state": state})
		return
	case state != "applying" && state != "pending_confirmation":
		// Already failed, rolled back or superseded: someone else decided its fate and the network is already reconciled; repeating it made the watchdog restart Unbound every five seconds.
		a.event(ctx, failedID, "rollback", false, map[string]any{
			"refused": "the revision is no longer in flight", "state": state})
		return
	}

	prevBundle, _ := a.previousBundle(ctx, failedID)

	if prevBundle == "" {
		// The tear-down is only correct for a revision that is NOT active, re-checked here: both callers (applier.Rollback, watchdogLoop) decide to roll back before looking for a target,
		// and a /confirm can land in the gap, making the revision ACTIVE with no other active revision to find, which would take the branch below and destroy every bridge of a confirmed configuration.
		// No prior good revision: tear down everything managed and clear. A factory-clean appliance must come back factory-clean, including Kea, which the first apply STARTED.
		if live, lerr := a.liveGuestBridges(); lerr != nil {
			// Not knowing what exists is not permission to declare the appliance clean.
			a.event(ctx, failedID, "rollback", false, map[string]any{
				"error": "the live bridges could not be listed: " + lerr.Error()})
			return
		} else if !a.dryRun {
			// A dry run may not destroy anything: this branch needs the same dryRun guard as the non-factory branch below.
			var refused []string
			for br := range live {
				if derr := a.destroyBridge(ctx, br); derr != nil {
					refused = append(refused, br+": "+derr.Error())
				}
			}
			_ = os.Remove(a.netplanFile)
			_ = os.Remove(a.unboundFrag)
			if len(refused) > 0 {
				// Recorded, and the rollback finishes what it can: leaving Kea serving a network whose bridge survived is worse than an incomplete tear-down that says so.
				a.event(ctx, failedID, "rollback_bridges", false, map[string]any{"not_destroyed": refused})
			}
			// Factory-clean has no portal address either: the interface goes, and the resolver stops answering
			// the portal names with it.
			if _, perr := a.ensurePortalIface(ctx, ""); perr != nil {
				a.event(ctx, failedID, "rollback_portal_address", false, map[string]any{"error": perr.Error()})
			}
			if changed, prev, ferr := a.writeUnboundPortal("", netcfg.PortalTLS{}); ferr == nil && changed {
				_ = a.reloadUnboundPortal(ctx, prev)
			}
		}
		a.clearInForce()
		a.restoreKeaBootstrap(ctx)
		a.recordRolledBack(ctx, failedID, reason)
		return
	}
	// Reconcile bridges to the previous good revision's managed set: destroy any managed bridge the failed apply created that it does not include
	// (legacy/mgmt/WAN are never in liveGuestBridges()).
	prevBridges := a.bridgesInBundle(prevBundle)
	if !a.dryRun {
		live, lerr := a.liveGuestBridges()
		if lerr != nil {
			a.event(ctx, failedID, "rollback", false, map[string]any{
				"error": "the live bridges could not be listed: " + lerr.Error()})
			return
		}
		var refused []string
		for br := range live {
			if !prevBridges[br] {
				if derr := a.destroyBridge(ctx, br); derr != nil {
					refused = append(refused, br+": "+derr.Error())
				}
			}
		}
		if len(refused) > 0 {
			a.event(ctx, failedID, "rollback_bridges", false, map[string]any{"not_destroyed": refused})
		}
		// ...and recreate what the failed apply removed: the previous good revision's own intent says which bridges must exist,
		// rebuilt as an apply builds them before nftables, DHCP and DNS are restored.
		a.recreateMissingBridges(ctx, failedID)
		// A failed apply may also have re-addressed a kept bridge; the same reconciliation restores its addressing.
		if prevIntent, ierr := a.previousActiveIntent(ctx, failedID); ierr == nil && prevIntent != nil {
			if rerr := a.reconcileAddresses(ctx, failedID, prevIntent); rerr != nil {
				a.event(ctx, failedID, "rollback_addresses", false, map[string]any{"error": rerr.Error()})
			}
		}
	}
	// Restore the previous good revision's nft structure by RENDERING ITS STORED INTENT with the current renderer. There is deliberately no fallback to the stored file:
	// it begins with `delete table inet stayconnect` and would delete every authorization set and structure the current software requires.
	// A rollback that cannot be done safely must stay unfinished and say so, leaving the operator the live ruleset they already had.
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
			// The previous revision's portal address comes back with it (from its bundle; for a bundle that predates the portal address, the confirmed active revision's address),
			// put in force BEFORE the converge so the restored ruleset names it.
			pvip, recorded := readPortalBundle(prevBundle)
			if !recorded {
				pvip = a.activeVIP()
			}
			a.setInForce(prevIntent, pvip)
			if _, nerr := a.ensureNftStructure(ctx, prevIntent, "rollback"); nerr != nil {
				a.event(ctx, failedID, "rollback_nft", false, map[string]any{
					"blocker": "safe reconciliation to the previous revision failed: " + nerr.Error(),
					"action":  "nft structure was NOT rolled back; the live ruleset is unchanged and needs operator attention",
				})
				slog.Error("netd rollback: safe nft reconciliation failed; NOT falling back to the stored ruleset file", "err", nerr)
			} else {
				nftRolledBack = true
				// ...and the portal interface follows the restored address (after the restored ruleset).
				if _, perr := a.ensurePortalIface(ctx, pvip); perr != nil {
					a.event(ctx, failedID, "rollback_portal_address", false, map[string]any{"error": perr.Error()})
				}
			}
		}
		a.event(ctx, failedID, "rollback_nft", nftRolledBack, nil)
		if raw, err := os.ReadFile(filepath.Join(prevBundle, "kea-dhcp4.json")); err == nil {
			if nftRolledBack {
				// The previous intent is in force: render it (renderKea, so option 114 reflects the CURRENT
				// HTTPS state rather than whatever it was when that revision was applied).
				_ = a.pushKea(a.renderKea(prevIntent))
			} else {
				_ = a.pushKeaFile(raw)
			}
		}
		_, inForceVIP, _ := a.inForce()
		portalFragChanged, portalFragPrev, _ := a.writeUnboundPortal(inForceVIP, a.renderTopo().PortalTLS)
		if raw, err := os.ReadFile(filepath.Join(prevBundle, "stayconnect-guest.conf")); err == nil {
			portalFragChanged = false // the restart below reloads both fragments
			_ = os.WriteFile(a.unboundFrag, raw, 0o644)
			if uerr := a.applyUnbound(ctx); uerr != nil {
				a.event(ctx, failedID, "rollback_unbound", false, map[string]any{"error": uerr.Error()})
			}
		}
		if portalFragChanged {
			if rerr := a.reloadUnboundPortal(ctx, portalFragPrev); rerr != nil {
				a.event(ctx, failedID, "rollback_unbound", false, map[string]any{"error": rerr.Error()})
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(prevBundle, "50-stayconnect-guest.yaml")); err == nil {
		// And regenerate: restoring the YAML without `netplan generate` left the generated units describing the FAILED revision, so the next reboot recreated destroyed bridges.
		if werr := os.WriteFile(a.netplanFile, raw, 0o600); werr != nil {
			a.event(ctx, failedID, "rollback_netplan", false, map[string]any{"error": werr.Error()})
		} else if gerr := a.run(ctx, "netplan", "generate"); gerr != nil {
			a.event(ctx, failedID, "rollback_netplan", false, map[string]any{
				"error": "the restored netplan could not be regenerated: " + gerr.Error()})
		}
	}
	a.recordRolledBack(ctx, failedID, reason)
}

// recreateMissingBridges rebuilds every enabled, netd-managed bridge of the previous ACTIVE revision that is
// not live. Each failure is recorded as a rollback event and the rest continue: one bridge that cannot be
// rebuilt must not stop the others from coming back.
func (a *applier) recreateMissingBridges(ctx context.Context, failedID string) {
	prevIntent, err := a.previousActiveIntent(ctx, failedID)
	if err != nil || prevIntent == nil {
		return // the nft step below records why the previous revision could not be read
	}
	live, lerr := a.liveGuestBridges()
	if lerr != nil {
		a.event(ctx, failedID, "rollback_bridges", false, map[string]any{
			"error": "the live bridges could not be listed, so none were rebuilt: " + lerr.Error()})
		return
	}
	var rebuilt, failed []string
	for _, n := range a.netdManaged(prevIntent) {
		if !n.Enabled || live[n.BridgeName] {
			continue
		}
		if err := a.createNetwork(ctx, n); err != nil {
			failed = append(failed, n.BridgeName+": "+err.Error())
			continue
		}
		rebuilt = append(rebuilt, n.BridgeName)
	}
	if len(rebuilt) > 0 || len(failed) > 0 {
		a.event(ctx, failedID, "rollback_bridges", len(failed) == 0, map[string]any{"rebuilt": rebuilt, "failed": failed})
	}
}

func (a *applier) pushKeaFile(raw []byte) error {
	// raw is {"Dhcp4": {...}} — extract and config-set (re-detects interfaces).
	dhcp4, err := extractDhcp4(raw)
	if err != nil {
		return err
	}
	return a.pushKea(dhcp4)
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

	// kea_running checks that Kea is LISTENING, not status-get: status-get answers healthy from a Kea holding no DHCP socket, which a rollback-then-re-apply leaves behind.
	keaOK, keaDetail := true, ""
	if !a.dryRun {
		if !a.kea.Healthy() {
			keaOK, keaDetail = false, "Kea is not answering its control socket"
		} else if missing := a.gatewaysNotListening(intent); len(missing) > 0 {
			keaOK = false
			keaDetail = "Kea is answering but holds NO DHCP socket for " + strings.Join(missing, ",") +
				" — guests on that network would get no address"
		}
	}
	add("kea_running", keaOK, keaDetail)

	// portal_listen: portald must still be listening on the HTTP portal port.
	portalOK := a.dryRun || tcpListening(a.topo.PortalHTTPPort)
	add("portal_listen", portalOK, itoa(a.topo.PortalHTTPPort))

	// portal_address: the portal interface carries exactly the address this apply put in force (or does not exist when there is none); the HTTPS portal and scd's verdict depend on it.
	_, vip, _ := a.inForce()
	paOK, paDetail := true, vip
	if !a.dryRun {
		if ok, detail := a.portalIfaceHealthy(vip); !ok {
			paOK, paDetail = false, detail
		}
	}
	add("portal_address", paOK, paDetail)

	return out
}

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

// liveGuestBridges answers which managed guest bridges exist now. An unreadable /sys is an error, not an empty appliance:
// an empty map would silently disable the whole L2/L3 reconciler.
func (a *applier) liveGuestBridges() (map[string]bool, error) {
	if a.liveBridgesFn != nil {
		return a.liveBridgesFn(), nil
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "br-g") && name != a.legacyBridge {
			out[name] = true
		}
	}
	return out, nil
}

func ifaceExists(name string) bool {
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// ifaceAddrs lists the IPv4 CIDRs configured on an interface ("10.20.0.1/24"), newest kernel order.
func ifaceAddrs(name string) []string {
	out, err := exec.Command("ip", "-o", "-4", "addr", "show", "dev", name).Output()
	if err != nil {
		return nil
	}
	var cidrs []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i, tok := range f {
			if tok == "inet" && i+1 < len(f) {
				cidrs = append(cidrs, f[i+1])
			}
		}
	}
	return cidrs
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

// bridgeMember names the single interface enslaved to a bridge, or "" when that cannot be answered: an unreadable directory or a bridge with more than one member
// is not the one-VLAN-interface shape netd creates. Callers treat "" as "nothing to assert".
func bridgeMember(bridge string) string {
	entries, err := os.ReadDir("/sys/class/net/" + bridge + "/brif")
	if err != nil || len(entries) != 1 {
		return ""
	}
	return entries[0].Name()
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

// recordRolledBack writes the rolled_back state and reports when it cannot: if the write fails the revision stays pending_confirmation
// and the watchdog rolls it back again every five seconds forever. A refusal (someone else already decided its fate) is not worth shouting about.
func (a *applier) recordRolledBack(ctx context.Context, id, reason string) {
	err := a.markRolledBack(ctx, id, reason)
	if err == nil {
		return
	}
	if errors.Is(err, errNotRollbackable) {
		a.event(ctx, id, "rollback", false, map[string]any{
			"note":  "the revision had already left the in-flight states; its recorded fate is left alone",
			"error": err.Error()})
		return
	}
	slog.Error("netd could not record a rollback; the revision will be retried by the watchdog",
		"revision", id, "err", err)
	a.event(ctx, id, "rollback", false, map[string]any{
		"error": "the rolled_back state could not be recorded: " + err.Error()})
}
