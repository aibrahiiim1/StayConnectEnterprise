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

// ---- BOOT RECONCILIATION MUST USE THE CONFIRMED ACTIVE REVISION -------------------------------------------
//
// THE FAILURE THIS PREVENTS. netd used to reconcile on boot from `LoadIntent`, which reads the live
// `guest_networks` rows — the ones the Hotel-Admin UI edits directly. Those edits are DRAFTS: they become the
// appliance's configuration only when an operator Applies them and then Confirms inside the watchdog window,
// which is the entire point of the validate → apply → pending_confirmation → active pipeline and of the
// automatic rollback when confirmation never arrives.
//
// Reconciling from those rows on boot would bypass all of it. Two concrete consequences:
//
//	an operator edits a VLAN, is called away, and never applies it — the edit takes effect at the next reboot,
//	with no apply record, no health check, no confirmation and no watchdog;
//	a change that was ROLLED BACK precisely because it broke connectivity comes back by itself the next time
//	the appliance restarts, because the draft rows were never reverted.
//
// The revision row already stores the exact intent it was applied with. That snapshot is immutable, and it is
// what "the active network configuration" means.

// draftIntent is what an operator has typed into Hotel-Admin but never applied: a different bridge, subnet and
// gateway from the confirmed one, so a render from it is unmistakable.
func draftIntent() []netcfg.GuestNetwork {
	n := testIntent()
	n[0].Name, n[0].BridgeName = "unapplied-draft", "br-draft"
	n[0].GatewayIP, n[0].SubnetCIDR = "10.99.0.1", "10.99.0.0/22"
	return n
}

// confirmedIntent is the revision that was applied AND confirmed — i.e. what the appliance actually runs.
func confirmedIntent() []netcfg.GuestNetwork { return testIntent() }

type fakeRevisions struct {
	id      string
	bundle  string
	intent  []netcfg.GuestNetwork
	err     error
	prev    []netcfg.GuestNetwork
	prevErr error
	events  []string
	bundle_ string // the previous revision's stored bundle DIRECTORY — nothing in it may ever be executed
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

// THE CENTRAL CASE: an unapplied draft exists, the appliance reboots, and the runtime must still be the
// confirmed revision.
func TestBootReconcile_UnappliedDraftDoesNotBecomeTheRuntime(t *testing.T) {
	k := newFakeKernel(t) // a reboot: no table at all
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent()}
	a := newLifecycleApplier(t, k, rev)

	// the operator's unapplied edit is live in guest_networks, and must be irrelevant here
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

// REPEATED restarts keep the confirmed revision, and the second one is a no-op.
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

// ONLY AN EXPLICIT SUCCESSFUL APPLY/CONFIRM CHANGES THE ACTIVE NETWORK. Modelled as the revision store now
// returning a NEW confirmed revision — which is the only thing that happens in the database when an operator
// applies and confirms.
func TestBootReconcile_OnlyAConfirmedApplyChangesTheActiveNetwork(t *testing.T) {
	k := newFakeKernel(t)
	rev := &fakeRevisions{id: "rev-1", intent: confirmedIntent()}
	a := newLifecycleApplier(t, k, rev)

	a.ReconcileActiveOnBoot(context.Background())
	first := k.fp

	// the operator edits Hotel-Admin repeatedly and reboots repeatedly: nothing changes
	for i := 0; i < 2; i++ {
		a.ReconcileActiveOnBoot(context.Background())
		if k.fp != first {
			t.Fatal("the active network changed without an apply/confirm")
		}
	}

	// now an apply IS confirmed: a new active revision, carrying the intent that was applied
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

// A revision whose intent snapshot cannot be read must leave the live ruleset alone rather than fall back to
// the mutable rows — falling back is the defect, not the recovery.
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

// AN APPLIANCE THAT HAS NEVER CONFIRMED A REVISION has no network configuration to reconstruct. It must do
// nothing quietly — not fail, and certainly not adopt the draft rows.
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

// ---- ROLLBACK MUST NEVER FALL BACK TO THE STORED RULESET FILE ---------------------------------------------
//
// THE FAILURE THIS PREVENTS. Rollback used to drop back to `nft -f <prevBundle>/stayconnect.nft` whenever the
// safe path could not be completed — so the worst moment (a failed apply, on a live appliance, with the
// operator already in trouble) was the one moment the code chose to execute a full-table replacement rendered
// by some earlier binary. That file begins with `delete table inet stayconnect`: it deletes every
// authorization set and every structure the current software requires. It is the exact artifact that caused
// the Live Increment-9 blocker.

func rollbackHarness(t *testing.T, rev *fakeRevisions) (*applier, *fakeKernel) {
	t.Helper()
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.9")
	a := newLifecycleApplier(t, k, rev)
	a.legacyBridge = "br-lan"
	// A previous bundle DIRECTORY exists — that is the realistic case, and it is precisely the case in which
	// the old code would have executed the stored stayconnect.nft inside it.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/stayconnect.nft", []byte(
		"table inet stayconnect\ndelete table inet stayconnect\ntable inet stayconnect {\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rev.bundle_ = dir
	a.prevBundleFn = func(context.Context, string) (string, error) { return dir, nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
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

// When the safe path CAN be completed, rollback really does restore the previous revision's structure.
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

// A rollback whose safe reconciliation FAILS mid-way must also refuse the legacy path, not "recover" into it.
func TestRollback_AFailedSafeReconciliationDoesNotDegradeToTheLegacyPath(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)
	k.failNftF = true // the safe converge cannot complete

	a.rollback(context.Background(), "failed-rev", "health check failed")

	// The safe path legitimately writes and attempts its OWN render (current-stayconnect.nft) and that
	// attempt failed. What must not happen is the fallback: executing the previous bundle's stored file.
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
