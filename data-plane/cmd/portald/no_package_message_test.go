package main

import "testing"

// The two answers after a verified stay are the page's own translated sentences, so every language says exactly what English does.
func TestAVerifiedStayWithoutAPackageGetsItsOwnSentence(t *testing.T) {
	for class, key := range map[string]string{"NO_PACKAGE": "err.room.nopackage", "ROOM_CHARGE_PAUSED": "err.room.paused"} {
		got := messageForClass(class, 0)
		if got != pageStrings["en"][key] {
			t.Errorf("%s answers %q, want the page's %s %q", class, got, key, pageStrings["en"][key])
		}
		if got == guestAuthTechnicalMessage || got == guestAuthMessage {
			t.Errorf("%s still answers the technical or credential sentence", class)
		}
		for lang, strs := range pageStrings {
			if strs[key] == "" {
				t.Errorf("%s has no %s translation", lang, key)
			}
		}
	}
}
