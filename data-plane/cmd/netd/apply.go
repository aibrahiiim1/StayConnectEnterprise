package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// applier owns the transactional apply/rollback of guest-network config: L2/L3 applied surgically with `ip` (additive, reversible,
// never touching management/WAN/legacy interfaces), netplan written only for reboot persistence, nftables + Kea + Unbound rendered
// from the DB and swapped atomically; a watchdog rolls back if not confirmed within confirmWindow.
type applier struct {
	// nftWatch records consecutive failures of the periodic firewall re-assertion (audit Phase 8 L13).
	nftWatch      nftWatchState
	st            revisionStore
	kea           *keaClient
	topo          netcfg.Topology
	generatedDir  string // /etc/stayconnect/generated/network
	netplanFile   string // /etc/netplan/50-stayconnect-guest.yaml
	nftPath       string
	unboundFrag   string // /etc/unbound/unbound.conf.d/stayconnect-guest.conf
	keaLeaseCSV   string
	keaSocket     string
	keaConfFile   string // /etc/kea/kea-dhcp4.conf — written only by the factory-clean bootstrap
	confirmWindow time.Duration
	// legacyBridge is never surgically managed (it is adopted as-is).
	legacyBridge string
	dryRun       bool // pilot-safe: skip real ip/nft/kea side effects

	// runFn/outFn are the ONLY places netd shells out; as fields a test can observe which commands a reconciliation issued
	// (including none in steady state, which differs from rewriting the same thing and affects live guests' authorization).
	runFn func(ctx context.Context, name string, args ...string) error
	outFn func(ctx context.Context, name string, args ...string) ([]byte, error)

	// discoverFn is the interface-discovery seam: presence is observed live at decision time.
	discoverFn func(ctx context.Context) ([]Interface, error)
	// knownFn / syncFn are advisory inventory seams (what was EVER recorded, and the opportunistic refresh); presence depends on neither.
	knownFn func(ctx context.Context) (map[string]bool, error)
	syncFn  func(ctx context.Context, ifaces []Interface) error

	// activeIntentFn / prevIntentFn / eventFn are the database seams: a restart must reconstruct the CONFIRMED active revision, never an unapplied draft.
	activeIntentFn func(ctx context.Context) (string, string, []netcfg.GuestNetwork, error)
	prevIntentFn   func(ctx context.Context, exceptID string) ([]netcfg.GuestNetwork, error)
	eventFn        func(ctx context.Context, revID, kind string, ok bool, detail map[string]any)
	prevBundleFn   func(ctx context.Context, exceptID string) (string, error)
	markRolledFn   func(ctx context.Context, id, reason string) error
	// revStateFn decides whether an operator-requested rollback has a target at all (see Rollback).
	revStateFn func(ctx context.Context, id string) (string, error)
	// liveBridgesFn answers which guest bridges exist now (default: /sys/class/net).
	liveBridgesFn func() map[string]bool
	// addrsFn answers which IPv4 CIDRs an interface carries now (default: ip -o -4 addr show).
	addrsFn func(name string) []string
	// memberFn answers which interface is enslaved to a bridge now (default: /sys/class/net/<br>/brif).
	memberFn func(bridge string) string

	// unboundPortalFrag is the SEPARATE resolver fragment for the portal's public names
	// (/etc/unbound/unbound.conf.d/stayconnect-portal.conf). "" disables it.
	unboundPortalFrag string
	// portalMu guards portal, the intent and portal address IN FORCE plus the latest HTTPS state.
	portalMu sync.Mutex
	portal   portalInForce
	// keaMu serialises every Kea config-set against the runtime refresh's read-compare-push.
	keaMu sync.Mutex
	// busy counts apply / rollback / boot reconcile in progress; the runtime refresh stands aside while > 0.
	busy atomic.Int32
	// opMu serialises every operation that changes what is in force (apply, rollback, boot reconcile); the periodic loops that render the
	// in-force configuration run only while they can take it (apply_integrity.go).
	opMu sync.Mutex
	// inflight / inflightMu: what was in force before the apply running in this process.
	inflight   *applyInflight
	inflightMu sync.Mutex
	// incomplete / incMu: set when a rollback did not restore everything; Apply refuses while it is set.
	incomplete *incompleteMarker
	incMu      sync.Mutex
	// inFlightFn is the seam for "which revision is applying or awaiting confirmation".
	inFlightFn func(ctx context.Context) (id, state string, err error)
	// ownerMarkFn / recordedFn are the seams for bridge ownership: the interface alias netd set, and the bridges applied revisions describe.
	// keaDiskErr is why the file Kea starts from could not be written from the confirmed configuration ("" = it holds it).
	keaDiskErr    string
	keaDiskMu     sync.Mutex
	ifaceExistsFn func(name string) bool
	// diag replaces the machine reads that diagnostics make.
	diag *diagSources
	// healthFn replaces the post-reconcile health checks.
	healthFn func(ctx context.Context, revID string, intent []netcfg.GuestNetwork) []healthResult
	// procNetUDPFn replaces /proc/net/udp when deciding whether Kea holds a DHCP socket.
	procNetUDPFn func() string
	ownerMarkFn  func(name string) string
	recordedFn   func(ctx context.Context) (map[string]recordedBridge, error)
	// nftMu serialises every structural converge (apply, rollback, boot, the periodic re-assertion in
	// nft_watch.go), so two of them can never interleave their atomic table replacements.
	nftMu sync.Mutex
	// mgmtAddrFn reads an interface's primary IPv4 for NETD_MGMT_ADDR=auto (nft_watch.go). nil = the live system.
	mgmtAddrFn func(iface string) string
	// Further seams (portal address, HTTPS state, host addresses, portal link, Kea socket, editable intent) default to the database,
	// `ip -j -4 addr`, /sys/class/net and the real Kea socket.
	portalIPFn   func(ctx context.Context) (string, error)
	portalTLSFn  func(ctx context.Context) (netcfg.PortalTLS, error)
	hostAddrsFn  func(ctx context.Context) ([]netcfg.HostAddr, error)
	portalLinkFn func(name string) (exists, up bool)
	keaRTSeam    keaRuntime
	intentFn     func(ctx context.Context) ([]netcfg.GuestNetwork, error)
}

