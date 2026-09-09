package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// applier owns the transactional apply/rollback of guest-network config. It
// applies L2/L3 SURGICALLY with `ip` commands (additive, reversible, never
// touching the management/WAN/legacy interfaces) and writes netplan only for
// reboot persistence. nftables + Kea + Unbound are rendered from the DB and
// swapped atomically. A watchdog auto-rolls-back if the operator does not
// confirm within confirmWindow.
type applier struct {
	st            *store
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

	// runFn/outFn are the ONLY places netd shells out. They are fields so a test can observe exactly which
	// commands a reconciliation issued — including, for the steady-state case, that it issued none. A test
	// that could only inspect the resulting ruleset could not tell "already correct, did nothing" apart from
	// "rewrote it to the same thing", and those differ by whether a live guest keeps their authorization.
	runFn func(ctx context.Context, name string, args ...string) error
	outFn func(ctx context.Context, name string, args ...string) ([]byte, error)

	// discoverFn is the interface-discovery seam. Presence is observed live at decision time, so a test has
	// to be able to say "this interface is gone now" without unplugging anything.
	discoverFn func(ctx context.Context) ([]Interface, error)
	// knownFn / syncFn are the inventory seams beside it: what this appliance has EVER recorded, and the
	// opportunistic refresh. Both are advisory to the decision -- presence does not depend on either -- so a
	// test can state them, or leave them out entirely.
	knownFn func(ctx context.Context) (map[string]bool, error)
	syncFn  func(ctx context.Context, ifaces []Interface) error

	// activeIntentFn / prevIntentFn / eventFn are the database seams the lifecycle paths use. They exist so a
	// test can state, without a database, exactly what the CONFIRMED active revision holds and what the
	// mutable guest_networks rows hold — which is the whole question item 2 is about: a restart must
	// reconstruct the confirmed revision, never an unapplied draft.
	activeIntentFn func(ctx context.Context) (string, string, []netcfg.GuestNetwork, error)
	prevIntentFn   func(ctx context.Context, exceptID string) ([]netcfg.GuestNetwork, error)
	eventFn        func(ctx context.Context, revID, kind string, ok bool, detail map[string]any)
	prevBundleFn   func(ctx context.Context, exceptID string) (string, error)
	markRolledFn   func(ctx context.Context, id, reason string) error
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
	res := netcfg.ValidateSet(intent, a.topo, avail, known)
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
	intent, err := a.st.LoadIntent(ctx)
	if err != nil {
		return nil, err
	}
	avail, known, err := a.presenceSets(ctx)
	if err != nil {
		return nil, err
	}
	res := netcfg.ValidateSet(intent, a.topo, avail, known)
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

	bundle := filepath.Join(a.generatedDir, fmt.Sprintf("revision-%06d", seq))
	if err := a.generate(bundle, intent); err != nil {
		a.fail(ctx, id, out, "generate: "+err.Error())
		return out, nil
	}
	a.st.Event(ctx, id, "generate", true, map[string]any{"bundle": bundle})

	// Pre-apply syntax gate: nft -c and netplan generate (kea config-test runs
	// after bridges exist, inside apply()).
	if !a.dryRun {
		if err := a.run(ctx, "nft", "-c", "-f", filepath.Join(bundle, "stayconnect.nft")); err != nil {
			a.fail(ctx, id, out, "nft syntax: "+err.Error())
			return out, nil
		}
	}

	deadline := time.Now().Add(a.confirmWindow)
	if err := a.st.MarkApplying(ctx, id, bundle, actor, deadline); err != nil {
		return nil, err
	}
	a.st.Event(ctx, id, "apply", true, map[string]any{"started": true})

	// Compute the live bridge set to diff against target.
	if err := a.applyBundle(ctx, id, intent, bundle); err != nil {
		a.st.Event(ctx, id, "apply", false, map[string]any{"error": err.Error()})
		a.rollback(ctx, id, "apply failed: "+err.Error())
		out.State = "rolled_back"
		out.Message = err.Error()
		return out, nil
	}

	// Health checks — any critical failure triggers immediate rollback.
	health := a.healthChecks(ctx, id, intent)
	out.Health = health
	for _, h := range health {
		if !h.OK {
			a.rollback(ctx, id, "health check failed: "+h.Name+": "+h.Detail)
			out.State = "rolled_back"
			out.Message = "health check failed: " + h.Name
			return out, nil
		}
	}

	// Enter pending_confirmation; the watchdog will roll back if unconfirmed.
	if err := a.st.MarkPending(ctx, id, deadline); err != nil {
		return nil, err
	}
	out.State = "pending_confirmation"
	out.Message = fmt.Sprintf("applied; confirm within %s or it rolls back automatically", a.confirmWindow)
	return out, nil
}

