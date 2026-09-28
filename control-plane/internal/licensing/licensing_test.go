package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	lic "github.com/stayconnect/enterprise/license"
)

// F8: a WAN-MAC rebind (and suspend/resume) re-signs the licence with the SAME terms. It used to re-issue
// with cap 0 (unlimited) and a fresh 365-day term.
func TestPreservedTermsKeepEveryTerm(t *testing.T) {
	until := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cur := IssueParams{
		TenantID: "t", SiteID: "s", ApplianceID: "a", CreatedBy: "original",
		MaxConcurrentOnlineGuests: 250, ValidFrom: from, ValidUntil: until,
		GracePeriodDays: 14, OfflineGraceDays: 21, Status: lic.DocSuspended,
		ValidFor: 365 * 24 * time.Hour,
	}
	got := PreservedTerms(cur, "operator-2")
	if got.MaxConcurrentOnlineGuests != 250 {
		t.Errorf("cap changed: %d", got.MaxConcurrentOnlineGuests)
	}
	if !got.ValidUntil.Equal(until) || !got.ValidFrom.Equal(from) {
		t.Errorf("validity changed: %v..%v", got.ValidFrom, got.ValidUntil)
	}
	if got.GracePeriodDays != 14 || got.OfflineGraceDays != 21 {
		t.Errorf("grace changed: %d/%d", got.GracePeriodDays, got.OfflineGraceDays)
	}
	if got.Status != lic.DocSuspended {
		t.Errorf("status changed: %s", got.Status)
	}
	if got.ValidFor != 0 {
		t.Errorf("valid_for must not survive: a re-issue would otherwise re-derive valid_until from now")
	}
	if got.CreatedBy != "operator-2" || got.ApplianceID != "a" || got.TenantID != "t" || got.SiteID != "s" {
		t.Errorf("identity/actor wrong: %+v", got)
	}
	if cur.CreatedBy != "original" {
		t.Errorf("input mutated")
	}
}

// Modules survive every preserved-terms re-issue (suspend, resume, rebind, move), and the copy is independent.
func TestPreservedTermsKeepModules(t *testing.T) {
	cur := IssueParams{ApplianceID: "a", Modules: []string{"hospitality", "paid_access"}}
	got := PreservedTerms(cur, "op")
	if !reflect.DeepEqual(got.Modules, []string{"hospitality", "paid_access"}) {
		t.Fatalf("modules lost on re-issue: %v", got.Modules)
	}
	got.Modules[0] = "changed"
	if cur.Modules[0] != "hospitality" {
		t.Fatal("re-issue terms alias the stored modules")
	}
	// A legacy licence stored with no modules re-issues as core only: nothing is invented.
	if legacy := PreservedTerms(IssueParams{ApplianceID: "a"}, "op"); len(legacy.Modules) != 0 {
		t.Fatalf("legacy re-issue invented modules: %v", legacy.Modules)
	}
}

func TestModuleTermsProjectFeatures(t *testing.T) {
	m, f, ids, err := ModuleTerms([]string{"room_charge", "paid_access", "hospitality", "sms_otp"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"hospitality", "paid_access", "room_charge", "sms_otp"}) {
		t.Fatalf("ids %v", ids)
	}
	if len(m) != 4 {
		t.Fatalf("modules %v", m)
	}
	want := lic.Features{PMS: true, PaidWiFi: true, SMSOTP: true}
	if f != want {
		t.Fatalf("features %+v, want the modules projection %+v", f, want)
	}
	// Core only: an explicit empty map (a nil map would not verify as v4) and all-false features.
	m, f, ids, err = ModuleTerms(nil)
	if err != nil || m == nil || len(m) != 0 || f != (lic.Features{}) || ids == nil || len(ids) != 0 {
		t.Fatalf("core only: %v %+v %v %v", m, f, ids, err)
	}
}

func TestModuleTermsRejectUnknownAndUnmet(t *testing.T) {
	for _, c := range []struct {
		ids  []string
		want string
	}{
		{[]string{"no_such_module"}, "unknown module"},
		{[]string{"card_payment"}, "requires"},
		{[]string{"room_charge", "paid_access"}, "requires"},
	} {
		if _, _, _, err := ModuleTerms(c.ids); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err %v, want %q", c.ids, err, c.want)
		}
	}
}

// The document IssueTx signs verifies on the appliance side for both a moduled and a core-only licence.
func TestModuleTermsDocumentsVerify(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := lic.NewSigner(priv)
	verifier := lic.NewVerifier(priv.Public().(ed25519.PublicKey))
	for _, ids := range [][]string{nil, {"hospitality", "paid_access", "card_payment"}} {
		m, f, _, err := ModuleTerms(ids)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		doc := &lic.Document{LicenseID: "l", TenantID: "t", SiteID: "s", ApplianceIDs: []string{"a"},
			CommercialPlanCode: PlanCode, Status: lic.DocActive, IssuedAt: now, ValidUntil: now.Add(time.Hour),
			OfflineGraceDays: 30, Features: f, Modules: m, SchemaVersion: lic.CurrentSchemaVersion}
		env, err := signer.Sign(doc)
		if err != nil {
			t.Fatalf("%v: sign: %v", ids, err)
		}
		got, err := verifier.Verify(env)
		if err != nil {
			t.Fatalf("%v: verify: %v", ids, err)
		}
		if len(got.AuthorizedModules()) != len(ids) {
			t.Fatalf("%v: authorised %v", ids, got.AuthorizedModules())
		}
	}
}
