package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// orphanFixture is an appliance with an identity, a licence and a client certificate on disk, whose record
// Central has just been confirmed to have deleted.
type orphanFixture struct {
	srv      *server
	paths    orphanPaths
	restarts int
}

func newOrphanFixture(t *testing.T) *orphanFixture {
	t.Helper()
	root := t.TempDir()
	p := orphanPaths{
		IdentityDir: filepath.Join(root, "identity"), AssignmentDir: filepath.Join(root, "assignment"),
		LicenseDir: filepath.Join(root, "license"), CertDir: filepath.Join(root, "certs"),
		Marker: filepath.Join(root, "removed-from-central.json"),
	}
	for f, body := range map[string]string{
		filepath.Join(p.IdentityDir, "identity.json"): `{"appliance_id":"appl-1"}`,
		filepath.Join(p.IdentityDir, "identity.key"):  "key",
		filepath.Join(p.LicenseDir, "current.json"):   "{}",
		filepath.Join(p.CertDir, "client.crt"):        "crt",
		filepath.Join(p.CertDir, "mtls-client.key"):   "key",
		filepath.Join(p.CertDir, "ca-bundle.crt"):     "ca",
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := &orphanFixture{paths: p}
	f.srv = &server{applID: "appl-1", serial: "SC-1", restartFn: func() { f.restarts++ }}
	return f
}

func (f *orphanFixture) adopt(t *testing.T, docs ...*assignment.Document) {
	t.Helper()
	st := &assignment.Store{Dir: f.paths.AssignmentDir}
	for _, d := range docs {
		if err := st.Adopt(d); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// NEVER HELD A CUSTOMER: a waiting appliance whose record was deleted may clear its identity and register
// again as a fresh waiting appliance, exactly as before.
func TestOrphanThatNeverHeldACustomerMayRegisterAgain(t *testing.T) {
	f := newOrphanFixture(t)
	if got := f.srv.handleConfirmedOrphan(context.Background(), f.paths); got != "reset" {
		t.Fatalf("outcome %q, want reset", got)
	}
	if exists(filepath.Join(f.paths.IdentityDir, "identity.key")) {
		t.Fatal("the identity was kept; the restarted scd would not register again")
	}
	if exists(f.paths.Marker) || f.srv.isRemovedFromCentral() {
		t.Fatal("a never-assigned appliance was marked removed")
	}
	if exists(filepath.Join(f.paths.LicenseDir, "current.json")) || exists(filepath.Join(f.paths.CertDir, "client.crt")) {
		t.Fatal("the licence or the client certificate survived")
	}
	if f.restarts != 1 {
		t.Fatalf("restarted %d times, want 1", f.restarts)
	}
	if !mayRegister(true, "https://central", loadRemoved(f.paths.Marker)) {
		t.Fatal("the restarted appliance may not register, so it would never reappear as waiting")
	}
}

// HAS HELD A CUSTOMER: it stops admitting guests, keeps its identity and data, writes the durable marker, and
// never registers again -- not now and not after a restart.
func TestOrphanThatHeldACustomerIsBlockedAndKeepsItsIdentity(t *testing.T) {
	f := newOrphanFixture(t)
	f.adopt(t, &assignment.Document{AssignmentID: "a-1", ApplianceID: "appl-1", TenantID: "t-1", SiteID: "s-1",
		Version: 3, State: assignment.StateAssigned, TenantName: "Hotel A"})

	if got := f.srv.handleConfirmedOrphan(context.Background(), f.paths); got != "removed" {
		t.Fatalf("outcome %q, want removed", got)
	}
	if !exists(filepath.Join(f.paths.IdentityDir, "identity.key")) {
		t.Fatal("the identity key was cleared; a new key would be generated and registered")
	}
	if !exists(filepath.Join(f.paths.AssignmentDir, "assignment.json")) {
		t.Fatal("the assignment was cleared; the local data would lose its owner")
	}
	if exists(filepath.Join(f.paths.LicenseDir, "current.json")) || exists(filepath.Join(f.paths.CertDir, "client.crt")) ||
		exists(filepath.Join(f.paths.CertDir, "mtls-client.key")) {
		t.Fatal("the licence or client credentials survived; the appliance could keep serving")
	}
	if !exists(filepath.Join(f.paths.CertDir, "ca-bundle.crt")) {
		t.Fatal("more than the client credential was removed")
	}
	if !f.srv.isRemovedFromCentral() {
		t.Fatal("this process does not know it was removed")
	}

	// AFTER A RESTART: the marker alone carries the state.
	rec := loadRemoved(f.paths.Marker)
	if rec == nil {
		t.Fatal("no marker was written; a restart would register again")
	}
	if mayRegister(true, "https://central", rec) {
		t.Fatal("a removed appliance may register again")
	}
	restarted := &server{applID: "appl-1"}
	restarted.removed.Store(rec)
	body := restarted.licenseRefusal("")
	if body == nil || body["error"] != removedFromCentralCode {
		t.Fatalf("a removed appliance admits guests: %v", body)
	}
	st := computeCentralStatus(centralInputs{ApplianceID: "appl-1", Removed: true,
		Asg: assignment.Resolution{Outcome: assignment.OutcomeGranted}})
	if st.Activation != activationRetired || st.Details.Reason != removedFromCentralCode {
		t.Fatalf("status reads %q / %q; want retired / %s", st.Activation, st.Details.Reason, removedFromCentralCode)
	}
	// No way back in through a licence file or an activation package.
	for name, h := range map[string]http.HandlerFunc{
		"licence upload":     restarted.licenseInstall,
		"offline package":    restarted.centralOfflinePackage,
		"activation request": restarted.setupActivationRequest,
	} {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`))))
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		if rr.Code != http.StatusConflict || out["error"] != removedFromCentralCode {
			t.Fatalf("%s on a removed appliance answered %d %s", name, rr.Code, rr.Body.String())
		}
	}
}

// A TERMINAL ASSIGNMENT is the retired case: the appliance held a customer before it was retired, so a later
// deletion of the retired record must not let it register again under a new key either.
func TestOrphanAfterATerminalAssignmentIsBlocked(t *testing.T) {
	f := newOrphanFixture(t)
	f.adopt(t,
		&assignment.Document{AssignmentID: "a-1", ApplianceID: "appl-1", TenantID: "t-1", SiteID: "s-1",
			Version: 3, State: assignment.StateAssigned},
		&assignment.Document{AssignmentID: "a-1", ApplianceID: "appl-1", Version: 4, State: assignment.StateRevoked})
	if got := f.srv.handleConfirmedOrphan(context.Background(), f.paths); got != "removed" {
		t.Fatalf("outcome %q, want removed", got)
	}
	if !exists(filepath.Join(f.paths.IdentityDir, "identity.key")) || loadRemoved(f.paths.Marker) == nil {
		t.Fatal("a retired appliance lost its identity or was not marked removed")
	}
}

// An unreadable marker is still a marker: a file that exists and cannot be parsed must not be read as
// permission to register.
func TestAnUnreadableMarkerStillBlocksRegistration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "removed-from-central.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := loadRemoved(p); rec == nil || mayRegister(true, "https://central", rec) {
		t.Fatal("an unreadable marker let the appliance register")
	}
	if loadRemoved(filepath.Join(t.TempDir(), "absent.json")) != nil {
		t.Fatal("no marker was read as removed")
	}
}
