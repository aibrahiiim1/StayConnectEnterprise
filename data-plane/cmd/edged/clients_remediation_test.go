package main

// Unit tests for the Clients-area audit remediation (2026-10-16): reason bounds in characters (C-17), clearable
// PATCH fields (C-9), the auth-methods shape check (C-20), the identity-provider Offered rule (C-14) and the
// provider screens' module gate (C-15).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReasonBounds_CountCharactersNotBytes(t *testing.T) {
	// Four Arabic characters are eight bytes: they are a four-character reason.
	if !validReason("سبب ا") || !validReason("شكرا") {
		t.Fatal("a four-character Arabic reason was refused")
	}
	// 300 Arabic characters are 600 bytes and within the 500-character bound.
	if !validReason(strings.Repeat("ع", 300)) {
		t.Fatal("a 300-character Arabic reason was refused as too long")
	}
	if validReason(strings.Repeat("ع", reasonMaxChars+1)) {
		t.Fatal("a reason over the bound was accepted")
	}
	// Surrounding spaces do not count.
	if validReason("   ab   ") {
		t.Fatal("padding made a two-character reason pass")
	}
	if got, ok := reasonText("  shown at the desk  "); !ok || got != "shown at the desk" {
		t.Fatalf("reasonText = %q %v", got, ok)
	}
	if reasonMinChars != 4 || reasonMaxChars != 500 {
		t.Fatal("the bounds changed: hotel-admin/lib/reason.ts mirrors them and must change with them")
	}
}

func TestOptional_DistinguishesAbsentNullAndValue(t *testing.T) {
	var in struct {
		A optional[string]    `json:"a"`
		B optional[string]    `json:"b"`
		C optional[time.Time] `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":null,"c":"2026-10-16T10:00:00Z"}`), &in); err != nil {
		t.Fatal(err)
	}
	if !in.A.cleared() {
		t.Fatal("an explicit null did not read as a clear")
	}
	if in.B.Set {
		t.Fatal("an absent key read as set")
	}
	stored := "kept"
	if got := in.B.or(&stored); got == nil || *got != "kept" {
		t.Fatal("an absent key did not keep the stored value")
	}
	if got := in.A.or(&stored); got != nil {
		t.Fatal("a cleared key kept the stored value")
	}
	if in.C.Value == nil || in.C.Value.Year() != 2026 {
		t.Fatalf("a value was not decoded: %+v", in.C)
	}
}

func TestAccountValidityError(t *testing.T) {
	a := time.Now()
	b := a.Add(time.Hour)
	if accountValidityError(&a, &b) != "" || accountValidityError(nil, &b) != "" || accountValidityError(&a, nil) != "" {
		t.Fatal("a valid window was refused")
	}
	if accountValidityError(&b, &a) == "" || accountValidityError(&a, &a) == "" {
		t.Fatal("a window that ends before (or when) it starts was accepted")
	}
}

func TestValidateAuthMethodsPatch_RefusesUnknownKeysAndShapes(t *testing.T) {
	for _, bad := range []string{
		`{"voucher":"yes"}`,
		`{"anything":{"enabled":true}}`,
		`{"voucher":{"enabled":"maybe"}}`,
		`{"sms":{"enabled":true,"provider":"twilio"}}`,
		`{"social":{"myspace":{"enabled":true}}}`,
		`{"social":{"google":{"enabled":1}}}`,
		`{"portal":{"remember_device_days":"30"}}`,
		`{"portal":{"theme":"dark"}}`,
		`{"pms":{"enabled":true,"mode":"room_lastname","colour":"red"}}`,
		`{}`,
	} {
		if err := validateAuthMethodsPatch(patch(t, bad)); err == nil {
			t.Fatalf("%s was accepted", bad)
		}
	}
	for _, good := range []string{
		`{"voucher":{"enabled":true}}`,
		`{"guest_account":{"enabled":false,"template_id":""}}`,
		`{"open":{"enabled":true}}`,
		`{"email_account":{"enabled":true}}`,
		`{"whatsapp":{"enabled":false}}`,
		`{"social":{"google":{"enabled":true},"microsoft":{"enabled":false}}}`,
		`{"pms":{"enabled":true,"mode":"room_any","provider":"protel-fias","max_failures_per_room":5}}`,
		`{"portal":{"primary_method":"voucher","remember_device_days":30}}`,
	} {
		if err := validateAuthMethodsPatch(patch(t, good)); err != nil {
			t.Fatalf("%s was refused: %v", good, err)
		}
	}
}

func TestApplySocialSwitch_OfferedHonoursTheSignInMethodSwitch(t *testing.T) {
	p := edgeSocialProvider{Provider: "google", Offered: true}
	applySocialSwitch(&p, map[string]bool{"google": false}, true)
	if p.Offered || p.OfferedReason != "method_off" {
		t.Fatalf("a provider whose switch is off reads as offered: %+v", p)
	}
	p = edgeSocialProvider{Provider: "google", Offered: true}
	applySocialSwitch(&p, map[string]bool{"google": true}, true)
	if !p.Offered {
		t.Fatal("a switched-on, ready provider was not offered")
	}
	p = edgeSocialProvider{Provider: "google", Offered: true}
	applySocialSwitch(&p, nil, false)
	if p.Offered || p.OfferedReason != "method_unknown" {
		t.Fatalf("an unreadable switch must fail closed: %+v", p)
	}
	// A reason the application itself already gave is kept.
	p = edgeSocialProvider{Provider: "google", Offered: false, OfferedReason: "http_fallback"}
	applySocialSwitch(&p, map[string]bool{}, true)
	if p.OfferedReason != "http_fallback" {
		t.Fatalf("the earlier reason was overwritten: %+v", p)
	}
}

func TestModuleGate_ProviderScreensFollowTheirMethodsModules(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{
		"social_login": {ID: "social_login", Licensed: false, Deployed: true},
		"email_otp":    {ID: "email_otp", Licensed: false, Deployed: true},
		"sms_otp":      {ID: "sms_otp", Licensed: true, Deployed: true},
		"whatsapp_otp": {ID: "whatsapp_otp", Licensed: false, Deployed: true},
	}))}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for name, want := range map[string]int{
		"social-providers":       404, // social login not licensed
		"notification-providers": 204, // SMS is
	} {
		rec := httptest.NewRecorder()
		s.moduleGate(name)(ok).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != want {
			t.Fatalf("%s: got %d want %d", name, rec.Code, want)
		}
	}
	none := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{}))}
	rec := httptest.NewRecorder()
	none.moduleGate("notification-providers")(ok).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 404 {
		t.Fatalf("delivery providers on a site with no code or email module: %d", rec.Code)
	}
}
