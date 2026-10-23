package main

// Each case is a path where the live network and the recorded revision could diverge while netd reported
// success: a step mutated the wire, discarded its error and let the revision commit.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// A rollback must not run against a revision just confirmed, on either branch: the watchdog could otherwise
// destroy bridges, rebuild nothing and stamp rolled_back over the active revision.
func TestRollbackRefusesAConfirmedRevisionEvenWithAPreviousBundle(t *testing.T) {
	for _, state := range []string{"active", "superseded", "failed", "rolled_back"} {
		t.Run(state, func(t *testing.T) {
			k := newFakeKernel(t)
			k.legacyJulyTable("br-g90|10.20.0.9")
			a := newTestApplier(t, k)
			a.revStateFn = func(context.Context, string) (string, error) { return state, nil }
			// The ordinary branch: a previous bundle exists.
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

// A revision in flight still rolls back.
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

// A bridge that will not go away fails the apply (else its network is removed from the config while the bridge still carries traffic).
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

// destroyBridge refuses the appliance's own interfaces by name, as a guard on management connectivity.
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

// A stale address that will not come off fails the apply (gateway_up only checks the wanted address is present).
func TestAStaleAddressThatCannotBeRemovedFailsTheApply(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.addrsFn = func(string) []string { return []string{"10.20.0.1/24"} }
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

// An unreadable /sys is not an empty appliance: it must fail, not disable the destroy loop and address reconciler.
func TestAnUnreadableInterfaceListFailsRatherThanReadingAsEmpty(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.liveBridgesFn = nil // use the real /sys read
	if _, err := a.liveGuestBridges(); err == nil {
		t.Skip("this environment has a readable /sys/class/net; the fail-closed path cannot be exercised here")
	}
	if err := a.reconcileAddresses(context.Background(), "rev-x", []netcfg.GuestNetwork{{
		BridgeName: "br-g90", GatewayIP: "10.20.0.1", PrefixLen: 24, Enabled: true,
	}}); err == nil {
		t.Fatal("the address reconciler reported success without being able to see a single interface")
	}
}

// A client network's VLAN or port cannot change under its bridge: applyBundle only creates missing bridges, so
// netd is the second line of defence behind edged's refusal.
func TestAnExistingBridgeAttachedToTheWrongVLANRefusesTheApply(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-gmismatch": true} }
	a.memberFn = func(string) string { return "ens192.30" }

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

// failingRun makes one family of commands report a kernel refusal; the command is still recorded.
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