func (a *applier) bridgeMemberOf(bridge string) string {
	if a.memberFn != nil {
		return a.memberFn(bridge)
	}
	return bridgeMember(bridge)
}

func (a *applier) ifaceAddrsOf(name string) []string {
	if a.addrsFn != nil {
		return a.addrsFn(name)
	}
	return ifaceAddrs(name)
}

func (a *applier) revisionState(ctx context.Context, id string) (string, error) {
	if a.revStateFn != nil {
		return a.revStateFn(ctx, id)
	}
	return a.st.RevisionState(ctx, id)
}

// currentActiveIntent returns the confirmed active revision's id, bundle and immutable intent snapshot.
func (a *applier) currentActiveIntent(ctx context.Context) (string, string, []netcfg.GuestNetwork, error) {
	if a.activeIntentFn != nil {
		return a.activeIntentFn(ctx)
	}
	return a.st.CurrentActiveIntent(ctx)
}

func (a *applier) previousActiveIntent(ctx context.Context, exceptID string) ([]netcfg.GuestNetwork, error) {
	if a.prevIntentFn != nil {
		return a.prevIntentFn(ctx, exceptID)
	}
	return a.st.ActiveIntent(ctx, exceptID)
}

func (a *applier) previousBundle(ctx context.Context, exceptID string) (string, error) {
	if a.prevBundleFn != nil {
		return a.prevBundleFn(ctx, exceptID)
	}
	return a.st.ActiveBundlePath(ctx, exceptID)
}