// Confirm commits a pending revision (cancels the watchdog rollback).
func (a *applier) Confirm(ctx context.Context, id, actor string) error {
	if err := a.st.MarkActive(ctx, id, actor); err != nil {
		return err
	}
	// Confirming is the operator saying to keep it, so the pre-bootstrap Kea snapshot is discarded: a later
	// rollback to a DIFFERENT revision must not put a confirmed appliance back to stopped-and-disabled.
	a.clearKeaSnapshot()
	return nil
}

// Rollback restores the previous active revision on operator request.
func (a *applier) Rollback(ctx context.Context, id, actor string) error {
	a.rollback(ctx, id, "operator requested rollback")
	return nil
}

func (a *applier) fail(ctx context.Context, id string, out *applyResult, reason string) {
	_ = a.st.MarkFailed(ctx, id, reason)
	a.st.Event(ctx, id, "generate", false, map[string]any{"error": reason})
	out.State = "failed"
	out.Message = reason
}

// generate renders the full bundle to disk.
func (a *applier) generate(bundle string, intent []netcfg.GuestNetwork) error {
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return err
	}
	files := map[string][]byte{
		"stayconnect.nft":           netcfg.RenderNftables(intent, a.topo),
		"50-stayconnect-guest.yaml": netcfg.RenderNetplan(a.netdManaged(intent)),
		"stayconnect-guest.conf":    netcfg.RenderUnbound(intent),
	}
	keaBytes, err := netcfg.RenderKeaFile(intent, a.topo, a.keaLeaseCSV, a.keaSocket)
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

// presenceSets reads what the appliance HAS NOW and what it has EVER recorded.
//
// Two sets rather than one, because "this interface does not exist" and "this interface existed and is gone"
// are different operator problems: the first is usually a typo, the second is a pulled card, an unloaded
// driver or a destroyed bridge. Validation refuses both — a guest network cannot come up on either — but the
// message says which.
func (a *applier) presenceSets(ctx context.Context) (present, known map[string]bool, err error) {
	// PRESENCE IS OBSERVED AT THE MOMENT OF THE DECISION, FROM THE KERNEL.
	//
	// It used to be derived from the inventory: each row's last_seen_at compared against the newest one in
	// the table. That is a RELATIVE reference, and it fails open in exactly the case that matters. If netd
	// stops sweeping -- discovery broken, the database refusing writes, the process wedged -- the whole table
	// freezes TOGETHER, every surviving row stays within the tolerance of the newest one, and an interface
	// that vanished afterwards reads as present indefinitely. The stored observations stay internally
	// consistent while being collectively wrong, which is the hardest kind of stale to notice.
	//
	// An absolute age bound on the stored observation cannot fix that either, because a sweep only happens at
	// boot and on GET /v1/interfaces -- there is no periodic refresh. On a healthy appliance nobody has
	// touched for a day, every stored observation is legitimately a day old, and any bound tight enough to
	// catch a wedged netd would refuse applies on an appliance with nothing wrong with it.
	//
	// So the question is not asked of the database at all. netd is the process that can SEE the interfaces;
	// it does not need to be told which ones exist. Discovering here removes the freshness problem rather
	// than parameterising it: the observation is taken when the decision is made, so it cannot be stale.
	//
	// FAIL CLOSED. If discovery fails, presence cannot be established, and the error is returned rather than
	// falling back to whatever the inventory last recorded. A fallback to stored data is precisely the
	// fail-open this replaces.
	ifaces, err := a.discover(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot determine which interfaces are present: %w — refusing to "+
			"validate against a stale inventory", err)
	}
	present = make(map[string]bool, len(ifaces))
	for _, f := range ifaces {
		present[f.Name] = true
	}

	// The inventory still answers a different question: what has this appliance EVER recorded. That is what
	// lets the message separate a name the appliance has never had from one it had and lost. It is advisory
	// -- if it cannot be read, the check still refuses an absent parent, it just cannot say which kind.
	known, err = a.knownIfaces(ctx)
	if err != nil {
		slog.Warn("interface inventory unreadable; an absent parent will still be refused, but the message "+
			"cannot distinguish 'never had one' from 'had one and lost it'", "err", err)
		known = nil
	}

	// Refresh the inventory opportunistically, since discovery just ran. A failure here does NOT affect the
	// decision above -- presence no longer depends on this table -- so it is logged and carried on from.
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
