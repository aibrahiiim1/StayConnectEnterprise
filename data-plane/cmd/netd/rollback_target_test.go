package main

// AN OPERATOR ROLLBACK MUST HAVE A TARGET, OR REFUSE.
//
// THE DEFECT THESE PIN. applier.Rollback used to call a.rollback straight through. a.rollback finds its
// target with store.ActiveBundlePath(ctx, exceptID) -- `WHERE state='active' AND id<>$1` -- which answers
// correctly while a revision is PENDING CONFIRMATION, because the previous revision is then still active.
// Once a revision is CONFIRMED there is exactly one active row and it is the revision itself, so the query
// returns nothing, prevBundle is empty, and a.rollback takes its factory-clean branch: every guest bridge
// destroyed, netplan and unbound fragment deleted, Kea restored to stopped and disabled.
//
// So pressing Rollback on the configuration currently in force took the whole guest network down, and the
// endpoint answered {"state":"rolled_back"}. These tests assert the refusal, and -- the part that matters
// most -- that the refusal issues NO kernel command at all. A test that only checked the error could pass
// while the bridges were already gone.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func rollbackApplier(t *testing.T, k *fakeKernel, state string, stateErr error) *applier {
	t.Helper()
	a := newTestApplier(t, k)
	a.revStateFn = func(context.Context, string) (string, error) { return state, stateErr }
	// If the guard ever falls through to the real rollback, these make the damage observable rather than
	// hypothetical: a target lookup that answers "none" is what selects the tear-down branch.
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
	a.eventFn = func(context.Context, string, string, bool, map[string]any) {}
	return a
}

