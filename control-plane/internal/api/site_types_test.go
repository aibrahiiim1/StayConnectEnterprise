package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	lic "github.com/stayconnect/enterprise/license"
)

func TestSiteTypeWritesAcceptOnlyKnownTypes(t *testing.T) {
	for in, want := range map[string]string{
		"":            SiteTypeUnspecified,
		"  hotel ":    "HOTEL",
		"BEACH_CLUB":  "BEACH_CLUB",
		"unspecified": SiteTypeUnspecified,
		"Other":       "OTHER",
	} {
		got, ok := normalizeSiteType(in)
		if !ok || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"RESTAURANT", "HOTEL;", "beach club", "X"} {
		if _, ok := normalizeSiteType(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	// Every accepted type fits the database CHECK shape (migration 0049).
	for _, st := range SiteTypes {
		if !regexpSiteTypeShape(st) {
			t.Errorf("%s does not match the 0049 CHECK shape", st)
		}
	}
}

func regexpSiteTypeShape(s string) bool {
	if len(s) < 2 || len(s) > 32 || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for _, c := range s[1:] {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// UNSPECIFIED is signed as empty, so an untyped site's assignment is byte-identical to the old layout.
func TestAssignmentCarriesSiteTypeOnlyWhenSet(t *testing.T) {
	if assignmentSiteType(SiteTypeUnspecified) != "" || assignmentSiteType("HOTEL") != "HOTEL" {
		t.Fatal("assignment site_type mapping wrong")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := &assignment.Document{AssignmentID: "x", ApplianceID: "a", TenantID: "t", SiteID: "s", Version: 2,
		State: assignment.StateAssigned, SiteType: assignmentSiteType(SiteTypeUnspecified)}
	assignment.Sign(priv, d)
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "site_type") {
		t.Fatalf("an untyped site leaked site_type into the document: %s", raw)
	}
	d.SiteType = assignmentSiteType("CAFE")
	assignment.Sign(priv, d)
	if !assignment.Verify(priv.Public().(ed25519.PublicKey), d) {
		t.Fatal("typed assignment does not verify")
	}
}

func TestModulesCatalogMirrorsTheRegistry(t *testing.T) {
	c := buildModulesCatalog()
	reg := lic.Registry()
	if len(c.Modules) != len(reg) {
		t.Fatalf("%d modules, registry has %d", len(c.Modules), len(reg))
	}
	for i, m := range reg {
		got := c.Modules[i]
		if got.ID != m.ID || got.Label != m.Label || got.Requires == nil || len(got.Requires) != len(m.Requires) {
			t.Errorf("module %d: %+v vs %+v", i, got, m)
		}
	}
	presets := map[string][]string{}
	for _, st := range c.SiteTypes {
		if st.Preset == nil {
			t.Errorf("%s preset is null; the console expects a list", st.ID)
		}
		presets[st.ID] = st.Preset
	}
	if len(c.SiteTypes) != len(SiteTypes) {
		t.Fatalf("site types %v", c.SiteTypes)
	}
	if !reflect.DeepEqual(presets["HOTEL"], []string{lic.ModuleHospitality}) {
		t.Errorf("HOTEL preset %v", presets["HOTEL"])
	}
	for _, st := range SiteTypes {
		if st != "HOTEL" && len(presets[st]) != 0 {
			t.Errorf("%s preset %v, want none", st, presets[st])
		}
	}
	// Every preset is itself a valid module set.
	for id, p := range presets {
		if _, err := lic.ModulesFromIDs(p); err != nil {
			t.Errorf("%s preset invalid: %v", id, err)
		}
	}
}

func TestLicenseTermsModules(t *testing.T) {
	p, err := licenseTerms{Modules: []string{"card_payment", "paid_access", "paid_access"}}.params("a", "op")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Modules, []string{"card_payment", "paid_access"}) {
		t.Fatalf("modules %v", p.Modules)
	}
	if p, err := (licenseTerms{}).params("a", "op"); err != nil || p.Modules == nil || len(p.Modules) != 0 {
		t.Fatalf("omitted modules must be core only: %v %v", p.Modules, err)
	}
	for _, bad := range [][]string{{"nope"}, {"card_payment"}, {"room_charge", "hospitality"}} {
		_, err := licenseTerms{Modules: bad}.params("a", "op")
		if err == nil || !strings.HasPrefix(err.Error(), "modules: ") {
			t.Errorf("%v: err %v", bad, err)
		}
	}
}