func (a *applier) markRolledBack(ctx context.Context, id, reason string) error {
	if a.markRolledFn != nil {
		return a.markRolledFn(ctx, id, reason)
	}
	return a.st.MarkRolledBack(ctx, id, reason)
}

func (a *applier) event(ctx context.Context, revID, kind string, ok bool, detail map[string]any) {
	if a.eventFn != nil {
		a.eventFn(ctx, revID, kind, ok, detail)
		return
	}
	a.st.Event(ctx, revID, kind, ok, detail)
}

type applyResult struct {
	RevisionID string                  `json:"revision_id"`
	Seq        int64                   `json:"seq"`
	State      string                  `json:"state"`
	Validation netcfg.ValidationResult `json:"validation"`
	Health     []healthResult          `json:"health"`
	Message    string                  `json:"message,omitempty"`
	// RollbackIncomplete lists the rollback steps that did not succeed; empty when the rollback restored everything.
	RollbackIncomplete []string `json:"rollback_incomplete,omitempty"`
}

type healthResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Validate loads intent, runs structural validation, and records a revision in
// the 'validated' (or 'failed') state. It never changes the system.
func (a *applier) Validate(ctx context.Context, summary, actor string) (*applyResult, error) {
	intent, err := a.st.LoadIntent(ctx)
	if err != nil {
		return nil, err
	}
	avail, known, err := a.presenceSets(ctx)
	if err != nil {
		return nil, err
	}
	res, _, err := a.validateWithPortal(ctx, intent, avail, known)
	if err != nil {
		return nil, err
	}
	id, seq, err := a.st.CreateRevision(ctx, summary, intent, actor)
	if err != nil {
		return nil, err
	}
	_ = a.st.SetRevisionValidation(ctx, id, res)
	a.st.Event(ctx, id, "validate", res.OK, map[string]any{"issues": len(res.Issues)})
	out := &applyResult{RevisionID: id, Seq: seq, State: "validated", Validation: res}
	if !res.OK {
		_ = a.st.SetRevisionState(ctx, id, "failed")
		out.State = "failed"
	}
	return out, nil
}

