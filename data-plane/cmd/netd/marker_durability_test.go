package main

// The incomplete-rollback block must survive a crash and must never be lost or lifted silently. Each case injects the failure on the file or the
// database side and then looks at what a restarted process would do.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// breakMarkerPath makes the marker file impossible to write or remove: a non-empty directory stands where the file belongs.
func breakMarkerPath(t *testing.T, a *applier) {
	t.Helper()
	p := a.incompleteMarkerPath()
	if err := os.MkdirAll(filepath.Join(p, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func restarted(t *testing.T, a *applier, st *fakeStore) *applier {
	t.Helper()
	b := applyFixture(t, newFakeKernel(t), st)
	b.generatedDir = a.generatedDir
	b.loadIncompleteMarker(context.Background())
	return b
}

func TestAMarkerThatCannotBeWrittenStillBlocksAndSurvivesACrashThroughTheRevisionRecord(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	breakMarkerPath(t, a)

	steps := a.rollback(context.Background(), "rev-failed", "x")
	if !strings.Contains(strings.Join(steps, " | "), "marker could not be saved") {
		t.Fatalf("the unsaved marker is not part of the rollback's report: %v", steps)
	}
	if _, err := a.Apply(context.Background(), "next", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("this process allowed an apply after an incomplete rollback: %v", err)
	}
	if !strings.Contains(st.reasons["rev-failed"], "ROLLBACK INCOMPLETE") {
		t.Fatalf("the revision history does not record it: %q", st.reasons["rev-failed"])
	}

	// The process crashes. There is no marker file, only the revision's recorded reason.
	if err := os.RemoveAll(a.incompleteMarkerPath()); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a, st)
	if _, err := b.Apply(context.Background(), "after the crash", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("a crash made the appliance forget an unfinished rollback: %v", err)
	}
}

func TestAMarkerThatCannotBeRemovedKeepsTheBlock(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	a.rollback(context.Background(), "rev-failed", "x")
	fk.setErr = nil
	if err := os.Remove(a.incompleteMarkerPath()); err != nil {
		t.Fatal(err)
	}
	breakMarkerPath(t, a)
	st.activeIntent = oldIntent()

	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("a boot that could not lift the block reported that it converged")
	}
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("the block was dropped from memory although the file could not be removed")
	}
	if _, err := a.Apply(context.Background(), "next", "tester"); !errors.Is(err, errRollbackIncomplete) {
		t.Fatalf("an apply was allowed: %v", err)
	}
}

func TestABlockThatCannotBeResolvedInTheHistoryIsNotLifted(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	a.rollback(context.Background(), "rev-failed", "x")
	fk.setErr = nil
	st.resolveErr = errors.New("database unavailable")
	st.activeIntent = oldIntent()

	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("converged although the record could not be resolved")
	}
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("the block was lifted although the history still says the rollback is incomplete")
	}
	if _, err := os.Stat(a.incompleteMarkerPath()); err != nil {
		t.Fatalf("the marker file was removed first: %v", err)
	}
}

func TestALiftedBlockStaysLiftedAfterARestart(t *testing.T) {
	a, _, st, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	a.rollback(context.Background(), "rev-failed", "x")
	fk.setErr = nil
	st.activeIntent = oldIntent()
	if !a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("setup: the boot reconcile did not converge")
	}
	b := restarted(t, a, st)
	if _, bad := b.rollbackIncomplete(); bad {
		t.Fatal("a resolved incomplete rollback blocks every restart for ever")
	}
}

func TestAnUnreadableMarkerFileCountsAsPresent(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	if err := os.WriteFile(a.incompleteMarkerPath(), []byte("{not json"), 0o640); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a, st)
	if _, bad := b.rollbackIncomplete(); !bad {
		t.Fatal("a corrupt marker was read as no marker")
	}
}

func TestACleanRetryThatCannotLiftTheBlockLeavesItInPlace(t *testing.T) {
	a, _, _, fk := rollbackFixture(t)
	fk.setErr = errors.New("kea refused")
	if steps := a.rollback(context.Background(), "rev-failed", "x"); len(steps) == 0 {
		t.Fatal("setup: should be incomplete")
	}
	fk.setErr = nil
	if err := os.Remove(a.incompleteMarkerPath()); err != nil {
		t.Fatal(err)
	}
	breakMarkerPath(t, a)
	a.rollback(context.Background(), "rev-failed", "retry")
	if _, bad := a.rollbackIncomplete(); !bad {
		t.Fatal("the block was dropped although it could not be removed durably")
	}
}
