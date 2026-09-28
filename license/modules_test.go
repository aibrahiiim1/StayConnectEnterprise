package license

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestModulesFromIDsRejectsUnknownAndUnmetDependencies(t *testing.T) {
	if _, err := ModulesFromIDs([]string{"no_such_module"}); err == nil {
		t.Fatal("unknown module accepted")
	}
	if _, err := ModulesFromIDs([]string{ModuleCardPayment}); err == nil {
		t.Fatal("card_payment without paid_access accepted")
	}
	if _, err := ModulesFromIDs([]string{ModuleRoomCharge, ModulePaidAccess}); err == nil {
		t.Fatal("room_charge without hospitality accepted")
	}
	m, err := ModulesFromIDs([]string{ModuleRoomCharge, ModulePaidAccess, ModuleHospitality})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ModuleIDs(); !reflect.DeepEqual(got, []string{"hospitality", "paid_access", "room_charge"}) {
		t.Fatalf("ids %v", got)
	}
}

func TestV4ModulesAreTheSoleAuthority(t *testing.T) {
	d := testDoc(time.Now().UTC())
	d.Modules = Modules{ModuleHospitality: {}}
	d.Features = ProjectFeatures(d.Modules)
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	// An independent feature claim is refused.
	d.Features.PaidWiFi = true
	if err := d.Validate(); err == nil {
		t.Fatal("v4 features that disagree with modules were accepted")
	}
	// v4 without modules is refused.
	d2 := testDoc(time.Now().UTC())
	d2.Modules = nil
	if err := d2.Validate(); err == nil {
		t.Fatal("v4 without modules accepted")
	}
}

func TestAuthorizedModulesV4DropsUnknownAndUnmet(t *testing.T) {
	d := &Document{SchemaVersion: 4, Modules: Modules{
		"future_vertical": {}, ModuleCardPayment: {}, ModuleSMSOTP: {},
	}}
	got := d.AuthorizedModules()
	if got["future_vertical"] || got[ModuleCardPayment] || !got[ModuleSMSOTP] {
		t.Fatalf("authorized %v", got)
	}
}

func TestLegacyMappingNeverDerivesFinancialModules(t *testing.T) {
	d := &Document{SchemaVersion: 3, Features: Features{PMS: true, PaidWiFi: true, SMSOTP: true, EmailOTP: true, SocialLogin: true, HA: true, WhiteLabel: true}}
	got := d.AuthorizedModules()
	for _, id := range []string{ModulePaidAccess, ModuleCardPayment, ModuleRoomCharge} {
		if got[id] {
			t.Fatalf("legacy licence derived %s", id)
		}
	}
	if !got[ModuleHospitality] || !got[ModuleSMSOTP] {
		t.Fatalf("legacy mapping lost %v", got)
	}
}

func TestUnknownModuleSurvivesSignedRoundTrip(t *testing.T) {
	// A newer Central may authorise a module this appliance does not know:
	// the signature still verifies (raw bytes) and the id is simply ignored.
	s := newSigner(t)
	v := NewVerifier(s.PublicKey())
	d := testDoc(time.Now().UTC())
	d.Modules["future_vertical"] = ModuleGrant{Limits: map[string]int{"x": 1}}
	env, err := s.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthorizedModules()["future_vertical"] {
		t.Fatal("unknown module authorised")
	}
	raw, _ := json.Marshal(got.Modules)
	if len(raw) == 0 {
		t.Fatal("modules lost")
	}
}

func TestCoreOnlyV4LicenceKeepsAnEmptyModulesMap(t *testing.T) {
	s := newSigner(t)
	v := NewVerifier(s.PublicKey())
	d := testDoc(time.Now().UTC())
	d.Modules = Modules{}
	d.Features = ProjectFeatures(d.Modules)
	env, err := s.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(env)
	if err != nil {
		t.Fatalf("core-only v4 licence must verify: %v", err)
	}
	if len(got.AuthorizedModules()) != 0 {
		t.Fatal("core-only licence authorised a module")
	}
}