// Apply validates, generates a bundle, snapshots, applies surgically, runs
// health checks, and enters pending_confirmation with a watchdog. On any
// pre-commit failure it rolls back to the previous active revision.
func (a *applier) Apply(ctx context.Context, summary, actor string) (*applyResult, error) {
	// One operation at a time. The periodic loops that render the in-force configuration take the same lock, so none of them can read an
	// intent this apply has published but not yet proven.
	if !a.acquireOp(ctx, opWait) {
		return nil, errApplyBusy
	}
	defer a.releaseOp()
	// The runtime portal refresh stands aside for the whole apply, including any rollback it ends in.
	a.busy.Add(1)
	defer a.busy.Add(-1)
	// An earlier rollback that did not finish left the appliance in a state that is not its confirmed configuration; a new change on
	// top of it would be applied against an unknown base.
	if m, ok := a.rollbackIncomplete(); ok {
		return nil, fmt.Errorf("%w: %s. Nothing was changed. Restart the network service so it restores the confirmed configuration, "+
			"then apply again", errRollbackIncomplete, m.describe())
	}
	// Refused BEFORE anything is published: a second change must not become what the firewall renders while another is awaiting
	// confirmation.
	if id, state, ierr := a.inFlight(ctx); ierr != nil {
		return nil, fmt.Errorf("cannot tell whether another network change is in flight, so none was started: %w", ierr)
	} else if id != "" {
		return nil, fmt.Errorf("%w: revision %s is %s. Confirm it or roll it back first", errChangeInFlight, id, state)
	}
	intent, err := a.st.LoadIntent(ctx)
	if err != nil {
		return nil, err
	}
	avail, known, err := a.presenceSets(ctx)
	if err != nil {
		return nil, err
	}
	res, vip, err := a.validateWithPortal(ctx, intent, avail, known)
	if err != nil {
		return nil, err
	}
	id, seq, err := a.st.CreateRevision(ctx, summary, intent, actor)
	if err != nil {
		return nil, err
	}
	_ = a.st.SetRevisionValidation(ctx, id, res)
	out := &applyResult{RevisionID: id, Seq: seq, Validation: res}
	if !res.OK {
		_ = a.st.SetRevisionState(ctx, id, "failed")
		out.State = "failed"
		out.Message = "validation failed"
		a.st.Event(ctx, id, "validate", false, map[string]any{"issues": len(res.Issues)})
		return out, nil
	}

	// This intent and portal address are now what every render uses; set before generating so bundle, converge and Kea push agree,
	// restored if nothing reaches the wire.
	before := a.snapshotInForce()
	a.setInForce(intent, vip)
	a.beginApplyInflight(id, before)

	bundle := filepath.Join(a.generatedDir, fmt.Sprintf("revision-%06d", seq))
	if err := a.generate(bundle, intent); err != nil {
		a.restoreInForce(before)
		a.clearApplyInflight()
		a.fail(ctx, id, out, "generate: "+err.Error())
		return out, nil
	}
	a.st.Event(ctx, id, "generate", true, map[string]any{"bundle": bundle, "portal_ip": vip})

	// Pre-apply syntax gate: nft -c and netplan generate (kea config-test runs
	// after bridges exist, inside apply()).
	if !a.dryRun {
		if err := a.run(ctx, "nft", "-c", "-f", filepath.Join(bundle, "stayconnect.nft")); err != nil {
			a.restoreInForce(before)
			a.clearApplyInflight()
			a.fail(ctx, id, out, "nft syntax: "+err.Error())
			return out, nil
		}
	}

	// The confirmation window starts when the apply FINISHES: applyBundle can itself take minutes (bridge waits, Kea wait, restarts),
	// and a deadline computed up front could already be past when MarkPending wrote it.
	// MarkApplying still gets a deadline so the watchdog can reclaim an apply that died mid-flight.
	if err := a.st.MarkApplying(ctx, id, bundle, actor, time.Now().Add(a.confirmWindow)); err != nil {
		// Nothing has reached the wire yet. The intent published above was never proven, so the previous one goes back in force before
		// returning; otherwise the next firewall tick would install a configuration the database refused to record.
		a.restoreInForce(before)
		a.clearApplyInflight()
		_ = a.st.MarkFailed(ctx, id, "could not take the in-flight slot: "+err.Error())
		return nil, err
	}
	a.st.Event(ctx, id, "apply", true, map[string]any{"started": true})

	// Compute the live bridge set to diff against target.
	if err := a.applyBundle(ctx, id, intent, bundle); err != nil {
		a.st.Event(ctx, id, "apply", false, map[string]any{"error": err.Error()})
		out.RollbackIncomplete = a.rollback(ctx, id, "apply failed: "+err.Error())
		out.State = "rolled_back"
		out.Message = rollbackMessage(err.Error(), out.RollbackIncomplete)
		return out, nil
	}

	// Health checks — any critical failure triggers immediate rollback.
	health := a.healthChecks(ctx, id, intent)
	out.Health = health
	for _, h := range health {
		if !h.OK {
			out.RollbackIncomplete = a.rollback(ctx, id, "health check failed: "+h.Name+": "+h.Detail)
			out.State = "rolled_back"
			out.Message = rollbackMessage("health check failed: "+h.Name, out.RollbackIncomplete)
			return out, nil
		}
	}

	// Enter pending_confirmation; the watchdog will roll back if unconfirmed.
	if err := a.st.MarkPending(ctx, id, time.Now().Add(a.confirmWindow)); err != nil {
		// The new configuration IS on the wire but cannot be recorded as awaiting confirmation, so nothing could ever confirm or time it
		// out. It is rolled back rather than left running unrecorded.
		incomplete := a.rollback(ctx, id, "could not record the change as awaiting confirmation: "+err.Error())
		return nil, fmt.Errorf("the change was applied but could not be recorded as awaiting confirmation, so it was rolled back%s: %w",
			rollbackSuffix(incomplete), err)
	}
	out.State = "pending_confirmation"
	out.Message = fmt.Sprintf("applied; confirm within %s or it rolls back automatically", a.confirmWindow)
	return out, nil
}

