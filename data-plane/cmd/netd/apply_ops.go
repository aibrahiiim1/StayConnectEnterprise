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
	live, err := a.liveGuestBridges()
	if err != nil {
		return err
	}

	// A BRIDGE THAT ALREADY EXISTS MUST STILL BE WHAT THE INTENT SAYS IT IS.
	//
	// applyBundle only ever calls createNetwork for a bridge that is MISSING, so a network whose VLAN id or
	// parent port changed under an UNCHANGED bridge name was written to the database, rendered into the bundle,
	// pushed to netplan, nftables and Kea -- and never to the wire. The live bridge went on being enslaved to
	// the old VLAN interface while every record said otherwise, which is the original silent-divergence defect
	// in its purest form.
	//
	// edged refuses such an edit (immutable_topology, and 0105's staged replacement is the supported route), so
	// this cannot be reached through the product today. It is checked here because netd is the component that
	// answers for the wire: anything that ever writes guest_networks -- a future surface, a migration, a hand-run
	// UPDATE -- would otherwise reproduce the defect with nothing to catch it. Refusing is safe because a
	// replacement always builds a NEW bridge name, so no legitimate change lands here.
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
		// ...and converge the addressing of the bridges that were ALREADY there, which createNetwork never
		// touches. Without this an edited subnet or gateway is applied everywhere except on the wire.
		if err := a.reconcileAddresses(ctx, revID, intent); err != nil {
			return err
		}
	}
	a.st.Event(ctx, revID, "l2l3", true, map[string]any{"bridges": len(desired)})

	// Persistence: write the netplan file + validate (no apply — the live
	// state is already correct via ip commands).
	//
	// A DRY RUN WRITES NOTHING. The netplan file and the Unbound fragment below were written outside the dryRun
	// guard, so a run that promised no side effects rewrote the two files that decide what the appliance brings
	// up after its next reboot -- and then skipped `netplan generate`, leaving the YAML and the generated units
	// describing different networks.
	if !a.dryRun {
		if err := os.WriteFile(a.netplanFile, netcfg.RenderNetplan(managed), 0o600); err != nil {
			return fmt.Errorf("write netplan: %w", err)
		}
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

	// A BRIDGE THAT IS NOT RUNNING YET CANNOT BE BOUND. `ip link add` returned moments ago, but a bridge is
	// not IFF_RUNNING until a member settles, and the Kea configuration used to land in exactly that gap:
	// Kea kept running, answered status-get, reported the right subnet, and held no DHCP socket at all.
	var wantBridges []string
	for _, n := range managed {
		if n.Enabled && n.BridgeName != "" {
			wantBridges = append(wantBridges, n.BridgeName)
		}
	}
	a.waitBridgesRunning(ctx, wantBridges)

	if !a.dryRun {
		// FIRST GUEST NETWORK ON A FACTORY-CLEAN APPLIANCE: Kea is installed stopped and disabled because it
		// binds a bridge that did not exist until a moment ago, and it can only be configured through a
		// socket that exists only while it runs. No-op once Kea is answering.
		// The control socket takes the inner Dhcp4 object; the config FILE needs the whole document, which is
		// the same render wrapped exactly as extractDhcp4 expects to read it back.
		full, ferr := json.MarshalIndent(map[string]any{"Dhcp4": dhcp4}, "", "  ")
		if ferr != nil {
			return fmt.Errorf("render kea config file: %w", ferr)
		}
		if err := a.bootstrapKeaIfStopped(ctx, revID, full); err != nil {
			return err
		}
		if err := a.kea.ConfigSet(dhcp4); err != nil {
			return err
		}
		// AND NOW ASSERT THE THING THAT MATTERS. config-set does not reliably rebind Kea's sockets when the
		// interface set changes underneath it — rollback destroys a bridge and re-apply creates a new one
		// with a new ifindex, and Kea accepts a configuration for an interface it never binds. A restart
		// rebinds instantly from the very same config. Only locally-served, enabled networks are expected to
		// hold a socket; a relayed network legitimately has none.
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
	if err := a.applyUnbound(ctx); err != nil {
		a.st.Event(ctx, revID, "unbound", false, map[string]any{"error": err.Error()})
		return fmt.Errorf("guest resolver: %w", err)
	}
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
// applyUnbound validates and reloads the guest resolver, AND SAYS WHEN IT COULD NOT.
//
// All three steps used to be discarded, and the caller then recorded "unbound ok=true". No health check looks at
// DNS, so a guest network whose resolver never came up produced a completely green apply -- and the guests on it
// could not resolve anything, which on a captive portal means the portal itself never appears.
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

// reconcileAddress makes an EXISTING managed bridge carry exactly the gateway address its intent asks for, and
// reports whether it had to change anything.
//
// WHY AN APPLY USED TO LIE ABOUT THIS. applyBundle only ever called createNetwork for a bridge that was
// MISSING, and `ip addr add` lives inside createNetwork. So an operator who edited a client network's subnet or
// gateway -- the one addressing change the detail page offers -- had their edit written to the database,
// rendered into the bundle, pushed to Kea... and never applied to the live bridge, which went on carrying the
// previous address. The DHCP scope then handed out addresses in a subnet the gateway was not on.
//
// It is reconciled here instead: the desired address is added if absent and every other address this bridge
// carries is removed, so the live interface ends up with exactly what the intent says. Only bridges in the
// desired managed set are ever touched -- liveGuestBridges() returns br-g* only, and the management and WAN
// interfaces are never in it.
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
		// A FAILED REMOVAL IS A FAILED APPLY. This used to be discarded, and the consequence was precise: the
		// bridge kept BOTH the previous gateway and the new one, the apply reported "readdress ok", and the
		// gateway_up health check only asserts that the wanted address is PRESENT -- never that the old one is
		// gone. The appliance then answered ARP for a gateway that no DHCP scope and no nftables rule described,
		// and the old leases went on routing through it. Returning the error rolls the apply back instead.
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

// destroyBridge tears down a managed guest bridge and its VLAN sub-interface.
//
// IT REPORTS FAILURE, WHICH IT DID NOT USED TO. Both deletes were discarded and the function returned nil
// unconditionally, which made applyBundle's "destroy %s" error path dead code. The consequence of a failed
// `ip link del` -- EBUSY, a restricted unit, a busy netlink socket -- was a guest network REMOVED from the
// intent, and therefore removed from netplan, from nftables and from Kea, whose bridge was still up and still
// carrying its gateway address. Its clients kept an L2/L3 path with no isolation, no NAT policy and no DHCP,
// and the revision recorded a clean success. Nothing in netd would ever have noticed: the health checks and
// the address reconciler both iterate the INTENT, and a bridge that should not exist is not in it.
//
// It also refuses to touch the management or WAN interface by name. Every caller already passes a br-g* name
// from liveGuestBridges(), so this cannot trigger today -- it is here because the only thing standing between
// this function and the appliance's own connectivity was a string prefix computed somewhere else.
func (a *applier) destroyBridge(ctx context.Context, bridge string) error {
	if bridge == "" || bridge == a.topo.MgmtInterface || bridge == a.topo.WANInterface || bridge == a.legacyBridge {
		return fmt.Errorf("refusing to delete %q: it is not a managed guest bridge", bridge)
	}
	// find VLAN member (if any) before deleting the bridge
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
	var bootProblems []string
	for _, n := range a.netdManaged(intent) {
		if n.Enabled && !ifaceExists(n.BridgeName) {
			if cerr := a.createNetwork(ctx, n); cerr != nil {
				// Recorded rather than discarded: a boot reconcile that could not rebuild a guest network used
				// to report ok=true, so the appliance came up serving fewer networks than its confirmed
				// revision describes and nothing said so.
				bootProblems = append(bootProblems, "create "+n.BridgeName+": "+cerr.Error())
			}
		}
	}
	// ...AND REMOVE WHAT SHOULD NOT EXIST, AND CORRECT WHAT IS ADDRESSED WRONGLY.
	//
	// Boot reconcile used to do neither, and both gaps were reachable from one crash. If netd died between
	// createNetwork and MarkPending, the bridge it had just built stayed up forever: no revision described it,
	// so no apply would ever remove it, and its clients kept an L2 path with no nftables rules and no DHCP.
	// And a bridge left carrying the previous subnet's gateway was corrected only on the apply path, never
	// here -- so the very defect reconcileAddress exists to fix survived a reboot.
	//
	// Both are now asserted against the CONFIRMED revision's own intent, which is the only thing netd trusts.
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
				if uerr := a.applyUnbound(ctx); uerr != nil {
					bootProblems = append(bootProblems, "guest resolver: "+uerr.Error())
				}
			}
		}
	}
	// THE RECORDED OUTCOME IS THE REAL OUTCOME. This used to be an unconditional ok=true, which is how a boot
	// that failed to rebuild a network, or left a stray bridge forwarding, looked identical to a clean one.
	a.event(ctx, id, "boot_reconcile", len(bootProblems) == 0, map[string]any{
		"bundle": bundle, "nft_changed": res.Changed, "carried_elements": res.Carried,
		"desired_fp": res.DesiredFP, "live_fp_before": res.LiveFP, "problems": bootProblems,
	})
	if len(bootProblems) > 0 {
		slog.Error("netd boot reconcile did not fully converge", "revision", id, "problems", bootProblems)
	}
}

