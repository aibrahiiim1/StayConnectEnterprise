package main

// The appliance must never be treated as being on its confirmed configuration when it is not: across a missing marker, a database that fails, a restart
// and the database coming back, and when a confirmation or a recovery fails half way.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// A boot that does not converge refuses further changes, even though no rollback ever failed, and the refusal survives a restart.
func TestAnUnconvergedBootBlocksChangesAndTheBlockSurvivesARestart(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	st.activeIntent = oldIntent()
	fk.setErr = errors.New("kea refused")

	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("a boot with a failed DHCP push claimed to converge")
	}
	if _, err := a.Apply(context.Background(), "next", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("a change was applied on top of an unconverged appliance: %v", err)
	}
	b := restarted(t, a, st)
	if _, err := b.Apply(context.Background(), "after restart", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("a restart forgot that the boot had not converged: %v", err)
	}
}

func TestAConfirmedNetworkThatIsNotServingIsNotConverged(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	st.activeIntent = oldIntent()
	a.healthFn = func(context.Context, string, []netcfg.GuestNetwork) []healthResult {
		return []healthResult{{Name: "kea_running", OK: false, Detail: "Kea holds no DHCP socket for 10.20.0.1"}}
	}
	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("a Kea that holds no socket counted as a converged network")
	}
	// portald is a separate service that may start after netd; its absence alone is not a failure of the confirmed network.
	a2, _, st2, _ := rollbackFixture(t)
	st2.activeIntent = oldIntent()
	a2.healthFn = func(context.Context, string, []netcfg.GuestNetwork) []healthResult {
		return []healthResult{{Name: "portal_listen", OK: false}, {Name: "kea_running", OK: true}, {Name: "gateway_up", OK: true}}
	}
	if !a2.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("portald not yet listening blocked the appliance")
	}
}

func TestAnUnreadableConfirmedRevisionBlocksChanges(t *testing.T) {
	a, _, _, _ := rollbackFixture(t)
	a.activeIntentFn = func(context.Context) (string, string, []netcfg.GuestNetwork, error) {
		return "rev-prev", "", nil, errors.New("intent unreadable")
	}
	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("converged without being able to read the configuration to converge to")
	}
	if _, err := a.Apply(context.Background(), "next", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("%v", err)
	}
}

// While changes are refused the reconcile retries by itself, so a fault that was transient (Kea slow to start, a database that was down) needs no restart.
func TestTheBlockClearsItselfWhenTheFaultGoesAway(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	st.activeIntent = oldIntent()
	fk.setErr = errors.New("kea refused")
	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("setup: should not converge")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.blockedRecoveryLoop(ctx, 10*time.Millisecond, 40*time.Millisecond)

	time.Sleep(80 * time.Millisecond)
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("the block cleared while the fault was still there")
	}
	fk.setErr = nil
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, bad := a.rollbackIncomplete(); !bad {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the block never cleared after the fault went away")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(a.incompleteMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("the marker file is still there: %v", err)
	}
}

// The database failing during a rollback and coming back later ends with the appliance reconciled and unblocked, never in between.
func TestADatabaseThatFailsAndRecoversEndsReconciledNotHalfWay(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	st.stateReadErr = errors.New("connection refused")
	if steps := a.rollback(context.Background(), "rev-failed", "x"); len(steps) == 0 {
		t.Fatal("a rollback that could not read the revision claimed to have finished")
	}
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("no block")
	}
	if st.state["rev-failed"] != "pending_confirmation" {
		t.Fatalf("the revision was moved to %q although the database could not be read", st.state["rev-failed"])
	}

	st.stateReadErr = nil // the database is back
	st.activeIntent = oldIntent()
	if !a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("did not converge after the database recovered")
	}
	if _, bad := a.rollbackIncomplete(); bad {
		t.Fatal("still blocked after converging")
	}
	// The watchdog then finishes the revision that was left pending.
	a.rollbackExclusive(context.Background(), "rev-failed", "confirmation window elapsed")
	if st.state["rev-failed"] != "rolled_back" {
		t.Fatalf("revision %q", st.state["rev-failed"])
	}
}

func factoryCleanFixture(t *testing.T) (*applier, *fakeKernel) {
	t.Helper()
	k := newFakeKernel(t)
	a := applyFixture(t, k, newFakeStore(newIntent()))
	a.healthFn = func(context.Context, string, []netcfg.GuestNetwork) []healthResult { return nil }
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g91": true} }
	a.ownerMarkFn = func(n string) string {
		if n == "br-g91" {
			return "net-91"
		}
		return ""
	}
	a.recordedFn = func(context.Context) (map[string]recordedBridge, error) {
		return map[string]recordedBridge{"br-g91": rec("ens192.91", "net-91")}, nil
	}
	return a, k
}

// With nothing ever confirmed the correct state is factory-clean. A first apply whose rollback failed leaves a block that only a verified factory-clean state lifts.
func TestAFirstApplyWhoseRollbackFailedIsCleanedAndUnblockedAtBoot(t *testing.T) {
	a, k := factoryCleanFixture(t)
	if err := a.markRollbackIncomplete("rev-first", []string{"bridges not destroyed"}); err != nil {
		t.Fatal(err)
	}
	if !a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("the factory-clean state was not reached")
	}
	if got := deleted(k); len(got) == 0 || got[0] != "br-g91" {
		t.Fatalf("the leftover bridge was not removed: %v", k.cmds)
	}
	if _, bad := a.rollbackIncomplete(); bad {
		t.Fatal("the block stayed although the appliance is factory-clean")
	}
}

func TestAFactoryCleanBootThatCannotRemoveALeftoverStaysBlocked(t *testing.T) {
	a, _ := factoryCleanFixture(t)
	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "ip" && len(args) > 1 && args[0] == "link" && args[1] == "del" {
			return errors.New("device busy")
		}
		return inner(ctx, name, args...)
	}
	if err := a.markRollbackIncomplete("rev-first", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("converged although a leftover bridge could not be removed")
	}
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("the block was lifted on an appliance that is not clean")
	}
}

// A confirmation that fails must leave the revision pending (so it still rolls back) and must not be reported as done.
func TestAFailedConfirmationLeavesTheRevisionPendingAndRollbackable(t *testing.T) {
	a, _, st := pendingFixture(t)
	st.markActiveErr = errors.New("database unavailable")
	if err := a.Confirm(context.Background(), "rev-failed", "operator"); err == nil {
		t.Fatal("a confirmation the database refused was reported as done")
	}
	if st.state["rev-failed"] != "pending_confirmation" {
		t.Fatalf("state %q", st.state["rev-failed"])
	}
	if steps := a.rollbackExclusive(context.Background(), "rev-failed", "confirmation window elapsed"); len(steps) != 0 {
		t.Fatalf("%v", steps)
	}
	if st.state["rev-failed"] != "rolled_back" {
		t.Fatalf("the revision never rolled back: %q", st.state["rev-failed"])
	}
}

func TestNothingIsConfirmedWhileTheApplianceIsNotKnownToBeOnItsConfiguration(t *testing.T) {
	a, _, st := pendingFixture(t)
	if err := a.markRollbackIncomplete("rev-x", []string{"kea not restored"}); err != nil {
		t.Fatal(err)
	}
	err := a.Confirm(context.Background(), "rev-failed", "operator")
	if !errors.Is(err, errRollbackIncomplete) || st.state["rev-failed"] == "active" {
		t.Fatalf("confirmed while blocked: %v state=%q", err, st.state["rev-failed"])
	}
	if !strings.Contains(err.Error(), "not known to be on its confirmed configuration") {
		t.Fatalf("%v", err)
	}
}