// Confirm commits a pending revision (cancels the watchdog rollback).
func (a *applier) Confirm(ctx context.Context, id, actor string) error {
	// Confirm and rollback exclude each other: whichever holds the lock first decides, and the other then sees the settled state. Without this a
	// rollback restoring the previous network could race a confirm into the database, leaving the database and the wire disagreeing.
	if !a.acquireOp(ctx, confirmWait) {
		return errApplyBusy
	}
	defer a.releaseOp()
	if m, blocked := a.rollbackIncomplete(); blocked {
		return fmt.Errorf("%w: %s", errRollbackIncomplete, m.describe())
	}
	if state, err := a.revisionState(ctx, id); err != nil {
		return fmt.Errorf("cannot read the state of revision %s: %w", id, err)
	} else if state != "pending_confirmation" {
		return errNotPending
	}
	// A revision is confirmed only if it is what the network is running now. After a restart inside the confirm window the boot reconcile has put
	// the previous configuration back, and confirming then would record as active a configuration that is not running.
	if err := a.runningIs(ctx, id); err != nil {
		return err
	}
	if err := a.st.MarkActive(ctx, id, actor); err != nil {
		return err
	}
	// The address now in force is the confirmed one: it is what a later rollback returns to when the previous
	// bundle records none.
	confirmedIntent, vip, _ := a.inForce()
	a.setActiveVIP(vip)
	_, bundle, _, _ := a.currentActiveIntent(ctx)
	a.keepLastConfirmedCurrent(id, bundle, vip, confirmedIntent)
	a.clearApplyInflight()
	// The configuration just confirmed is now the one a cold Kea start must come up with. The confirmation itself stands either way; a failed
	// write is reported through /v1/health and retried by the portal refresh, never silently dropped.
	if perr := a.persistConfirmedKea(ctx, id); perr != nil {
		slog.Error("netd confirmed the revision but could not save its DHCP configuration to disk; Kea would start from an older file", "revision", id, "err", perr)
	}
	// Confirming is the operator saying to keep it, so the pre-bootstrap Kea snapshot is discarded: a later
	// rollback to a DIFFERENT revision must not put a confirmed appliance back to stopped-and-disabled.
	a.clearKeaSnapshot()
	return nil
}

// errRollbackRefused marks a rollback the appliance declined on purpose, so the transport answers 409 instead of 500.
var errRollbackRefused = errors.New("rollback refused")

