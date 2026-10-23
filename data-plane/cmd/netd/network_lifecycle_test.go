package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// Boot reconciliation must use the confirmed active revision's immutable intent snapshot, never the live
// guest_networks rows: those are unapplied drafts, and reconciling from them would apply an unconfirmed edit
// at reboot, or resurrect a rolled-back change, bypassing apply, health check, confirmation and watchdog.

// draftIntent is an unapplied Hotel-Admin edit, rendering differently from the confirmed one.
func draftIntent() []netcfg.GuestNetwork {
	n := testIntent()
	n[0].Name, n[0].BridgeName = "unapplied-draft", "br-draft"
	n[0].GatewayIP, n[0].SubnetCIDR = "10.99.0.1", "10.99.0.0/22"
	return n
}

// confirmedIntent is the applied and confirmed revision.
func confirmedIntent() []netcfg.GuestNetwork { return testIntent() }

type fakeRevisions struct {
	id      string
	bundle  string
	intent  []netcfg.GuestNetwork
	err     error
	prev    []netcfg.GuestNetwork
	prevErr error
	events  []string
	bundle_ string // previous revision's stored bundle directory; nothing in it may be executed
}

func (r *fakeRevisions) active(context.Context) (string, string, []netcfg.GuestNetwork, error) {
	return r.id, r.bundle, r.intent, r.err
}

func (r *fakeRevisions) previous(_ context.Context, _ string) ([]netcfg.GuestNetwork, error) {
	return r.prev, r.prevErr
}

func (r *fakeRevisions) record(_ context.Context, revID, kind string, ok bool, detail map[string]any) {
	r.events = append(r.events, fmt.Sprintf("%s/%s=%v %v", revID, kind, ok, detail))
}

func newLifecycleApplier(t *testing.T, k *fakeKernel, rev *fakeRevisions) *applier {
	t.Helper()
	a := newTestApplier(t, k)
	a.activeIntentFn = rev.active
	a.prevIntentFn = rev.previous
	a.eventFn = rev.record
	return a
}

// With an unapplied draft present, a reboot must still produce the confirmed revision.
func TestBootReconcile_UnappliedDraftDoesNotBecomeTheRuntime(t *testing.T) {
	k := newFakeKernel(t) // a reboot: no table
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent()}
	a := newLifecycleApplier(t, k, rev)

	draftFP := netcfg.RenderFingerprint(draftIntent(), a.topo)
	confirmedFP := netcfg.RenderFingerprint(confirmedIntent(), a.topo)
	if draftFP == confirmedFP {
		t.Fatal("the draft and confirmed intents render identically; this test would prove nothing")
	}

	a.ReconcileActiveOnBoot(context.Background())

	if k.fp != confirmedFP {
		t.Fatalf("boot reconstructed fingerprint %q; the confirmed revision renders %q", k.fp, confirmedFP)
	}
	if k.fp == draftFP {
		t.Fatal("an unapplied Hotel-Admin draft became the running network after a reboot")
	}
	if _, ok := k.sets["phase3_auth_ipv4"]; !ok {
		t.Fatal("the reconstruction did not produce the current structure")
	}
}

// Repeated restarts keep the confirmed revision as a no-op.
func TestBootReconcile_RepeatedRestartsStayOnTheConfirmedRevision(t *testing.T) {
	k := newFakeKernel(t)
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent()}
	a := newLifecycleApplier(t, k, rev)
	confirmedFP := netcfg.RenderFingerprint(confirmedIntent(), a.topo)

	a.ReconcileActiveOnBoot(context.Background())
	for i := 0; i < 3; i++ {
		k.reset()
		a.ReconcileActiveOnBoot(context.Background())
		if k.fp != confirmedFP {
			t.Fatalf("restart %d drifted off the confirmed revision", i)
		}
		if m := k.mutations(); len(m) != 0 {
			t.Fatalf("restart %d rewrote the ruleset: %v", i, m)
		}
	}
}

