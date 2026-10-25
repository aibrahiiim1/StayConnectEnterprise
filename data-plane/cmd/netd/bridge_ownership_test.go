package main

// netd may delete only a bridge it can PROVE it created. Before this, any bridge named br-g* that the configuration in force did not want was
// destroyed, whoever made it. These tests drive removal through the real code paths and assert on the commands that reached the kernel.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// ownershipFixture is a non-dry-run applier with an in-memory ownership mark (the alias netd sets) updated by the commands it issues.
func ownershipFixture(t *testing.T, live map[string]bool) (*applier, *fakeKernel, map[string]string) {
	t.Helper()
	k := newFakeKernel(t)
	st := newFakeStore(oldIntent())
	a := applyFixture(t, k, st)
	marks := map[string]string{}
	a.ownerMarkFn = func(name string) string { return marks[name] }
	a.liveBridgesFn = func() map[string]bool { return live }
	a.ifaceExistsFn = func(name string) bool { return live[name] }
	members := map[string]string{}
	a.memberFn = func(br string) string { return members[br] }
	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "ip" && len(args) >= 6 && args[0] == "link" && args[1] == "set" && args[2] == "dev" && args[4] == "alias" {
			marks[args[3]] = strings.TrimPrefix(args[5], ownerMarkPrefix)
		}
		return inner(ctx, name, args...)
	}
	t.Cleanup(func() { _ = members })
	return a, k, marks
}

func deleted(k *fakeKernel) []string {
	var out []string
	for _, c := range k.cmds {
		if strings.HasPrefix(c, "ip link del ") {
			out = append(out, strings.TrimPrefix(c, "ip link del "))
		}
	}
	return out
}

func TestOnlyBridgesNetdCanProveItCreatedAreRemoved(t *testing.T) {
	live := map[string]bool{"br-g20": true, "br-g30": true, "br-g40": true, "br-g90": true}
	a, k, marks := ownershipFixture(t, live)
	marks["br-g20"] = "net-20" // marked by netd
	// br-g30 is unmarked but a revision netd applied names it (created before the mark existed)
	a.recordedFn = func(context.Context) (map[string]recordedBridge, error) {
		return map[string]recordedBridge{"br-g30": {}, "br-g90": {}}, nil
	}
	// br-g40: neither marked nor recorded: someone else's. br-g90: wanted.

	removed, failed, unmanaged, err := a.removeStrayBridges(context.Background(), func(b string) bool { return b == "br-g90" })
	if err != nil || len(failed) != 0 {
		t.Fatalf("err=%v failed=%v", err, failed)
	}
	if strings.Join(removed, ",") != "br-g20,br-g30" {
		t.Fatalf("removed %v, want the marked and the recorded bridge", removed)
	}
	if strings.Join(unmanaged, ",") != "br-g40" {
		t.Fatalf("unmanaged %v, want br-g40 reported and left alone", unmanaged)
	}
	for _, d := range deleted(k) {
		if strings.Contains(d, "br-g40") || strings.Contains(d, "br-g90") {
			t.Fatalf("netd deleted %s, which it did not create or still needs: %v", d, k.cmds)
		}
	}
}