// Rollback is the OPERATOR-requested rollback; the automatic one calls a.rollback directly.
// a.rollback looks for a target with ActiveBundlePath(exceptID); once a revision is CONFIRMED it is the only active row, so that finds nothing
// and takes the factory-clean branch (every guest bridge destroyed, Kea stopped). That is right only for a failed or expired FIRST apply, not for "undo what I confirmed".
// A confirmed revision has no stored predecessor (previous_seq is never written), so the honest outcome is a refusal naming the supported path.
func (a *applier) Rollback(ctx context.Context, id, actor string) error {
	state, err := a.revisionState(ctx, id)
	if err != nil {
		// Fail closed. Not knowing the state is not a reason to run the branch that destroys everything.
		return fmt.Errorf("cannot determine the state of revision %s, so cannot determine whether a "+
			"rollback has a target: %w -- refusing rather than guessing", id, err)
	}
	// Every refusal wraps errRollbackRefused so the transport answers 409, not 500: the appliance protected the guest network.
	switch state {
	case "applying", "pending_confirmation":
		// The real rollback window. The previous revision is still active and is a genuine target; if there
		// is none, this is the first apply and coming back factory-clean is correct.
		if !a.acquireOp(ctx, opWait) {
			return errApplyBusy
		}
		defer a.releaseOp()
		if steps := a.rollback(ctx, id, "operator requested rollback"); len(steps) > 0 {
			return fmt.Errorf("%w: the rollback ran but did not restore everything: %s", errRollbackIncomplete, strings.Join(steps, "; "))
		}
		return nil
	case "":
		return fmt.Errorf("%w: revision %s does not exist", errRollbackRefused, id)
	case "active":
		return fmt.Errorf("%w: revision %s is the CONFIRMED active configuration and cannot be rolled back: "+
			"its predecessor is superseded, so there is no stored configuration to return to, and the only "+
			"code path left would tear down every guest network and stop DHCP. Edit the networks to the "+
			"state you want and apply that as a new revision", errRollbackRefused, id)
	default:
		// failed, rolled_back, superseded, draft, validated: not in force, so there is nothing to undo.
		return fmt.Errorf("%w: revision %s is %s and is not in force; there is nothing to roll back",
			errRollbackRefused, id, state)
	}
}

// validateWithPortal is ValidateSet plus the portal address the apply would put in force, read at the moment of the decision and checked
// against this intent and every address the appliance carries. Fails closed: an unreadable setting or address list is an error, not a pass.
func (a *applier) validateWithPortal(ctx context.Context, intent []netcfg.GuestNetwork, avail, known map[string]bool) (netcfg.ValidationResult, string, error) {
	vip, err := a.loadPortalIP(ctx)
	if err != nil {
		return netcfg.ValidationResult{}, "", fmt.Errorf("cannot read the portal address setting: %w", err)
	}
	a.refreshPortalTLS(ctx)
	topo := a.renderTopo()
	topo.PortalVIP = vip
	res := netcfg.ValidateSet(intent, topo, avail, known)
	issues, err := a.validatePortal(ctx, vip, intent)
	if err != nil {
		return res, vip, err
	}
	if len(issues) > 0 {
		res.OK = false
		res.Issues = append(res.Issues, issues...)
	}
	return res, vip, nil
}

func (a *applier) fail(ctx context.Context, id string, out *applyResult, reason string) {
	_ = a.st.MarkFailed(ctx, id, reason)
	a.st.Event(ctx, id, "generate", false, map[string]any{"error": reason})
	out.State = "failed"
	out.Message = reason
}

func (a *applier) generate(bundle string, intent []netcfg.GuestNetwork) error {
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return err
	}
	topo := a.renderTopo()
	files := map[string][]byte{
		"stayconnect.nft":           netcfg.RenderNftables(intent, topo),
		"50-stayconnect-guest.yaml": netcfg.RenderNetplan(a.netdManaged(intent)),
		"stayconnect-guest.conf":    netcfg.RenderUnbound(intent),
		// The portal address this revision is applied with ("" = none). A rollback to this revision, and a
		// boot reconcile of it, restore exactly this address.
		netcfg.PortalVIPBundleFile: []byte(strings.TrimSpace(topo.PortalVIP) + "\n"),
	}
	keaBytes, err := netcfg.RenderKeaFile(intent, topo, a.keaLeaseCSV, a.keaSocket)
	if err != nil {
		return err
	}
	files["kea-dhcp4.json"] = keaBytes
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(bundle, name), data, 0o640); err != nil {
			return err
		}
	}
	return nil
}

// netdManaged returns the networks netd surgically manages (excludes the
// adopted legacy bridge, whose L2/L3 lives in the base netplan).
func (a *applier) netdManaged(intent []netcfg.GuestNetwork) []netcfg.GuestNetwork {
	out := make([]netcfg.GuestNetwork, 0, len(intent))
	for _, n := range intent {
		if n.BridgeName == a.legacyBridge {
			continue
		}
		out = append(out, n)
	}
	return out
}