// rollback restores the previous active revision (its bundle re-applied) or, if
// none, removes everything netd added. Management connectivity is preserved
// throughout (mgmt/legacy interfaces are never touched here).
func (a *applier) rollback(ctx context.Context, failedID, reason string) {
	a.event(ctx, failedID, "rollback", true, map[string]any{"reason": reason})

	// IS THIS REVISION STILL ROLLBACKABLE? ASKED FIRST, FOR EVERY BRANCH.
	//
	// This check used to live inside the factory-clean branch below, so it only ever ran on an appliance with
	// no earlier revision -- which is to say, almost never. On every appliance that had applied anything
	// before, the ordinary branch ran with no state check at all, and lost this race:
	//
	//   watchdogLoop sees revision R overdue and calls us; we read R's predecessor P, still active;
	//   the operator's /confirm commits in the gap -- R becomes active, P becomes superseded;
	//   we carry on rolling back to P, destroy every bridge R created, and then cannot rebuild P's
	//   (ActiveIntent looks for an ACTIVE revision other than R, and P is superseded now), so nothing is
	//   rebuilt, nftables is left holding R's ruleset, and MarkRolledBack stamps over R's 'active'.
	//
	// The appliance was then left with NO active revision, no guest bridges, and a boot reconcile that could
	// never find an intent to assert -- after telling the operator their change was confirmed. Asking here, for
	// both branches, makes the confirm and the rollback mutually exclusive whichever arrives first.
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
		// Already failed, already rolled back, or superseded: someone else has decided its fate and the live
		// network has already been reconciled to that decision. Doing it again is how the watchdog used to
		// restart Unbound every five seconds forever.
		a.event(ctx, failedID, "rollback", false, map[string]any{
			"refused": "the revision is no longer in flight", "state": state})
		return
	}

	prevBundle, _ := a.previousBundle(ctx, failedID)

	if prevBundle == "" {
		// THE TEAR-DOWN IS ONLY CORRECT FOR A REVISION THAT IS NOT ACTIVE, AND THAT IS RE-CHECKED ABOVE.
		//
		// Both callers decide to roll back BEFORE this function looks for a target, and a confirmation can
		// land in the gap:
		//
		//   applier.Rollback   reads the state, sees pending_confirmation, calls us;
		//   watchdogLoop       reads PendingRevision(), sees it overdue, calls us.
		//
		// If /confirm commits in between, the revision becomes ACTIVE and its predecessor becomes
		// SUPERSEDED. ActiveBundlePath then finds no OTHER active revision, prevBundle is empty, and we
		// would take the branch below -- destroying every guest bridge and stopping DHCP for a
		// configuration an operator had just confirmed. That is the precise failure the operator guard was
		// written to prevent, arriving through a window the guard could not close from outside.
		//
		// The check belongs here rather than in either caller, because there are two callers and the
		// watchdog's race predates the operator guard entirely. A revision that is ACTIVE has, by
		// definition, been confirmed, and the factory-clean path is for a first apply that never was.
		// No prior good revision: tear down everything managed and clear.
		//
		// A FACTORY-CLEAN APPLIANCE HAS TO COME BACK FACTORY-CLEAN. This is the path the very first guest
		// network takes when it fails or its confirmation expires, and that first apply is the one that
		// STARTED Kea — because there is no other way to configure it. Tearing down the bridge, the netplan
		// and the unbound fragment while leaving Kea running, enabled, and serving DHCP for a network that
		// no longer exists is not a rollback.
		if live, lerr := a.liveGuestBridges(); lerr != nil {
			// Not knowing what exists is not permission to declare the appliance clean.
			a.event(ctx, failedID, "rollback", false, map[string]any{
				"error": "the live bridges could not be listed: " + lerr.Error()})
			return
		} else if !a.dryRun {
			// A DRY RUN MAY NOT DESTROY ANYTHING. This branch had no dryRun guard at all, so a dry-run apply
			// that failed tore down every guest bridge and deleted the netplan and Unbound files for real --
			// while the non-factory branch below was guarded, which made the asymmetry a bug rather than a
			// decision.
			var refused []string
			for br := range live {
				if derr := a.destroyBridge(ctx, br); derr != nil {
					refused = append(refused, br+": "+derr.Error())
				}
			}
			_ = os.Remove(a.netplanFile)
			_ = os.Remove(a.unboundFrag)
			if len(refused) > 0 {
				// Recorded, and the rollback still finishes what it can: leaving Kea serving a network whose
				// bridge survived would be worse than an incomplete tear-down that says so.
				a.event(ctx, failedID, "rollback_bridges", false, map[string]any{"not_destroyed": refused})
			}
		}
		a.restoreKeaBootstrap(ctx)
		a.recordRolledBack(ctx, failedID, reason)
		return
	}
	// Reconcile bridges to the previous good revision's managed set: destroy any
	// managed guest bridge that the failed apply created but the previous good
	// revision does not include. The legacy/mgmt/WAN interfaces are never in
	// liveGuestBridges(), so they cannot be touched here.
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
		// ...AND RECREATE what the failed apply removed. A revision that deleted or replaced a network
		// destroyed that network's bridge; rolling back used to leave it destroyed until the next boot
		// reconcile, so a refused replacement took the OLD network down too. The previous good revision's
		// own intent says which bridges must exist; any that are missing are rebuilt exactly as an apply
		// builds them, before nftables, DHCP and DNS are restored for them below.
		a.recreateMissingBridges(ctx, failedID)
		// A failed apply may also have RE-ADDRESSED a bridge it kept. The previous confirmed intent says what
		// each bridge should carry, so the same reconciliation puts the addressing back.
		if prevIntent, ierr := a.previousActiveIntent(ctx, failedID); ierr == nil && prevIntent != nil {
			if rerr := a.reconcileAddresses(ctx, failedID, prevIntent); rerr != nil {
				a.event(ctx, failedID, "rollback_addresses", false, map[string]any{"error": rerr.Error()})
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
			if uerr := a.applyUnbound(ctx); uerr != nil {
				a.event(ctx, failedID, "rollback_unbound", false, map[string]any{"error": uerr.Error()})
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(prevBundle, "50-stayconnect-guest.yaml")); err == nil {
		// AND REGENERATE. Restoring the YAML without re-running `netplan generate` left the generated networkd
		// units describing the FAILED revision, so the next reboot recreated the bridges the rollback had just
		// destroyed -- a rollback that undid itself at the next power cut.
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

	// kea_running — IS IT LISTENING, not is it feeling well.
	//
	// This used to be a.kea.Healthy(), a status-get. status-get answers "healthy" from a Kea that holds no
	// DHCP socket at all, which is the exact state a rollback-then-re-apply leaves behind: the bridge is
	// recreated with a new ifindex and config-set does not rebind. The check therefore could not fail while
	// guests were unable to get an address, and the failure it hid looks like a cabling fault from the guest
	// side — the most expensive place to debug it.
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

// liveGuestBridges answers which managed guest bridges exist right now.
//
// AN UNREADABLE /sys IS NOT AN EMPTY APPLIANCE. The directory read used to be discarded, so any failure became
// an empty map -- which every caller reads as "no guest bridges exist". That silently disabled the entire L2/L3
// reconciler in one move: nothing was destroyed, every bridge was skipped by the address reconciler, and a
// rollback decided every previous bridge needed recreating. The error is returned so the apply fails instead.
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

// bridgeMember names the single interface enslaved to a bridge, or "" when that cannot be answered.
//
// It returns "" for an unreadable directory AND for a bridge with more than one member, because both mean "this
// is not the one-VLAN-interface-per-bridge shape netd creates" and neither justifies acting on entries[0] --
// which is what it used to do, so a bridge with two members had one of them picked arbitrarily for deletion.
// Every caller treats "" as "nothing to assert", which is the safe reading.
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

// recordRolledBack writes the rolled_back state and SAYS SO WHEN IT CANNOT.
//
// Both rollback exits used to discard this error. If the write failed the revision stayed
// pending_confirmation, so five seconds later the watchdog found it overdue again and rolled it back again --
// rebuilding bridges, re-pushing Kea and restarting Unbound on every pass, for as long as the appliance ran.
// A refusal (someone else already decided this revision's fate) is not an error worth shouting about; a failure
// to write is, because it is the one that loops.
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
