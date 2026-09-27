//go:build integration

package main

// LICENCES FOR THE INTEGRATION FIXTURES.
//
// Every guest path now asks the appliance's licence before it admits anybody -- the room sign-in included --
// so a fixture server with no licence manager is, correctly, an appliance that refuses every guest. These
// helpers give a fixture the licence it means to test with, built the way the appliance builds it: a real
// licstate.Manager, and where a limit or a state matters, a real vendor-signed document installed through the
// same verification path a licence upload takes. Nothing here short-circuits the decision being tested.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	lic "github.com/stayconnect/enterprise/license"
)

// devLicence is what a development build runs with when no licence is installed: Active, every feature,
// no concurrent-guest limit. It is the licence every fixture that is NOT about licensing uses, so those tests
// keep measuring what they were written to measure.
func devLicence() *licstate.Manager {
	return licstate.New(nil, "", "", "", false)
}

// signedLicence installs a vendor-signed licence with the given concurrent-guest cap and PMS entitlement.
// expired issues one whose validity (and grace) ended ten days ago. required=true is the production profile:
// the signed document is the only thing that can admit a guest.
func signedLicence(t *testing.T, maxGuests int, pms, expired bool) *licstate.Manager {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(dir, "vendor.pub")
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	issued, until := now.Add(-time.Hour), now.AddDate(0, 6, 0)
	if expired {
		issued, until = now.AddDate(0, 0, -40), now.AddDate(0, 0, -10)
	}
	doc := &lic.Document{
		LicenseID: "44444444-4444-4444-8444-444444444444", Status: lic.DocActive,
		TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
		IssuedAt: issued, ValidUntil: until, SchemaVersion: lic.CurrentSchemaVersion, LicenseVersion: 1,
		MaxConcurrentOnlineGuests: maxGuests,
		Features:                  lic.Features{PMS: pms},
	}
	env, err := lic.NewSigner(priv).Sign(doc)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	m := licstate.New(nil, "", filepath.Join(dir, "license"), pubPath, true)
	if _, err := m.Install(context.Background(), raw); err != nil {
		t.Fatalf("install the fixture licence: %v", err)
	}
	return m
}
