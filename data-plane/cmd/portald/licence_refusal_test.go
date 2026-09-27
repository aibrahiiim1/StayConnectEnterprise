package main

import (
	"strings"
	"testing"
)

// A ROOM GUEST, A VOUCHER GUEST AND AN ACCOUNT GUEST READ ONE SENTENCE FOR ONE LICENCE CONDITION.
//
// scd refuses every method through the same licence decision; these assert that portald turns that decision
// into the same words whichever method met it, that the room sign-in's closed set carries them (so the
// disclosure test still holds), and that the translated page knows them.
func TestLicenceRefusalsReadTheSameForEveryMethod(t *testing.T) {
	if got := messageForClass("CAPACITY", 0); got != guestCapacityMessage {
		t.Errorf("room sign-in at capacity reads %q", got)
	}
	if got := messageForClass("LICENSE", 0); got != guestLicenseRefusedMessage {
		t.Errorf("room sign-in refused by the licence reads %q", got)
	}
	for _, class := range []string{"CAPACITY", "LICENSE"} {
		_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "licence", class, "", "", 0)
		if leaksDetail(body) {
			t.Errorf("the %s refusal is not in the room sign-in's closed set: %q", class, body.Message)
		}
		if term := bodyMentionsForbiddenTerm(body); term != "" {
			t.Errorf("the %s refusal mentions %q", class, term)
		}
	}
	for _, code := range []string{"unlicensed", "license_expired", "feature_not_licensed", "tenant_transition_pending", "removed_from_central"} {
		if !isLicenceRefusal(code) {
			t.Errorf("%s is a licence refusal", code)
		}
	}
	if isLicenceRefusal("AUTH_DENIED") || isLicenceRefusal("MAX_DEVICES_REACHED") {
		t.Error("a credential or device-limit refusal is not a licence refusal")
	}
	// The English of each translation key IS the server's sentence, so the page can translate it.
	en := builtinStrings["en"]
	if en["err.capacity"] != guestCapacityMessage || en["err.method.disabled"] != guestLicenseRefusedMessage {
		t.Error("the capacity / licence sentences and their translation keys disagree")
	}
	for _, key := range []string{"err.capacity", "err.method.disabled", "err.room.devices"} {
		if !strings.Contains(landingHTML, "ROOM_MESSAGES[BUILTIN.en['"+key+"']] = '"+key+"'") {
			t.Errorf("the room sign-in page does not translate %s", key)
		}
	}
}