// A bridge nobody can vouch for is left alone even when the record cannot be read at all.
func TestNothingUnprovableIsRemovedWhenTheRecordIsUnreadable(t *testing.T) {
	a, k, marks := ownershipFixture(t, map[string]bool{"br-g20": true, "br-g30": true})
	marks["br-g20"] = "net-20"
	a.recordedFn = func(context.Context) (map[string]recordedBridge, error) { return nil, os.ErrInvalid }
	removed, failed, unmanaged, err := a.removeStrayBridges(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "br-g20" || strings.Join(unmanaged, ",") != "br-g30" {
		t.Fatalf("removed=%v unmanaged=%v", removed, unmanaged)
	}
	if len(failed) == 0 {
		t.Fatal("the unreadable record was not reported")
	}
	if got := deleted(k); len(got) != 1 {
		t.Fatalf("deleted %v", got)
	}
}

func TestOnlyAVlanInterfaceNetdCreatedIsDeletedWithItsBridge(t *testing.T) {
	cases := []struct {
		name     string
		memberOf string
		marked   bool
		recorded recordedBridge
		wantDel  bool
	}{
		{"marked member", "ens192.20", true, recordedBridge{Member: "ens192.20"}, true},
		{"unmarked member the record names", "ens192.20", false, recordedBridge{Member: "ens192.20"}, true},
		{"unmarked member the record does not name", "ens192.20", false, recordedBridge{Member: "ens192.99"}, false},
		{"member with an ambiguous record", "ens192.20", false, recordedBridge{Member: ""}, false},
		{"a physical port is never deleted", "ens192", true, recordedBridge{Member: "ens192"}, false},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			a, k, marks := ownershipFixture(t, map[string]bool{"br-g20": true})
			marks["br-g20"] = "net-20"
			if tc.marked {
				marks[tc.memberOf] = "net-20"
			}
			a.memberFn = func(string) string { return tc.memberOf }
			a.recordedFn = func(context.Context) (map[string]recordedBridge, error) {
				return map[string]recordedBridge{"br-g20": tc.recorded}, nil
			}
			if _, failed, _, err := a.removeStrayBridges(context.Background(), nil); err != nil || len(failed) != 0 {
				t.Fatalf("err=%v failed=%v", err, failed)
			}
			got := false
			for _, d := range deleted(k) {
				if d == tc.memberOf {
					got = true
				}
			}
			if got != tc.wantDel {
				t.Fatalf("member %s deleted=%v, want %v (commands %v)", tc.memberOf, got, tc.wantDel, k.cmds)
			}
		})
	}
}

// A wanted bridge that exists without a mark is verified against the configuration, marked, and otherwise left exactly as it is.
func TestAHealthyUnmarkedNetworkIsAdoptedWithoutBeingTouched(t *testing.T) {
	a, k, marks := ownershipFixture(t, map[string]bool{"br-g90": true})
	n := oldIntent()[0]
	n.ID = "net-90"
	a.memberFn = func(string) string { return "ens192.90" }
	desired := map[string]netcfg.GuestNetwork{"br-g90": n}

	probs := a.adoptWantedBridges(context.Background(), desired, map[string]bool{"br-g90": true})

	if len(probs) != 0 {
		t.Fatalf("adoption reported problems: %v", probs)
	}
	if marks["br-g90"] != "net-90" {
		t.Fatalf("the bridge was not marked: %v", marks)
	}
	for _, c := range k.cmds {
		if strings.Contains(c, "link del") || strings.Contains(c, "addr") || strings.Contains(c, "master") || strings.Contains(c, " down") {
			t.Fatalf("adopting a healthy network touched it: %s", c)
		}
	}
}

// A bridge whose member is not what the configuration says is NOT adopted: it is not the network the configuration describes.
func TestABridgeThatIsNotTheConfiguredNetworkIsNotAdopted(t *testing.T) {
	a, k, marks := ownershipFixture(t, map[string]bool{"br-g90": true})
	n := oldIntent()[0]
	n.ID = "net-90"
	a.memberFn = func(string) string { return "eth7.55" }
	a.adoptWantedBridges(context.Background(), map[string]netcfg.GuestNetwork{"br-g90": n}, map[string]bool{"br-g90": true})
	if _, marked := marks["br-g90"]; marked {
		t.Fatal("a bridge on someone else's interface was claimed")
	}
	if len(k.cmds) != 0 {
		t.Fatalf("commands issued: %v", k.cmds)
	}
}

func TestACreatedNetworkIsMarked(t *testing.T) {
	a, k, marks := ownershipFixture(t, map[string]bool{})
	n := oldIntent()[0]
	n.ID = "net-90"
	if err := a.createNetwork(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if marks["br-g90"] != "net-90" || marks["ens192.90"] != "net-90" {
		t.Fatalf("created resources are not marked: %v (commands %v)", marks, k.cmds)
	}
}

// ---- through the rollback and boot paths -------------------------------------------------------------------------------------------

func TestRollbackLeavesABridgeItCannotProveIsItsOwn(t *testing.T) {
	a, k, st, _ := rollbackFixture(t)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true, "br-gstranger": true} }
	if steps := a.rollback(context.Background(), "rev-failed", "x"); len(steps) != 0 {
		t.Fatalf("setup: %v", steps)
	}
	for _, d := range deleted(k) {
		if strings.Contains(d, "stranger") {
			t.Fatalf("rollback deleted a bridge it did not create: %s", d)
		}
	}
	if !strings.Contains(st.eventsJoined(), "bridges_left_alone") {
		t.Fatalf("the bridge left alone was not reported: %s", st.eventsJoined())
	}
}

