package licstate

import (
	"context"
	"testing"
	"time"

	lic "github.com/stayconnect/enterprise/license"
)

func (f *fixture) docEnvelope(t *testing.T, mutate func(*lic.Document)) []byte {
	t.Helper()
	now := time.Now().UTC()
	d := &lic.Document{
		LicenseID: "11111111-1111-1111-1111-111111111111", Status: lic.DocActive,
		TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
		IssuedAt: now.Add(-time.Hour), ValidUntil: now.AddDate(0, 6, 0), SchemaVersion: lic.CurrentSchemaVersion,
		Modules: lic.Modules{}, LicenseVersion: 1, Limits: lic.Limits{MaxConcurrentGuestSessions: 50},
	}
	mutate(d)
	env, err := f.signer.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := env.Encode()
	return raw
}

// AD-15: a v4 licence that names no appliance is refused at install; a licence field the appliance cannot compare
// (blank local value) is refused instead of silently passing.
func TestInstallRefusesUnboundAndUncomparableLicences(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	m.SetLocalIdentity(lic.LocalIdentity{ApplianceID: "app-1", IdentityKeyFingerprint: "aaaa", Serial: "SC-1", HardwareFingerprint: "hw1"})

	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) {})); err == nil {
		t.Fatal("an unbound v4 licence must be refused")
	}
	// A licence for another hotel but bound to nothing this appliance can check is the same case.
	m.SetLocalIdentity(lic.LocalIdentity{ApplianceID: "app-1", Serial: "SC-1", HardwareFingerprint: "hw1"}) // identity key blank
	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) { d.IdentityKeyFingerprint = "aaaa" })); err == nil {
		t.Fatal("a licence naming an identity key must be refused while the appliance's own key is unknown")
	}
	m.SetLocalIdentity(lic.LocalIdentity{ApplianceID: "app-1", IdentityKeyFingerprint: "aaaa", Serial: "SC-1", HardwareFingerprint: "hw1"})
	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) {
		d.IdentityKeyFingerprint, d.ApplianceID, d.ApplianceIDs = "aaaa", "app-1", []string{"app-9"}
	})); err == nil {
		t.Fatal("an appliance outside the licence's appliance list must be refused")
	}
	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) {
		d.IdentityKeyFingerprint, d.ApplianceID, d.ApplianceSerial, d.HardwareFingerprint = "aaaa", "app-1", "SC-1", "hw1"
		d.ApplianceIDs = []string{"app-1"}
	})); err != nil {
		t.Fatalf("a fully bound licence for this appliance installs: %v", err)
	}
}

// AD-15(b): with a granting signed assignment, the licence must name the same customer and site.
func TestInstallComparesLicenceWithTheSignedAssignment(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	m.SetLocalIdentity(lic.LocalIdentity{ApplianceID: "app-1", IdentityKeyFingerprint: "aaaa"})
	bind := func(d *lic.Document) { d.IdentityKeyFingerprint, d.ApplianceID = "aaaa", "app-1" }

	tenant, site, granted := "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", true
	m.SetAssignmentLookup(func() (string, string, bool) { return tenant, site, granted })

	if _, err := m.Install(context.Background(), f.docEnvelope(t, bind)); err != nil {
		t.Fatalf("a licence for the assigned customer and site installs: %v", err)
	}
	tenant = "99999999-9999-9999-9999-999999999999"
	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) { bind(d); d.LicenseVersion = 2 })); err == nil {
		t.Fatal("a licence for another customer must be refused when an assignment exists")
	}
	tenant, site = "22222222-2222-2222-2222-222222222222", "88888888-8888-8888-8888-888888888888"
	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) { bind(d); d.LicenseVersion = 2 })); err == nil {
		t.Fatal("a licence for another site must be refused when an assignment exists")
	}
	granted = false // no assignment yet: nothing to compare, a licence may arrive first
	if _, err := m.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) { bind(d); d.LicenseVersion = 2 })); err != nil {
		t.Fatalf("with no assignment the licence installs as before: %v", err)
	}
}

// AD-15 cannot brick a running appliance: a legacy unbound v4 licence that is ALREADY installed keeps being
// evaluated (the install-time refusal does not run at Load), and the next bound licence replaces it.
func TestAlreadyInstalledUnboundLicenceKeepsRunning(t *testing.T) {
	f := newFixture(t)
	// Written the way an older appliance would have: through the store, bypassing the Manager's new install check.
	old := New(nil, "", f.dir, f.pubPath, true)
	old.syncLimitsFn = func(context.Context, *lic.Document) error { return nil }
	if _, err := old.store.Install(f.docEnvelope(t, func(d *lic.Document) {}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rebooted := New(nil, "", f.dir, f.pubPath, true)
	rebooted.syncLimitsFn = func(context.Context, *lic.Document) error { return nil }
	rebooted.SetLocalIdentity(lic.LocalIdentity{ApplianceID: "app-1", IdentityKeyFingerprint: "aaaa", Serial: "SC-1"})
	rebooted.Load(context.Background())
	if rebooted.WrongHardware() != "" || !rebooted.AllowsNewSessions() || !rebooted.HasUsableLicense() {
		t.Fatalf("an installed unbound licence must keep running: wrong=%q state=%s", rebooted.WrongHardware(), rebooted.State())
	}
	if _, err := rebooted.Install(context.Background(), f.docEnvelope(t, func(d *lic.Document) {
		d.LicenseVersion = 2
		d.IdentityKeyFingerprint, d.ApplianceID, d.ApplianceIDs = "aaaa", "app-1", []string{"app-1"}
	})); err != nil {
		t.Fatalf("the bound renewal replaces it: %v", err)
	}
}
