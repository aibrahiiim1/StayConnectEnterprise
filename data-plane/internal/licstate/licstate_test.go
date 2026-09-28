package licstate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	lic "github.com/stayconnect/enterprise/license"
)

type fixture struct {
	dir, pubPath string
	signer       *lic.Signer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubPath := filepath.Join(dir, "vendor.pub")
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	return &fixture{dir: filepath.Join(dir, "license"), pubPath: pubPath, signer: lic.NewSigner(priv)}
}

func (f *fixture) envelope(t *testing.T, idFpr string, version int64) []byte {
	t.Helper()
	now := time.Now().UTC()
	d := &lic.Document{
		LicenseID: "11111111-1111-1111-1111-111111111111", Status: lic.DocActive,
		TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
		IssuedAt: now.Add(-time.Hour), ValidUntil: now.AddDate(0, 6, 0), SchemaVersion: lic.CurrentSchemaVersion, Modules: lic.Modules{},
		IdentityKeyFingerprint: idFpr, ApplianceSerial: "SC-1", LicenseVersion: version,
		Limits: lic.Limits{MaxConcurrentGuestSessions: 50},
	}
	env, err := f.signer.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := env.Encode()
	return raw
}

// F4: the hardware/identity binding is checked on EVERY evaluation, not only at install. A licence already on
// disk that belongs to another appliance (a copied licence directory, or an identity replaced under it) must be
// refused after a reboot -- which is exactly what a fresh Manager + Load is.
func TestBindingIsCheckedAtLoad(t *testing.T) {
	f := newFixture(t)
	mine := lic.LocalIdentity{IdentityKeyFingerprint: "aaaa", Serial: "SC-1"}

	m := New(nil, "", f.dir, f.pubPath, true)
	m.SetLocalIdentity(mine)
	if _, err := m.Install(context.Background(), f.envelope(t, "aaaa", 1)); err != nil {
		t.Fatalf("install for this appliance: %v", err)
	}
	if !m.AllowsNewSessions() || m.WrongHardware() != "" || !m.HasUsableLicense() {
		t.Fatalf("a licence bound to this appliance must be usable: state=%s", m.State())
	}

	// "Reboot" as a different identity over the same licence directory.
	rebooted := New(nil, "", f.dir, f.pubPath, true)
	rebooted.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "bbbb", Serial: "SC-1"})
	rebooted.Load(context.Background())
	if rebooted.WrongHardware() == "" {
		t.Fatal("a licence bound to another appliance must be flagged at load")
	}
	if rebooted.AllowsNewSessions() || rebooted.State() != lic.StateUnlicensed {
		t.Fatalf("a licence for another appliance must fail closed, got %s", rebooted.State())
	}
	if rebooted.MaxConcurrentOnlineGuests() != 0 || rebooted.FeatureEnabled(FeatPMS) || rebooted.HasUsableLicense() {
		t.Fatal("a refused licence must grant no capacity and no feature")
	}

	// Same licence, the right identity again: usable, and the periodic Load clears the refusal.
	rebooted.SetLocalIdentity(mine)
	rebooted.Load(context.Background())
	if rebooted.WrongHardware() != "" || !rebooted.AllowsNewSessions() {
		t.Fatal("the right identity must make the licence usable again on the next evaluation")
	}
}

// A WAN MAC change is a warning that survives a reboot and never stops the licence.
func TestWANMismatchIsRederivedAtLoadAndDoesNotRefuse(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	d := &lic.Document{
		LicenseID: "11111111-1111-1111-1111-111111111112", Status: lic.DocActive,
		TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
		IssuedAt: now.Add(-time.Hour), ValidUntil: now.AddDate(0, 6, 0), SchemaVersion: lic.CurrentSchemaVersion, Modules: lic.Modules{},
		IdentityKeyFingerprint: "aaaa", WANMAC: "00:11:22:33:44:55", LicenseVersion: 1,
	}
	env, _ := f.signer.Sign(d)
	raw, _ := env.Encode()
	m := New(nil, "", f.dir, f.pubPath, true)
	m.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "aaaa", WANMAC: "66:77:88:99:aa:bb"})
	if _, err := m.Install(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	rebooted := New(nil, "", f.dir, f.pubPath, true)
	rebooted.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "aaaa", WANMAC: "66:77:88:99:aa:bb"})
	rebooted.Load(context.Background())
	if rebooted.HardwareMismatch() == "" {
		t.Fatal("the WAN MAC warning must survive a reboot")
	}
	if !rebooted.AllowsNewSessions() || rebooted.WrongHardware() != "" {
		t.Fatal("a WAN MAC change must not stop the licence")
	}
}

