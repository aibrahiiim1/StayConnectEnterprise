package main

import (
	"context"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// B-26: AFTER `systemctl restart nftables` THE TABLE IS GONE, and the periodic re-assertion rebuilds it from the
// in-force intent -- without waiting for a reboot or a network change.
func TestPeriodicReassertionRebuildsAFlushedTable(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.setInForce(testIntent(), "")
	converge(t, a, "boot_reconcile")
	if !k.tableExists {
		t.Fatal("precondition: the table exists")
	}

	// nftables.service restart: `flush ruleset`
	k.tableExists, k.fp, k.sets = false, "", map[string][]string{}
	k.reset()
	a.reassertNftStructure(context.Background(), "periodic")
	if !k.tableExists || k.fp != netcfg.RenderFingerprint(testIntent(), testTopo()) {
		t.Fatalf("the flushed table was not rebuilt (exists=%v fp=%q)", k.tableExists, k.fp)
	}
	if _, ok := k.sets["phase3_auth_ipv4"]; !ok {
		t.Fatal("the rebuilt table has no Phase-3 set for netd to write into")
	}
}

// Steady state: the re-assertion issues NO mutation, so a healthy appliance's guests are never touched by it.
func TestPeriodicReassertionIsANoOpWhenTheRulesetMatches(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.setInForce(testIntent(), "")
	converge(t, a, "boot_reconcile")
	k.reset()
	a.reassertNftStructure(context.Background(), "periodic")
	if m := k.mutations(); len(m) != 0 {
		t.Fatalf("a matching ruleset was rewritten: %v", m)
	}
}

// Nothing in force (factory-clean), or an apply in progress: the re-assertion stands aside.
func TestPeriodicReassertionStandsAside(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.reassertNftStructure(context.Background(), "periodic")
	if len(k.cmds) != 0 {
		t.Fatalf("a factory-clean appliance was touched: %v", k.cmds)
	}
	a.setInForce(testIntent(), "")
	a.busy.Add(1)
	a.reassertNftStructure(context.Background(), "periodic")
	a.busy.Add(-1)
	if len(k.cmds) != 0 {
		t.Fatalf("the re-assertion ran during an apply: %v", k.cmds)
	}
}

// The management pin is part of the render, so a WAN address change converges the ruleset to the new pin.
func TestReassertionFollowsTheManagementAddress(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.topo.MgmtAddr = mgmtAddrAuto
	addr := "192.0.2.23"
	a.mgmtAddrFn = func(string) string { return addr }
	a.setInForce(testIntent(), "")
	a.reassertNftStructure(context.Background(), "periodic")
	first := k.fp
	addr = "192.0.2.77"
	k.reset()
	a.reassertNftStructure(context.Background(), "system_network")
	if len(k.mutations()) != 1 || k.fp == first {
		t.Fatalf("the ruleset did not follow the new management address: %v", k.cmds)
	}
	topo := testTopo()
	topo.MgmtAddr = "192.0.2.77"
	if k.fp != netcfg.RenderFingerprint(testIntent(), topo) {
		t.Fatal("the live ruleset does not pin the new address")
	}
}
