package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func patch(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("bad test fixture: %v", err)
	}
	return m
}

func TestValidateAuthMethodsPatch_AcceptsEveryOfferedMode(t *testing.T) {
	for _, mode := range []string{"room_lastname", "room_firstname", "room_reservation", "room_any"} {
		if err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":true,"mode":"`+mode+`"}}`)); err != nil {
			t.Fatalf("mode %q is offered by the UI but rejected by validation: %v", mode, err)
		}
		if !pmsSignInModes[mode] {
			t.Fatalf("mode %q is not in the offered set", mode)
		}
	}
}

// "either" is accepted so a stored configuration can be saved back, but not offered: it guesses which field the guest typed.
func TestValidateAuthMethodsPatch_LegacyEitherAcceptedButNotOffered(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":true,"mode":"either"}}`)); err != nil {
		t.Fatalf("a site already storing \"either\" must still be able to save: %v", err)
	}
	if pmsSignInModes["either"] {
		t.Fatal("\"either\" must not be offered as a new choice")
	}
}

// room_any is not a second spelling of "either": only "either" makes the browser guess which field was typed.
func TestValidateAuthMethodsPatch_CombinedModeIsOfferedAndEitherIsNot(t *testing.T) {
	if !pmsSignInModes["room_any"] {
		t.Fatal("room_any is the approved combined sign-in mode and must be selectable")
	}
	if pmsSignInModes["either"] {
		t.Fatal("\"either\" guesses in the browser and must never be offered alongside room_any")
	}
}

func TestValidateAuthMethodsPatch_RejectsUnknownMode(t *testing.T) {
	err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":true,"mode":"room_passport"}}`))
	if err == nil {
		t.Fatal("an unsupported verification value was accepted")
	}
	for _, want := range []string{"room_lastname", "room_reservation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should name the supported modes, got %q", err.Error())
		}
	}
}

// Disabling a misconfigured method must not be blocked by that misconfiguration.
func TestValidateAuthMethodsPatch_DisabledNeedsNoMode(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":false}}`)); err != nil {
		t.Fatalf("disabling PMS must not require a mode: %v", err)
	}
}

func TestValidateAuthMethodsPatch_IgnoresUnrelatedMethods(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"voucher":{"enabled":true}}`)); err != nil {
		t.Fatalf("a voucher-only patch must validate: %v", err)
	}
	if err := validateAuthMethodsPatch(patch(t, `{"pms":"nonsense"}`)); err == nil {
		t.Fatal("a non-object pms value must be refused")
	}
}
