package main

// AD-28: the system-network state names the pending revision, and confirm / rollback that name a revision act only on that one.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func revPendingFixture(t *testing.T) *sysNetMgr {
	m, ps, _ := testSysnet(t)
	_ = ps
	m.writePending(pendingChange{RevisionID: "rev-reviewed", Deadline: time.Now().Add(time.Minute).Unix(), Backup: t.TempDir(), MgmtURL: "https://192.0.2.10"})
	return m
}

func TestStateReportsThePendingRevision(t *testing.T) {
	m := revPendingFixture(t)
	st := m.readState(context.Background())
	if st.Pending == nil || st.Pending.RevisionID != "rev-reviewed" {
		t.Fatalf("pending state does not name its revision: %+v", st.Pending)
	}
}

func TestConfirmNamingAnotherRevisionIsRefusedAndKeepsThePendingChange(t *testing.T) {
	m := revPendingFixture(t)
	err := m.confirmRevision(context.Background(), "op", "", "", "rev-older")
	if !errors.Is(err, errRevisionMismatch) {
		t.Fatalf("got %v", err)
	}
	if _, ok := m.readPending(); !ok {
		t.Fatal("a refused confirm cleared the pending change")
	}
}

func TestConfirmNamingThePendingRevisionOrNoneStillWorks(t *testing.T) {
	m := revPendingFixture(t)
	if err := m.confirmRevision(context.Background(), "op", "", "", "rev-reviewed"); err != nil {
		t.Fatal(err)
	}
	m = revPendingFixture(t)
	if err := m.confirm(context.Background(), "op", "", ""); err != nil {
		t.Fatalf("an unnamed confirm (older callers) must keep working: %v", err)
	}
}

func TestRollbackNamingAnotherRevisionIsRefusedBeforeAnythingIsRestored(t *testing.T) {
	m := revPendingFixture(t)
	if err := m.rollbackRevision(context.Background(), "op", "", "", "rev-older"); !errors.Is(err, errRevisionMismatch) {
		t.Fatalf("got %v", err)
	}
	if _, ok := m.readPending(); !ok {
		t.Fatal("a refused rollback cleared the pending change")
	}
}