func TestRollbackRemovesAMarkedBridgeTheFailedApplyCreated(t *testing.T) {
	a, k, _, _ := rollbackFixture(t)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true, "br-g91": true} }
	a.ownerMarkFn = func(name string) string {
		if name == "br-g91" {
			return "net-91"
		}
		return ""
	}
	a.rollback(context.Background(), "rev-failed", "x")
	found := false
	for _, d := range deleted(k) {
		if d == "br-g91" {
			found = true
		}
		if d == "br-g90" {
			t.Fatal("the previous revision's bridge was deleted")
		}
	}
	if !found {
		t.Fatalf("the failed apply's own bridge was not removed: %v", k.cmds)
	}
}

func TestFactoryCleanRollbackKeepsAnUnprovenBridge(t *testing.T) {
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	a := applyFixture(t, k, st)
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true, "br-gstranger": true} }
	a.ownerMarkFn = func(n string) string {
		if n == "br-g90" {
			return "net-90"
		}
		return ""
	}
	st.state["rev-first"] = "pending_confirmation"
	a.rollback(context.Background(), "rev-first", "expired")
	got := deleted(k)
	if len(got) != 1 || got[0] != "br-g90" {
		t.Fatalf("deleted %v, want only the bridge netd created", got)
	}
}

// bootFixture is a confirmed revision on disk, with an unmarked stray and a marked orphan alongside the wanted bridge.
func bootFixture(t *testing.T) (*applier, *fakeStore, *fakeKernel) {
	t.Helper()
	a, k, st, _ := rollbackFixture(t)
	st.activeID = "rev-prev"
	st.state["rev-failed"] = "rolled_back"
	return a, st, k
}

func TestBootReconcileRemovesOnlyWhatNetdProvablyCreated(t *testing.T) {
	a, _, k := bootFixture(t)
	a.liveBridgesFn = func() map[string]bool {
		return map[string]bool{"br-g90": true, "br-gorphan": true, "br-gstranger": true}
	}
	a.ifaceExistsFn = func(name string) bool { return name == "br-g90" || name == "ens192.90" }
	a.ownerMarkFn = func(n string) string {
		if n == "br-gorphan" {
			return "net-gone"
		}
		return ""
	}
	a.ReconcileActiveOnBoot(context.Background())
	got := deleted(k)
	if len(got) != 1 || got[0] != "br-gorphan" {
		t.Fatalf("boot deleted %v, want only the marked orphan", got)
	}
}

// A healthy confirmed network is not rebuilt, re-addressed or restarted by a boot reconcile that only has to adopt it.
func TestBootReconcileLeavesAHealthyConfirmedNetworkAlone(t *testing.T) {
	a, _, k := bootFixture(t)
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g90": true} }
	a.ifaceExistsFn = func(name string) bool { return name == "br-g90" || name == "ens192.90" }
	a.addrsFn = func(string) []string { return []string{"10.20.0.1/22"} }
	a.ReconcileActiveOnBoot(context.Background())
	for _, c := range k.cmds {
		if strings.Contains(c, "link del") || strings.Contains(c, "link add") || strings.Contains(c, "addr del") || strings.Contains(c, "addr add") {
			t.Fatalf("a healthy network was touched: %s", c)
		}
	}
}

// ---- D-3: a disabled network on a removed interface does not block every change --------------------------------------------------

func TestApplyValidatesWithADisabledNetworkOnARemovedInterface(t *testing.T) {
	k := newFakeKernel(t)
	intent := append(oldIntent(), netcfg.GuestNetwork{
		ID: "net-old", Name: "history", Enabled: false, NetworkType: "vlan", ParentInterface: "ens999", VLANID: 7,
		BridgeName: "br-g7", GatewayIP: "10.77.0.1", SubnetCIDR: "10.77.0.0/24", PrefixLen: 24, DHCPMode: netcfg.DHCPLocal,
		Pools: []netcfg.Pool{{StartIP: "10.77.0.100", EndIP: "10.77.0.200"}},
	})
	a := applyFixture(t, k, newFakeStore(intent))
	res, err := a.Validate(context.Background(), "x", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "validated" || !res.Validation.OK {
		t.Fatalf("a disabled network on a removed NIC blocked validation: %+v", res.Validation.Issues)
	}
	_ = filepath.Join
}
