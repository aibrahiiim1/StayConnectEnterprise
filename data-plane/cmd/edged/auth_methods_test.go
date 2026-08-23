package main

// Sign-in method configuration: validation, and the mode vocabulary the screen may offer.
//
// The merge itself is exercised against a real database in the integration suite; what is asserted here is
// the part that decides whether a save is accepted at all, and the part that decides which PMS verification
// values an operator can choose.

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

// Every mode offered on the screen must be accepted, and each corresponds to a field the resolver actually
// matches against. A mode that validated but never resolved would be a switch that silently does nothing.
func TestValidateAuthMethodsPatch_AcceptsEveryOfferedMode(t *testing.T) {
	for _, mode := range []string{"room_lastname", "room_firstname", "room_reservation"} {
		if err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":true,"mode":"`+mode+`"}}`)); err != nil {
			t.Fatalf("mode %q is offered by the UI but rejected by validation: %v", mode, err)
		}
		if !pmsSignInModes[mode] {
			t.Fatalf("mode %q is not in the offered set", mode)
		}
	}
}

// "either" is legacy: still accepted so an existing stored configuration can be saved back unchanged, but
// deliberately not in the offered set, because it guesses which field the guest typed.
func TestValidateAuthMethodsPatch_LegacyEitherAcceptedButNotOffered(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":true,"mode":"either"}}`)); err != nil {
		t.Fatalf("a site already storing \"either\" must still be able to save: %v", err)
	}
	if pmsSignInModes["either"] {
		t.Fatal("\"either\" must not be offered as a new choice")
	}
	// room_any was proposed and withdrawn: it is not an authorised mode and must not be selectable.
	if pmsSignInModes["room_any"] {
		t.Fatal("room_any is not an approved sign-in mode")
	}
}

// An unusable mode is refused at save time, so it cannot become a sign-in method that never works.
func TestValidateAuthMethodsPatch_RejectsUnknownMode(t *testing.T) {
	err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":true,"mode":"room_passport"}}`))
	if err == nil {
		t.Fatal("an unsupported verification value was accepted")
	}
	// The refusal names the alternatives; an operator cannot act on "invalid mode".
	for _, want := range []string{"room_lastname", "room_reservation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should name the supported modes, got %q", err.Error())
		}
	}
}

// Turning the method OFF must not require a valid mode: an operator disabling a misconfigured method would
// otherwise be blocked by the very misconfiguration they are removing.
func TestValidateAuthMethodsPatch_DisabledNeedsNoMode(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"pms":{"enabled":false}}`)); err != nil {
		t.Fatalf("disabling PMS must not require a mode: %v", err)
	}
}

// A patch that does not mention PMS is not a PMS change; validation must not invent a requirement for it.
func TestValidateAuthMethodsPatch_IgnoresUnrelatedMethods(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"voucher":{"enabled":true}}`)); err != nil {
		t.Fatalf("a voucher-only patch must validate: %v", err)
	}
	if err := validateAuthMethodsPatch(patch(t, `{"pms":"nonsense"}`)); err == nil {
		t.Fatal("a non-object pms value must be refused")
	}
}
