package main

// WHAT NETD MAY NOT DO QUIETLY.
//
// Every case here is a path on which the live Linux network and the recorded revision could end up describing
// different things while netd reported success. They are grouped because they share one shape: a step that
// mutated the wire, discarded its error, and let the revision commit anyway.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// ---- the confirm/watchdog race -----------------------------------------------------------------------------

// A ROLLBACK MAY NOT RUN AGAINST A REVISION SOMEBODY HAS JUST CONFIRMED, ON EITHER BRANCH.
//
// The guard for this existed but sat INSIDE the factory-clean branch, so it only ran on an appliance that had
// never applied anything. Every appliance with an earlier revision took the ordinary branch with no state check
// at all, and the watchdog could then roll back a revision the operator had confirmed a millisecond earlier:
// bridges destroyed, nothing rebuilt (the predecessor was superseded by the confirm), nftables left holding the
// new ruleset, and the revision stamped rolled_back over its own active. No active revision was left anywhere.
func TestRollbackRefusesAConfirmedRevisionEvenWithAPreviousBundle(t *testing.T) {
	for _, state := range []string{"active", "superseded", "failed", "rolled_back"} {
		t.Run(state, func(t *testing.T) {
			k := newFakeKernel(t)
			k.legacyJulyTable("br-g90|10.20.0.9")
			a := newTestApplier(t, k)
			a.revStateFn = func(context.Context, string) (string, error) { return state, nil }
			// The ordinary branch: a previous bundle exists, which is the case the old guard never covered.
			a.prevBundleFn = func(context.Context, string) (string, error) { return t.TempDir(), nil }
			marked := false
			a.markRolledFn = func(context.Context, string, string) error { marked = true; return nil }
			var refusals []string
			a.eventFn = func(_ context.Context, _, kind string, ok bool, detail map[string]any) {
				if kind == "rollback" && !ok {
					if why, has := detail["refused"]; has {
						refusals = append(refusals, fmt.Sprint(why))
					}
				}
			}
			a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true} }

			a.rollback(context.Background(), "rev-under-race", "confirmation window elapsed")

			if got := k.mutations(); len(got) != 0 {
				t.Fatalf("a revision in state %q was rolled back anyway: %d kernel mutation(s): %v", state, len(got), got)
			}
			if marked {
				t.Fatalf("a revision in state %q was stamped rolled_back; its recorded fate is not this caller's to overwrite", state)
			}
			if len(refusals) == 0 {
				t.Fatalf("the refusal was not recorded, so an operator could not tell why nothing happened")
			}
		})
	}
}

// ...and a revision that IS in flight still rolls back, so the guard did not simply disable the feature.
func TestRollbackStillProceedsForARevisionAwaitingConfirmation(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.9")
	a := newTestApplier(t, k)
	a.revStateFn = func(context.Context, string) (string, error) { return "pending_confirmation", nil }
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	marked := false
	a.markRolledFn = func(context.Context, string, string) error { marked = true; return nil }
	a.eventFn = func(context.Context, string, string, bool, map[string]any) {}

	a.rollback(context.Background(), "rev-in-flight", "health check failed")

	if !marked {
		t.Error("a revision awaiting confirmation must still be rolled back")
	}
}

// ---- the two swallowed kernel commands --------------------------------------------------------------------

// A BRIDGE THAT WILL NOT GO AWAY FAILS THE APPLY. destroyBridge used to discard both `ip link del` results and
// return nil unconditionally, which made applyBundle's destroy error path dead code. The consequence of a
// failed delete was a guest network removed from netplan, nftables and Kea whose bridge was still up and still
// carrying its gateway -- clients with an unfiltered path, and a green revision.
func TestABridgeThatCannotBeDeletedFailsTheApply(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.runFn = failingRun(k, "link del", errors.New("RTNETLINK answers: Device or resource busy"))
	err := a.destroyBridge(context.Background(), "br-g77")
	if err == nil {
		t.Fatal("destroyBridge reported success for a delete the kernel refused")
	}
	if !strings.Contains(err.Error(), "br-g77") {
		t.Errorf("the failure does not name the bridge: %v", err)
	}
}

// AND IT REFUSES THE APPLIANCE'S OWN INTERFACES BY NAME. Every caller passes a br-g* name today, so this
// cannot trigger -- it is here because the only thing between this function and management connectivity was a
// string prefix computed somewhere else.
func TestDestroyBridgeRefusesManagementAndWAN(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.legacyBridge = "br-lan"
	for _, name := range []string{a.topo.MgmtInterface, a.topo.WANInterface, "br-lan", ""} {
		if err := a.destroyBridge(context.Background(), name); err == nil {
			t.Errorf("destroyBridge accepted %q", name)
		}
	}
	if got := k.mutations(); len(got) != 0 {
		t.Fatalf("a refused destroy still issued %d kernel command(s): %v", len(got), got)
	}
}