// Only a confirmed apply (a new confirmed revision in the store) changes the active network.
func TestBootReconcile_OnlyAConfirmedApplyChangesTheActiveNetwork(t *testing.T) {
	k := newFakeKernel(t)
	rev := &fakeRevisions{id: "rev-1", intent: confirmedIntent()}
	a := newLifecycleApplier(t, k, rev)

	a.ReconcileActiveOnBoot(context.Background())
	first := k.fp

	for i := 0; i < 2; i++ {
		a.ReconcileActiveOnBoot(context.Background())
		if k.fp != first {
			t.Fatal("the active network changed without an apply/confirm")
		}
	}

	rev.id, rev.intent = "rev-2", draftIntent()
	k.reset()
	a.ReconcileActiveOnBoot(context.Background())

	want := netcfg.RenderFingerprint(draftIntent(), a.topo)
	if k.fp != want {
		t.Fatalf("after a confirmed apply the runtime is %q, want %q", k.fp, want)
	}
	if len(k.mutations()) == 0 {
		t.Fatal("a genuinely new confirmed revision did not converge the ruleset")
	}
}

// An unreadable intent snapshot must leave the live ruleset alone, not fall back to the mutable rows.
func TestBootReconcile_UnreadableSnapshotLeavesTheLiveRulesetAlone(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.5")
	before := k.fp
	rev := &fakeRevisions{id: "rev-broken", err: errors.New("active revision rev-broken has unreadable intent")}
	a := newLifecycleApplier(t, k, rev)

	a.ReconcileActiveOnBoot(context.Background())

	if len(k.mutations()) != 0 {
		t.Fatalf("the ruleset was rebuilt from something other than the confirmed snapshot: %v", k.mutations())
	}
	if k.fp != before {
		t.Fatal("the live ruleset changed despite an unreadable revision snapshot")
	}
	if !k.has("auth_ipv4", "br-g90|10.20.0.5") {
		t.Fatal("a live guest lost authorization while the snapshot was unreadable")
	}
	if len(rev.events) == 0 || !strings.Contains(strings.Join(rev.events, " "), "boot_reconcile") {
		t.Fatal("the failure was not recorded")
	}
}

// An appliance that never confirmed a revision does nothing quietly and does not adopt the draft rows.
func TestBootReconcile_NoConfirmedRevisionDoesNothing(t *testing.T) {
	k := newFakeKernel(t)
	rev := &fakeRevisions{err: ErrNoConfirmedActiveRevision}
	a := newLifecycleApplier(t, k, rev)

	a.ReconcileActiveOnBoot(context.Background())

	if len(k.mutations()) != 0 {
		t.Fatal("an appliance with no confirmed revision built a ruleset anyway")
	}
	for _, e := range rev.events {
		if strings.Contains(e, "boot_reconcile=false") {
			t.Fatalf("a never-configured appliance recorded a failure: %s", e)
		}
	}
}

// Rollback must never fall back to `nft -f <prevBundle>/stayconnect.nft`: that earlier-binary file begins with
// `delete table inet stayconnect` and would delete every authorization set and structure the current software needs.

func rollbackHarness(t *testing.T, rev *fakeRevisions) (*applier, *fakeKernel) {
	t.Helper()
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.9")
	a := newLifecycleApplier(t, k, rev)
	a.legacyBridge = "br-lan"
	// A previous bundle directory with a stored stayconnect.nft exists.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/stayconnect.nft", []byte(
		"table inet stayconnect\ndelete table inet stayconnect\ntable inet stayconnect {\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rev.bundle_ = dir
	a.prevBundleFn = func(context.Context, string) (string, error) { return dir, nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
	// Rollback re-reads the revision's state on every branch; these cases are for a revision still awaiting confirmation.
	a.revStateFn = func(context.Context, string) (string, error) { return "pending_confirmation", nil }
	a.netplanFile = dir + "/50-stayconnect-guest.yaml"
	a.unboundFrag = dir + "/stayconnect-guest.conf"
	return a, k
}

func TestRollback_NeverExecutesAStoredRulesetFile(t *testing.T) {
	cases := []struct {
		name string
		rev  *fakeRevisions
	}{
		{"the previous intent cannot be read", &fakeRevisions{prevErr: errors.New("intent unreadable")}},
		{"there is no previous confirmed revision", &fakeRevisions{prev: nil}},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			a, k := rollbackHarness(t, tc.rev)
			before := len(k.sets["auth_ipv4"])

			a.rollback(context.Background(), "failed-rev", "health check failed")

			for _, c := range k.cmds {
				if strings.Contains(c, tc.rev.bundle_) {
					t.Fatalf("rollback touched the stored bundle: %s", c)
				}
			}
			if len(k.sets["auth_ipv4"]) != before {
				t.Fatal("rollback deauthorized live guests")
			}
			// and it must SAY it could not roll the structure back
			joined := strings.Join(tc.rev.events, " | ")
			if !strings.Contains(joined, "rollback_nft") || !strings.Contains(joined, "blocker") {
				t.Fatalf("the nft rollback blocker was not reported: %s", joined)
			}
			if !strings.Contains(joined, "needs operator attention") {
				t.Fatalf("the report does not say the ruleset still needs attention: %s", joined)
			}
		})
	}
}