// F10: no licence -> ask every fastEvery; licence installed -> the slow cadence.
func TestFetchCadence(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	m.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "aaaa", Serial: "SC-1"})
	if got := m.nextFetchInterval(6 * time.Hour); got != DefaultFastFetchInterval {
		t.Fatalf("unlicensed appliance must poll every minute, got %s", got)
	}
	if _, err := m.Install(context.Background(), f.envelope(t, "aaaa", 1)); err != nil {
		t.Fatal(err)
	}
	if got := m.nextFetchInterval(6 * time.Hour); got != 6*time.Hour {
		t.Fatalf("licensed appliance keeps the 6 h cadence, got %s", got)
	}
	// A licence for another appliance is not a licence: back to the fast cadence so a corrected one arrives.
	m.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "bbbb", Serial: "SC-1"})
	m.Load(context.Background())
	if got := m.nextFetchInterval(6 * time.Hour); got != DefaultFastFetchInterval {
		t.Fatalf("wrong-hardware licence must poll fast, got %s", got)
	}
}

// The loop fetches immediately, then on the fast cadence while unlicensed, and at once on Kick even when the
// cadence is slow; every outcome reaches the observer.
func TestFetchLoopFastPollAndKick(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	m.fastEvery = 20 * time.Millisecond
	var calls, observed atomic.Int32
	m.OnFetch(func(err error) {
		observed.Add(1)
		if !errors.Is(err, ErrNoLicenseYet) {
			t.Errorf("observer got %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.runFetchLoop(ctx, func(context.Context) error { calls.Add(1); return ErrNoLicenseYet }, time.Hour)

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() < 3 {
		t.Fatalf("unlicensed appliance must re-poll on the fast cadence, got %d fetches", calls.Load())
	}
	if observed.Load() == 0 {
		t.Fatal("fetch outcomes must reach the observer")
	}

	// Slow cadence: only a Kick can trigger the next fetch within the test window.
	m.mu.Lock()
	m.fastEvery = time.Hour
	m.mu.Unlock()
	time.Sleep(50 * time.Millisecond) // let the loop settle on an hour-long timer
	before := calls.Load()
	m.Kick()
	deadline = time.Now().Add(2 * time.Second)
	for calls.Load() == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() == before {
		t.Fatal("Kick must trigger an immediate fetch")
	}
}

// v4: modules are the sole authority. A v4 licence authorising only
// hospitality enables PMS and nothing financial.
func TestModulesAreTheSoleAuthority(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	mods := lic.Modules{lic.ModuleHospitality: {}}
	d := &lic.Document{
		LicenseID: "11111111-1111-1111-1111-111111111119", Status: lic.DocActive,
		TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
		IssuedAt: now.Add(-time.Hour), ValidUntil: now.AddDate(0, 6, 0), SchemaVersion: lic.CurrentSchemaVersion,
		IdentityKeyFingerprint: "aaaa", LicenseVersion: 1, Modules: mods, Features: lic.ProjectFeatures(mods),
	}
	env, err := f.signer.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := env.Encode()
	m := New(nil, "", f.dir, f.pubPath, true)
	m.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "aaaa"})
	if _, err := m.Install(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if !m.FeatureEnabled(FeatPMS) || !m.ModuleEnabled(lic.ModuleHospitality) {
		t.Fatal("hospitality must be enabled")
	}
	for _, id := range []string{lic.ModulePaidAccess, lic.ModuleCardPayment, lic.ModuleRoomCharge, lic.ModuleSMSOTP} {
		if m.ModuleEnabled(id) {
			t.Fatalf("%s enabled without authorisation", id)
		}
	}
	if m.FeatureEnabled(FeatPaidWiFi) {
		t.Fatal("paid_wifi must follow paid_access")
	}
}
