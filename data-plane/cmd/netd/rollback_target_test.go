package main

// An operator rollback must have a target or refuse.
// On a confirmed revision no previous active bundle exists and the factory-clean branch would destroy every guest bridge; the refusal must issue no kernel command.

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
	// Makes damage observable if the guard falls through to the real rollback.
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
	a.eventFn = func(context.Context, string, string, bool, map[string]any) {}
	return a
}

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
	for _, want := range []string{"CONFIRMED", "apply that as a new revision"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if got := k.mutations(); len(got) != 0 {
		t.Errorf("a refused rollback issued %d kernel mutation(s): %v", len(got), got)
	}
}

// Rollback is meaningful only while the previous revision is still active.
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

// Revisions not in force have nothing to undo and must not reach the tear-down branch.
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

// Fail closed: unknown state is a reason not to run the destructive branch.
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

// Race: applier.Rollback and the watchdog each decide to roll back before a.rollback looks for a target. If /confirm commits in that gap, no previous bundle exists and the factory-clean branch destroys a just-confirmed configuration.
// The fix is at the decision point inside a.rollback, not in either caller.
func TestAConfirmationLandingMidRollbackDoesNotTearDownTheGuestNetwork(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)

	// The caller saw mid-flight state; a.rollback's own read at the decision point sees it confirmed.
	calls := 0
	a.revStateFn = func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "pending_confirmation", nil
		}
		return "active", nil
	}
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

// The factory-clean path must still work for a first apply that fails or expires.
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
