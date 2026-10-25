package main

// The file Kea starts from (what a crash, a package upgrade or a reboot loads) must hold the CONFIRMED configuration and nothing else, be written
// without ever being half-written, and report a failed write (audit: the old `config-write` result was thrown away, and wrote whatever was live).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

func keaFixture(t *testing.T) (*applier, *fakeStore) {
	t.Helper()
	k := newFakeKernel(t)
	st := newFakeStore(newIntent())
	a := applyFixture(t, k, st)
	st.activeID, st.activeBundle, st.activeIntent = "rev-confirmed", t.TempDir(), oldIntent()
	st.state["rev-confirmed"] = "active"
	return a, st
}

func wantKeaFile(t *testing.T, a *applier, intent []netcfg.GuestNetwork) string {
	t.Helper()
	return string(mustJSON(t, map[string]any{"Dhcp4": a.renderKea(intent)}))
}

func readKeaFile(t *testing.T, a *applier) string {
	t.Helper()
	raw, err := os.ReadFile(a.keaConfPath())
	if err != nil {
		t.Fatalf("the Kea file was not written: %v", err)
	}
	return string(raw)
}

func TestPersistWritesTheConfirmedConfigurationAtomically(t *testing.T) {
	a, _ := keaFixture(t)
	if err := a.persistConfirmedKea(context.Background(), "rev-confirmed"); err != nil {
		t.Fatal(err)
	}
	if got, want := readKeaFile(t, a), wantKeaFile(t, a, oldIntent()); got != want {
		t.Fatalf("the file does not hold the confirmed configuration:\n%s", got)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(readKeaFile(t, a)), &doc); err != nil || doc["Dhcp4"] == nil {
		t.Fatalf("the file is not a Kea document: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(a.keaConfPath()))
	if len(entries) != 1 {
		t.Fatalf("temporary files were left beside the config: %v", entries)
	}
	if ok, _ := a.keaDiskState(); !ok {
		t.Fatal("a successful write is reported as a failure")
	}
}

// What Kea starts from follows the CONFIRMED revision even while another is awaiting confirmation and is the intent in force.
func TestPersistFollowsTheConfirmedRevisionNotTheOneInForce(t *testing.T) {
	a, _ := keaFixture(t)
	a.setInForce(newIntent(), "") // a change is pending: the live configuration is the new one
	if err := a.persistConfirmedKea(context.Background(), "rev-confirmed"); err != nil {
		t.Fatal(err)
	}
	got := readKeaFile(t, a)
	if got != wantKeaFile(t, a, oldIntent()) {
		t.Fatal("the unconfirmed intent reached the file a cold Kea starts from")
	}
	if strings.Contains(got, "10.21.0.0/22") {
		t.Fatal("the pending change's subnet is in the Kea file")
	}
}

// Pushing the live configuration (an apply, the portal refresh) never touches the file.
func TestPushingTheLiveConfigurationNeverWritesTheKeaFile(t *testing.T) {
	a, _ := keaFixture(t)
	if err := a.pushKea(a.renderKea(newIntent())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.keaConfPath()); !os.IsNotExist(err) {
		t.Fatalf("a push wrote the Kea file: %v", err)
	}
}

func TestPersistFailureIsReportedAndRetriedByThePortalRefresh(t *testing.T) {
	a, _ := keaFixture(t)
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := a.keaConfFile
	a.keaConfFile = filepath.Join(blocker, "kea-dhcp4.conf") // a directory cannot be made under a regular file

	if err := a.persistConfirmedKea(context.Background(), "rev-confirmed"); err == nil {
		t.Fatal("an unwritable Kea file was reported as written")
	}
	ok, detail := a.keaDiskState()
	if ok || detail == "" {
		t.Fatalf("the failure is not visible: ok=%v detail=%q", ok, detail)
	}
	status := a.keaDiskStatus()
	if status["holds_confirmed_configuration"] != false {
		t.Fatalf("health does not report it: %v", status)
	}

	// The cause is fixed; the periodic refresh writes it without anyone asking.
	a.keaConfFile = good
	a.setInForce(oldIntent(), "")
	a.portalLinkFn = func(string) (bool, bool) { return false, false }
	a.hostAddrsFn = func(context.Context) ([]netcfg.HostAddr, error) { return nil, nil }
	a.portalRefresh(context.Background())
	if ok, _ := a.keaDiskState(); !ok {
		t.Fatal("the refresh did not retry the write")
	}
	if readKeaFile(t, a) != wantKeaFile(t, a, oldIntent()) {
		t.Fatal("the retried write is not the confirmed configuration")
	}
}

func TestUnchangedFileIsNotRewritten(t *testing.T) {
	a, _ := keaFixture(t)
	if err := a.persistConfirmedKea(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(a.keaConfPath())
	// An unwritable directory would fail a rewrite; an unchanged file must not need one.
	if err := os.Chmod(filepath.Dir(a.keaConfPath()), 0o555); err == nil {
		defer func() { _ = os.Chmod(filepath.Dir(a.keaConfPath()), 0o755) }()
	}
	if err := a.persistConfirmedKea(context.Background(), "r"); err != nil {
		t.Fatalf("an unchanged file was rewritten: %v", err)
	}
	st2, _ := os.Stat(a.keaConfPath())
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("the file was rewritten although nothing changed")
	}
}

func TestConfirmSavesTheConfirmedConfigurationButAFailedWriteDoesNotUnconfirm(t *testing.T) {
	a, st := keaFixture(t)
	st.state["rev-new"] = "pending_confirmation"
	st.activeID, st.activeIntent = "rev-new", newIntent() // MarkActive makes this the confirmed one
	a.setInForce(newIntent(), "")
	if err := a.Confirm(context.Background(), "rev-new", "tester"); err != nil {
		t.Fatal(err)
	}
	if readKeaFile(t, a) != wantKeaFile(t, a, newIntent()) {
		t.Fatal("confirming did not save the newly confirmed configuration")
	}

	// A failed write is reported, and the confirmation stands.
	st.state["rev-3"] = "pending_confirmation"
	blocker := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(blocker, []byte("x"), 0o644)
	a.keaConfFile = filepath.Join(blocker, "kea-dhcp4.conf")
	if err := a.Confirm(context.Background(), "rev-3", "tester"); err != nil {
		t.Fatalf("a failed Kea file write unconfirmed the revision: %v", err)
	}
	if st.state["rev-3"] != "active" {
		t.Fatalf("revision state %q", st.state["rev-3"])
	}
	if ok, _ := a.keaDiskState(); ok {
		t.Fatal("the failed write is not visible")
	}
}

func TestRollbackSavesThePreviousConfirmedConfigurationToTheKeaFile(t *testing.T) {
	a, _, _, _ := rollbackFixture(t)
	if steps := a.rollback(context.Background(), "rev-failed", "x"); len(steps) != 0 {
		t.Fatalf("setup: %v", steps)
	}
	if readKeaFile(t, a) != wantKeaFile(t, a, oldIntent()) {
		t.Fatal("after a rollback the Kea file is not the previous confirmed configuration")
	}
}

func TestBootReconcileSavesTheConfirmedConfigurationToTheKeaFile(t *testing.T) {
	a, st, _ := bootFixture(t)
	if !a.ReconcileActiveOnBoot(context.Background()) {
		t.Fatal("setup: the boot reconcile did not converge")
	}
	if readKeaFile(t, a) != wantKeaFile(t, a, st.activeIntent) {
		t.Fatal("boot did not save the confirmed configuration, so a cold Kea would start from a stale file")
	}
}