// A STALE ADDRESS THAT WILL NOT COME OFF FAILS THE APPLY. The `ip addr del` error was discarded and the
// function still reported "changed, no error", so the bridge kept BOTH the old gateway and the new one. The
// gateway_up health check only asserts the wanted address is PRESENT, never that the old one is gone, so the
// appliance went on answering ARP for a gateway no DHCP scope and no nftables rule described.
func TestAStaleAddressThatCannotBeRemovedFailsTheApply(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.addrsFn = func(string) []string { return []string{"10.20.0.1/24"} } // the previous subnet, still on the bridge
	a.runFn = failingRun(k, "addr del", errors.New("RTNETLINK answers: Operation not permitted"))
	_, err := a.reconcileAddress(context.Background(), netcfg.GuestNetwork{
		BridgeName: "br-g90", GatewayIP: "10.30.0.1", PrefixLen: 24, Enabled: true,
	})
	if err == nil {
		t.Fatal("re-addressing reported success while the previous gateway was still live on the bridge")
	}
	if !strings.Contains(err.Error(), "10.20.0.1/24") {
		t.Errorf("the failure does not name the address that stayed: %v", err)
	}
}

// AN UNREADABLE /sys IS NOT AN EMPTY APPLIANCE. The directory read used to be discarded, so any failure became
// "there are no guest bridges" -- which silently disabled the destroy loop, the address reconciler and the
// rollback's rebuild decision, all at once.
func TestAnUnreadableInterfaceListFailsRatherThanReadingAsEmpty(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.liveBridgesFn = nil // fall through to the real /sys read, which does not exist in a test environment
	if _, err := a.liveGuestBridges(); err == nil {
		t.Skip("this environment has a readable /sys/class/net; the fail-closed path cannot be exercised here")
	}
	if err := a.reconcileAddresses(context.Background(), "rev-x", []netcfg.GuestNetwork{{
		BridgeName: "br-g90", GatewayIP: "10.20.0.1", PrefixLen: 24, Enabled: true,
	}}); err == nil {
		t.Fatal("the address reconciler reported success without being able to see a single interface")
	}
}

// ---- a bridge that no longer matches its intent -------------------------------------------------------------

// A CLIENT NETWORK'S VLAN OR PORT CANNOT CHANGE UNDERNEATH ITS BRIDGE. applyBundle only ever creates a bridge
// that is MISSING, so a changed VLAN id on an unchanged bridge name was written everywhere -- database, bundle,
// netplan, nftables, Kea -- and never to the wire. edged refuses such an edit, but netd is the component that
// answers for the wire, and it had no second line of defence.
func TestAnExistingBridgeAttachedToTheWrongVLANRefusesTheApply(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-gmismatch": true} }
	a.memberFn = func(string) string { return "ens192.30" } // the wire says VLAN 30

	err := a.applyBundle(context.Background(), "rev-mismatch", []netcfg.GuestNetwork{{
		BridgeName: "br-gmismatch", NetworkType: "vlan", ParentInterface: "ens192", VLANID: 40,
		GatewayIP: "10.40.0.1", PrefixLen: 24, Enabled: true,
	}}, t.TempDir())
	if err == nil {
		t.Fatal("an apply whose bridge is attached to the wrong VLAN was accepted")
	}
	if !strings.Contains(err.Error(), "ens192.30") || !strings.Contains(err.Error(), "ens192.40") {
		t.Errorf("the refusal does not say what the wire carries and what was asked: %v", err)
	}
	if got := k.mutations(); len(got) != 0 {
		t.Fatalf("a refused apply still changed %d kernel object(s): %v", len(got), got)
	}
}

// failingRun wraps the fake kernel so one family of commands reports a kernel refusal. The command is still
// recorded, so a test can assert both that it was attempted and that its failure was honoured.
func failingRun(k *fakeKernel, match string, boom error) func(context.Context, string, ...string) error {
	return func(ctx context.Context, name string, args ...string) error {
		line := name + " " + strings.Join(args, " ")
		if err := k.run(ctx, name, args...); err != nil {
			return err
		}
		if strings.Contains(line, match) {
			return boom
		}
		return nil
	}
}