func TestRollback_RendersThePreviousConfirmedIntent(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)

	a.rollback(context.Background(), "failed-rev", "health check failed")

	want := netcfg.RenderFingerprint(confirmedIntent(), a.topo)
	if k.fp != want {
		t.Fatalf("rollback left fingerprint %q, want the previous revision's %q", k.fp, want)
	}
	if !k.has("auth_ipv4", "br-g90|10.20.0.9") {
		t.Fatal("rollback deauthorized a live guest")
	}
	joined := strings.Join(rev.events, " | ")
	if !strings.Contains(joined, "rollback_nft=true") {
		t.Fatalf("a successful nft rollback was not recorded: %s", joined)
	}
}

// A failed safe reconciliation must not degrade into the legacy path.
func TestRollback_AFailedSafeReconciliationDoesNotDegradeToTheLegacyPath(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)
	k.failNftF = true

	a.rollback(context.Background(), "failed-rev", "health check failed")

	// The safe path attempts its own render (current-stayconnect.nft); only executing the previous bundle's file is forbidden.
	for _, c := range k.cmds {
		if strings.Contains(c, rev.bundle_) {
			t.Fatalf("a failed safe reconciliation degraded into the stored-file path: %s", c)
		}
	}
	joined := strings.Join(rev.events, " | ")
	if !strings.Contains(joined, "safe reconciliation to the previous revision failed") {
		t.Fatalf("the blocker was not reported: %s", joined)
	}
}

// A rollback rebuilds a bridge the failed apply destroyed (the previous intent names br-g90, not live here) and says so.
func TestRollback_RebuildsABridgeTheFailedApplyRemoved(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{} }

	a.rollback(context.Background(), "failed-rev", "confirmation expired")

	rebuilt := false
	for _, c := range k.cmds {
		if strings.Contains(c, "link add") && strings.Contains(c, "br-g90") {
			rebuilt = true
		}
	}
	if !rebuilt {
		t.Fatalf("the previous revision's bridge br-g90 was not rebuilt: %v", k.cmds)
	}
	joined := strings.Join(rev.events, " | ")
	if !strings.Contains(joined, "rollback_bridges=true") || !strings.Contains(joined, "br-g90") {
		t.Fatalf("the rebuild was not recorded: %s", joined)
	}
}

// A bridge that is already live is left alone.
func TestRollback_LeavesALiveBridgeAlone(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true} }

	a.rollback(context.Background(), "failed-rev", "confirmation expired")

	for _, c := range k.cmds {
		if strings.Contains(c, "link add") && strings.Contains(c, "br-g90") {
			t.Fatalf("a live bridge was rebuilt: %s", c)
		}
	}
	if strings.Contains(strings.Join(rev.events, " | "), "rollback_bridges") {
		t.Fatal("nothing was rebuilt, so no rebuild event may be recorded")
	}
}

// The guest resolver fragment is rebuilt from the current renderer on boot, not replayed from the stored bundle,
// so an upgrade takes effect at the next start.
func TestBootReconcile_RendersTheResolverFragmentFromTheCurrentRenderer(t *testing.T) {
	k := newFakeKernel(t)
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/stayconnect-guest.conf", []byte("# an older binary's fragment\nserver:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent(), bundle: dir}
	a := newLifecycleApplier(t, k, rev)
	a.unboundFrag = t.TempDir() + "/live.conf"
	a.ReconcileActiveOnBoot(context.Background())
	want := string(netcfg.RenderUnbound(confirmedIntent()))
	for _, p := range []string{a.unboundFrag, dir + "/stayconnect-guest.conf"} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("%s is not the current rendering:\n%s", p, got)
		}
	}
	if !strings.Contains(want, "ip-ratelimit: 100") {
		t.Fatal("the current rendering carries the DNS limits")
	}
}
