package main

import (
	"context"
	"fmt"
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
	st           *store
	kea          *keaClient
	topo         netcfg.Topology
	generatedDir string // /etc/stayconnect/generated/network
	netplanFile  string // /etc/netplan/50-stayconnect-guest.yaml
	nftPath      string
	unboundFrag  string // /etc/unbound/unbound.conf.d/stayconnect-guest.conf
	keaLeaseCSV  string
	keaSocket    string
	// keaConfFile / keaUnit are used ONLY to bootstrap Kea the first time guest networking is configured:
	// it cannot be reached over its control socket until it is running, and it cannot run until it has a
	// config file. Afterwards every change goes through the socket and neither of these is touched.
	keaConfFile   string
	keaUnit       string
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
	avail, err := a.st.AvailableIfaceSet(ctx)
	if err != nil {
		return nil, err
	}
	res := netcfg.ValidateSet(intent, a.topo, avail)
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
	avail, err := a.st.AvailableIfaceSet(ctx)
	if err != nil {
		return nil, err
	}
	res := netcfg.ValidateSet(intent, a.topo, avail)
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
	return a.st.MarkActive(ctx, id, actor)
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
