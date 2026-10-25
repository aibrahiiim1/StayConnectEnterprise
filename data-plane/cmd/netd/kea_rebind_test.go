package main

// A restart makes Kea load the FILE, which now holds the confirmed configuration and not the one being applied (config-write is gone). The rebind
// restart must therefore push the new configuration again, on the restarted Kea, before it checks that the gateway is bound.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func udpListeningOn(gw string) string {
	return "  sl  local_address rem_address   st\n   1: " + hexLE(gw) + ":0043 00000000:0000 07\n"
}

func rebindFixture(t *testing.T) (*applier, *fakeKernel, *fakeKea, *int) {
	t.Helper()
	k := newFakeKernel(t)
	st := newFakeStore(oldIntent())
	a := applyFixture(t, k, st)
	srv := &fakeKeaServer{}
	a.keaSocket = serveFakeKea(t, srv)
	fk := &fakeKea{}
	a.keaRTSeam = fk
	live := map[string]bool{"br-g90": true}
	a.liveBridgesFn = func() map[string]bool { return live }
	a.ifaceExistsFn = func(n string) bool { return live[n] || n == "ens192.90" }
	a.memberFn = func(string) string { return "ens192.90" }
	a.addrsFn = func(string) []string { return []string{"10.20.0.1/22"} }
	a.ownerMarkFn = func(string) string { return "marked" }
	restarts := 0
	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "systemctl" && len(args) == 2 && args[0] == "restart" && args[1] == "kea-dhcp4-server" {
			restarts++
		}
		return inner(ctx, name, args...)
	}
	return a, k, fk, &restarts
}

func TestRebindRestartPushesTheNewConfigurationAgain(t *testing.T) {
	a, _, fk, restarts := rebindFixture(t)
	calls := 0
	a.procNetUDPFn = func() string {
		calls++
		if *restarts == 0 {
			return "" // before the restart Kea holds no socket for the gateway
		}
		return udpListeningOn("10.20.0.1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := a.applyBundle(ctx, "rev-1", oldIntent(), t.TempDir()); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if *restarts != 1 {
		t.Fatalf("Kea was restarted %d times, want 1", *restarts)
	}
	if fk.set != 2 {
		t.Fatalf("the configuration was pushed %d times; a restart loads the confirmed file, so it must be pushed again (want 2)", fk.set)
	}
	if !strings.Contains(string(mustJSON(t, fk.lastSet)), "10.20.0.0/22") {
		t.Fatal("the last configuration pushed is not the one being applied")
	}
}

func TestRebindRestartThatStillDoesNotBindFailsTheApply(t *testing.T) {
	a, _, _, restarts := rebindFixture(t)
	a.procNetUDPFn = func() string { return "" }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := a.applyBundle(ctx, "rev-1", oldIntent(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "still not listening") {
		t.Fatalf("an apply whose gateway never bound succeeded: %v", err)
	}
	if *restarts != 1 {
		t.Fatalf("restarts = %d", *restarts)
	}
}

func TestNoRestartWhenKeaAlreadyListens(t *testing.T) {
	a, _, fk, restarts := rebindFixture(t)
	a.procNetUDPFn = func() string { return udpListeningOn("10.20.0.1") }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := a.applyBundle(ctx, "rev-1", oldIntent(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 || fk.set != 1 {
		t.Fatalf("restarts=%d pushes=%d, want 0 and 1", *restarts, fk.set)
	}
	_ = netcfg.DefaultTopology
}
