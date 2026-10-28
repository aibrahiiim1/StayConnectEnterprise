package main

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// After `systemctl restart nftables` the table is gone; the periodic re-assertion rebuilds it from the in-force intent.
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

// Audit Phase 8 L13: a re-assertion that keeps failing is reported in health and backed off; success clears it.
func TestPeriodicReassertionFailuresAreReportedAndBackedOff(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.setInForce(testIntent(), "")
	converge(t, a, "boot_reconcile")
	k.tableExists, k.fp, k.sets = false, "", map[string][]string{}
	k.failNftF = true

	for i := 0; i < nftReassertReportAfter; i++ {
		a.reassertNftStructure(context.Background(), "periodic")
		a.nftWatch.mu.Lock()
		a.nftWatch.nextTry = time.Time{} // the back-off is proven below; here each tick is made due
		a.nftWatch.mu.Unlock()
	}
	st := a.nftWatch.status()
	if st["failing"] != true || st["consecutive_failures"] != nftReassertReportAfter || st["last_error"] == "" {
		t.Fatalf("a re-assertion failing every tick is not reported: %+v", st)
	}

	a.reassertNftStructure(context.Background(), "periodic")
	k.reset()
	a.reassertNftStructure(context.Background(), "periodic") // inside the back-off: not attempted
	if len(k.cmds) != 0 {
		t.Fatalf("the re-assertion was retried inside its back-off: %v", k.cmds)
	}

	k.failNftF = false
	a.nftWatch.mu.Lock()
	a.nftWatch.nextTry = time.Time{}
	a.nftWatch.mu.Unlock()
	a.reassertNftStructure(context.Background(), "periodic")
	if !k.tableExists || a.nftWatch.status()["failing"] != false {
		t.Fatalf("a successful re-assertion did not clear the report: %+v", a.nftWatch.status())
	}
}

// Steady state: no mutation.
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
