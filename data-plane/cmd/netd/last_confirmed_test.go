package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func dbDown() error { return &net.OpError{Op: "dial", Err: errors.New("connection refused")} }

func TestWithTheDatabaseDownTheLastConfirmedNetworkComesUpAndChangesStayRefused(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	if err := a.saveLastConfirmed("rev-prev", "", "", oldIntent()); err != nil {
		t.Fatal(err)
	}
	a.activeIntentFn = func(context.Context) (string, string, []netcfg.GuestNetwork, error) { return "", "", nil, dbDown() }

	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("a boot from the snapshot claimed full convergence while the database is unreachable")
	}
	if _, err := a.Apply(context.Background(), "next", "tester"); !errors.Is(err, errRollbackIncomplete) || !strings.Contains(err.Error(), "database is unreachable") {
		t.Fatalf("a change was accepted while running provisionally: %v", err)
	}

	st.activeIntent = oldIntent()
	a.activeIntentFn = nil
	deadline := time.Now().Add(3 * time.Second)
	go a.blockedRecoveryLoop(context.Background(), 5*time.Millisecond, 20*time.Millisecond)
	for {
		if _, blocked := a.rollbackIncomplete(); !blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the appliance stayed blocked after the database answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestADatabaseThatAnswersDecidesAndTheSnapshotFollowsIt(t *testing.T) {
	a, _, st, _ := rollbackFixture(t)
	older := oldIntent()
	if err := a.saveLastConfirmed("rev-older", "", "", older); err != nil {
		t.Fatal(err)
	}
	st.activeIntent = oldIntent()
	a.ReconcileActiveOnBoot(context.Background())
	got, err := a.readLastConfirmed()
	if err != nil || got.Revision == "rev-older" {
		t.Fatalf("the snapshot was not brought to the database's confirmed revision: %+v %v", got, err)
	}
}

func TestADatabaseErrorThatIsAnAnswerIsNotTreatedAsUnreachable(t *testing.T) {
	if databaseUnreachable(&pgconn.PgError{Code: "42P01"}) {
		t.Fatal("a query error from a reachable database let the snapshot stand in")
	}
	if databaseUnreachable(ErrNoConfirmedActiveRevision) || databaseUnreachable(errors.New("intent unreadable")) {
		t.Fatal("an unreadable or absent revision was read as an outage")
	}
	if !databaseUnreachable(dbDown()) || !databaseUnreachable(context.DeadlineExceeded) {
		t.Fatal("an outage was not recognised")
	}
}

func TestATornOrAlteredSnapshotIsNeverUsed(t *testing.T) {
	a, _, _, _ := rollbackFixture(t)
	if err := a.saveLastConfirmed("rev-prev", "", "", oldIntent()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(a.lastConfirmedPath())
	if err := os.WriteFile(a.lastConfirmedPath(), []byte(strings.Replace(string(raw), "rev-prev", "rev-evil", 1)), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := a.readLastConfirmed(); err == nil {
		t.Fatal("an altered snapshot was accepted")
	}
	_ = os.WriteFile(a.lastConfirmedPath(), raw[:len(raw)/2], 0o640)
	if _, err := a.readLastConfirmed(); err == nil {
		t.Fatal("a torn snapshot was accepted")
	}
	a.activeIntentFn = func(context.Context) (string, string, []netcfg.GuestNetwork, error) { return "", "", nil, dbDown() }
	if a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("a boot with no usable snapshot and no database claimed to converge")
	}
}
