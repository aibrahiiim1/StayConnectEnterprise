package main

// A confirmed revision in the database must be the network that is running. Confirm and rollback exclude each other, and a revision that is not
// what is running (the service restarted inside its confirm window) cannot be confirmed.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func pendingFixture(t *testing.T) (*applier, *fakeKernel, *fakeStore) {
	t.Helper()
	a, k, st, _ := rollbackFixture(t)
	st.intents = map[string][]netcfg.GuestNetwork{"rev-failed": newIntent()}
	a.setInForce(newIntent(), "") // the pending revision is what is running
	return a, k, st
}

func TestConfirmCannotLandWhileARollbackIsRestoringThePreviousNetwork(t *testing.T) {
	a, _, st := pendingFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	inner := a.runFn
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "netplan" && len(args) > 0 && args[0] == "generate" {
			close(entered)
			<-release
		}
		return inner(ctx, name, args...)
	}
	rolled := make(chan struct{})
	go func() {
		a.rollbackExclusive(context.Background(), "rev-failed", "confirmation window elapsed")
		close(rolled)
	}()
	<-entered

	confirmed := make(chan error, 1)
	go func() { confirmed <- a.Confirm(context.Background(), "rev-failed", "operator") }()
	select {
	case err := <-confirmed:
		t.Fatalf("Confirm returned (%v) while the rollback was still restoring the network", err)
	case <-time.After(300 * time.Millisecond):
	}
	if s := st.state["rev-failed"]; s == "active" {
		t.Fatal("the database already says active while the wire is being rolled back")
	}

	close(release)
	<-rolled
	if err := <-confirmed; !errors.Is(err, errNotPending) {
		t.Fatalf("a confirm that lost the race to a rollback must be refused as not pending: %v", err)
	}
	if st.state["rev-failed"] != "rolled_back" {
		t.Fatalf("revision is %q, want rolled_back", st.state["rev-failed"])
	}
}

func TestRollbackAfterAConfirmIsRefusedAndTouchesNothing(t *testing.T) {
	a, k, st := pendingFixture(t)
	st.activeID, st.activeIntent = "rev-failed", newIntent()
	if err := a.Confirm(context.Background(), "rev-failed", "operator"); err != nil {
		t.Fatal(err)
	}
	k.reset()
	a.rollbackExclusive(context.Background(), "rev-failed", "confirmation window elapsed")
	if st.state["rev-failed"] != "active" {
		t.Fatalf("a rollback undid a confirmed revision: %q", st.state["rev-failed"])
	}
	for _, c := range k.cmds {
		if strings.Contains(c, "link del") || strings.Contains(c, "nft -f") {
			t.Fatalf("a refused rollback changed the network: %s", c)
		}
	}
}

func TestRevisionThatIsNotWhatIsRunningCannotBeConfirmed(t *testing.T) {
	a, _, st := pendingFixture(t)
	a.setInForce(oldIntent(), "") // the service restarted and put the previous configuration back
	err := a.Confirm(context.Background(), "rev-failed", "operator")
	if !errors.Is(err, errNotApplied) {
		t.Fatalf("a revision that is not running was confirmed: %v", err)
	}
	if st.state["rev-failed"] != "pending_confirmation" {
		t.Fatalf("state changed to %q", st.state["rev-failed"])
	}

	a.clearInForce()
	if err := a.Confirm(context.Background(), "rev-failed", "operator"); !errors.Is(err, errNotApplied) {
		t.Fatalf("with nothing in force the revision was confirmed: %v", err)
	}
}

func TestConfirmingWhatIsRunningStillWorks(t *testing.T) {
	a, _, st := pendingFixture(t)
	st.activeID, st.activeIntent = "rev-failed", newIntent()
	if err := a.Confirm(context.Background(), "rev-failed", "operator"); err != nil {
		t.Fatal(err)
	}
	if st.state["rev-failed"] != "active" {
		t.Fatalf("state %q", st.state["rev-failed"])
	}
}

func TestIntentComparisonSurvivesTheDatabaseRoundTrip(t *testing.T) {
	x := oldIntent()
	x[0].Reservations, x[0].DNSServers = nil, nil
	var stored []netcfg.GuestNetwork
	if err := json.Unmarshal(mustJSON(t, x), &stored); err != nil {
		t.Fatal(err)
	}
	if !sameIntent(x, stored) {
		t.Fatal("an intent differs from itself after a database round trip, so no revision could ever be confirmed")
	}
	stored[0].GatewayIP = "10.99.0.1"
	if sameIntent(x, stored) {
		t.Fatal("different intents compare as the same")
	}
}