func (a *applier) run(ctx context.Context, name string, args ...string) error {
	if a.runFn != nil {
		return a.runFn(ctx, name, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v — %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// output runs a command and returns stdout. A non-zero exit is an error and the caller decides what it means —
// `nft list table` failing because the table does not exist is a normal state, not a fault.
func (a *applier) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if a.outFn != nil {
		return a.outFn(ctx, name, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, name, args...).Output()
}

// presenceSets reads what the appliance HAS NOW and what it has EVER recorded; "never existed" (a typo) and "existed and is gone"
// (pulled card, destroyed bridge) are both refused, but the message says which.
func (a *applier) presenceSets(ctx context.Context) (present, known map[string]bool, err error) {
	// Presence is observed from the kernel at the moment of the decision, not derived from the inventory: a relative last_seen_at comparison
	// fails open when netd stops sweeping (the whole table freezes together), and an absolute age bound would refuse applies on a healthy idle
	// appliance (sweeps happen only at boot and on GET /v1/interfaces). Fail closed: if discovery fails the error is returned, never a fallback to stored data.
	ifaces, err := a.discover(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot determine which interfaces are present: %w — refusing to "+
			"validate against a stale inventory", err)
	}
	present = make(map[string]bool, len(ifaces))
	for _, f := range ifaces {
		present[f.Name] = true
	}

	// The inventory still answers what the appliance has EVER recorded, which separates a never-had name from a lost one;
	// advisory, so an unreadable inventory only loses that distinction.
	known, err = a.knownIfaces(ctx)
	if err != nil {
		slog.Warn("interface inventory unreadable; an absent parent will still be refused, but the message "+
			"cannot distinguish 'never had one' from 'had one and lost it'", "err", err)
		known = nil
	}

	// Refresh the inventory opportunistically; a failure does not affect the decision.
	if err := a.syncInventory(ctx, ifaces); err != nil {
		slog.Error("interface inventory refresh failed during validation; the decision is unaffected because "+
			"presence is observed live, but the inventory is now stale for the UI and for audit", "err", err)
	}
	return present, known, nil
}

// knownIfaces reports what the appliance has EVER recorded. Advisory: it only sharpens the message.
func (a *applier) knownIfaces(ctx context.Context) (map[string]bool, error) {
	if a.knownFn != nil {
		return a.knownFn(ctx)
	}
	if a.st == nil {
		return nil, nil
	}
	return a.st.KnownIfaceSet(ctx)
}

// syncInventory refreshes the inventory from a sweep that has already happened. Advisory: the decision does
// not depend on it.
func (a *applier) syncInventory(ctx context.Context, ifaces []Interface) error {
	if a.syncFn != nil {
		return a.syncFn(ctx, ifaces)
	}
	if a.st == nil {
		return nil
	}
	return a.st.SyncInterfaces(ctx, ifaces, a.topo.MgmtInterface, a.topo.WANInterface)
}

// discover is the discovery seam. Production uses Discover(); a test substitutes it to state exactly which
// interfaces exist at the moment of the decision, including none at all and an outright failure.
func (a *applier) discover(ctx context.Context) ([]Interface, error) {
	if a.discoverFn != nil {
		return a.discoverFn(ctx)
	}
	return Discover(ctx)
}

// rollbackMessage is the operator-facing outcome: the cause, and the unfinished steps when the rollback did not restore everything.
func rollbackMessage(cause string, incomplete []string) string {
	return cause + rollbackSuffix(incomplete)
}

func rollbackSuffix(incomplete []string) string {
	if len(incomplete) == 0 {
		return ""
	}
	return " -- ROLLBACK INCOMPLETE, the appliance may not be on its confirmed configuration: " + strings.Join(incomplete, "; ")
}