// THE CENTRAL CASE.
func TestRollbackOfAConfirmedRevisionIsRefusedAndTouchesNothing(t *testing.T) {
	k := newFakeKernel(t)
	a := rollbackApplier(t, k, "active", nil)

	err := a.Rollback(context.Background(), "rev-confirmed", "operator@example.test")
	if err == nil {
		t.Fatal("rolling back the confirmed active revision was ACCEPTED; it would have destroyed every " +
			"guest bridge and stopped DHCP")
	}
	if !errors.Is(err, errRollbackRefused) {
		t.Errorf("error does not wrap errRollbackRefused, so the transport will report 500: %v", err)
	}
	// The operator has to be able to act on the message, so it must say what to do instead.
	for _, want := range []string{"CONFIRMED", "apply that as a new revision"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if got := k.mutations(); len(got) != 0 {
		t.Errorf("a refused rollback issued %d kernel mutation(s): %v", len(got), got)
	}
}

// The window where a rollback IS meaningful: the previous revision is still the active one.
func TestRollbackIsAllowedWhileTheRevisionIsStillMidFlight(t *testing.T) {
	for _, state := range []string{"applying", "pending_confirmation"} {
		k := newFakeKernel(t)
		a := rollbackApplier(t, k, state, nil)
		if err := a.Rollback(context.Background(), "rev-inflight", "operator@example.test"); err != nil {
			t.Errorf("state %q: rollback was refused, but this is exactly the window it exists for: %v",
				state, err)
		}
	}
}

// Everything that is not in force has nothing to undo, and must not reach the tear-down branch either.
func TestRollbackOfARevisionThatIsNotInForceIsRefused(t *testing.T) {
	for _, state := range []string{"draft", "validated", "failed", "rolled_back", "superseded"} {
		k := newFakeKernel(t)
		a := rollbackApplier(t, k, state, nil)
		err := a.Rollback(context.Background(), "rev-"+state, "operator@example.test")
		if err == nil {
			t.Errorf("state %q: rollback was accepted; there is nothing in force to roll back", state)
			continue
		}
		if !errors.Is(err, errRollbackRefused) {
			t.Errorf("state %q: error does not wrap errRollbackRefused: %v", state, err)
		}
		if got := k.mutations(); len(got) != 0 {
			t.Errorf("state %q: a refused rollback issued %d kernel mutation(s): %v", state, len(got), got)
		}
	}
}

func TestRollbackOfAnUnknownRevisionIsRefused(t *testing.T) {
	k := newFakeKernel(t)
	a := rollbackApplier(t, k, "", nil)
	err := a.Rollback(context.Background(), "rev-does-not-exist", "operator@example.test")
	if err == nil || !errors.Is(err, errRollbackRefused) {
		t.Fatalf("an unknown revision must be refused: %v", err)
	}
	if got := k.mutations(); len(got) != 0 {
		t.Errorf("a refused rollback issued %d kernel mutation(s): %v", len(got), got)
	}
}

// FAIL CLOSED. Not knowing the state is not a reason to run the branch that destroys everything -- it is the
// strongest reason not to.
func TestAnUnreadableRevisionStateRefusesWithoutTouchingTheKernel(t *testing.T) {
	k := newFakeKernel(t)
	a := rollbackApplier(t, k, "", fmt.Errorf("connection refused"))
	err := a.Rollback(context.Background(), "rev-unknown-state", "operator@example.test")
	if err == nil {
		t.Fatal("an unreadable revision state was treated as permission to roll back")
	}
	if !strings.Contains(err.Error(), "refusing rather than guessing") {
		t.Errorf("the error does not say it refused rather than guessed: %v", err)
	}
	if got := k.mutations(); len(got) != 0 {
		t.Errorf("a failed state read issued %d kernel mutation(s): %v", len(got), got)
	}
}

// THE RACE THE REVIEWER FOUND, AND IT WAS WIDER THAN THE GUARD.
//
// Both callers decide to roll back before a.rollback looks for a target:
//
//	applier.Rollback   reads the state, sees pending_confirmation, calls a.rollback;
//	watchdogLoop       reads PendingRevision(), sees it overdue, calls a.rollback.
//
// If /confirm commits in that gap the revision becomes ACTIVE and its predecessor becomes SUPERSEDED, so
// ActiveBundlePath finds no other active revision, prevBundle is empty, and the factory-clean branch
// destroys every guest bridge and stops DHCP -- for a configuration an operator had just confirmed.
//
// The watchdog's half of this predates the operator guard, so the fix is at the decision point inside
// a.rollback rather than in either caller.
func TestAConfirmationLandingMidRollbackDoesNotTearDownTheGuestNetwork(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)

	// The state read that the CALLER made: still mid-flight. The state read a.rollback makes at the
	// decision point: confirmed in between.
	calls := 0
	a.revStateFn = func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "pending_confirmation", nil
		}
		return "active", nil
	}
	// No previous active bundle -- exactly what a confirmation leaves behind.
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
	var events []string
	a.eventFn = func(_ context.Context, _ string, kind string, ok bool, detail map[string]any) {
		events = append(events, fmt.Sprintf("%s ok=%v %v", kind, ok, detail["refused"]))
	}

	if err := a.Rollback(context.Background(), "rev-confirmed-mid-flight", "operator@example.test"); err != nil {
		t.Fatalf("the caller's guard should have admitted this: %v", err)
	}
	if got := k.mutations(); len(got) != 0 {
		t.Fatalf("a confirmation landing mid-rollback destroyed %d kernel object(s): %v", len(got), got)
	}
	joined := strings.Join(events, " | ")
	if !strings.Contains(joined, "CONFIRMED") {
		t.Errorf("the refusal was not recorded as an event an operator can find: %s", joined)
	}
}

// Fail closed: a state that cannot be re-read at the decision point is not permission to destroy anything.
func TestAnUnreadableStateAtTheDecisionPointDoesNotTearDownTheGuestNetwork(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	calls := 0
	a.revStateFn = func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "pending_confirmation", nil
		}
		return "", fmt.Errorf("connection reset")
	}
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
	a.eventFn = func(context.Context, string, string, bool, map[string]any) {}

	if err := a.Rollback(context.Background(), "rev-unreadable", "operator@example.test"); err != nil {
		t.Fatalf("the caller's guard should have admitted this: %v", err)
	}
	if got := k.mutations(); len(got) != 0 {
		t.Fatalf("an unreadable state destroyed %d kernel object(s): %v", len(got), got)
	}
}

// AND THE FACTORY-CLEAN PATH MUST STILL WORK. It is the correct answer for a first apply that fails or
// expires, and narrowing the branch must not have taken that away.
func TestAFirstApplyThatExpiresStillComesBackFactoryClean(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.revStateFn = func(context.Context, string) (string, error) { return "pending_confirmation", nil }
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	marked := false
	a.markRolledFn = func(context.Context, string, string) error { marked = true; return nil }
	a.eventFn = func(context.Context, string, string, bool, map[string]any) {}

	if err := a.Rollback(context.Background(), "rev-first-apply", "operator@example.test"); err != nil {
		t.Fatalf("a first apply that expired must still roll back: %v", err)
	}
	if !marked {
		t.Error("the revision was not marked rolled back, so the factory-clean path did not complete")
	}
}
