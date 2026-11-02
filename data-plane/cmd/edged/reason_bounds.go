package main

// One bound for every mandatory staff reason, counted in characters (runes) of the trimmed text, not bytes
// (Arabic is two bytes a character). The database checks count characters too (length(btrim(..))), and the
// Admin Console mirrors these numbers (hotel-admin/lib/reason.ts); reason_bounds_test.go pins them.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	reasonMinChars = 4
	reasonMaxChars = 500
)

var reasonBoundsMessage = fmt.Sprintf("a bounded reason (%d-%d characters) is required", reasonMinChars, reasonMaxChars)

func reasonText(reason string) (string, bool) {
	t := strings.TrimSpace(reason)
	n := utf8.RuneCountInString(t)
	return t, n >= reasonMinChars && n <= reasonMaxChars
}

// validReason bounds the mandatory justification: empty is refused (an unexplained action looks unauthorized afterwards).
func validReason(reason string) bool {
	_, ok := reasonText(reason)
	return ok
}

// defaultSettingReason is what the audit trail records when an operator confirms an ordinary settings change without
// typing a justification: the confirm dialog is the consent. Actions that need a real reason keep validReason.
const defaultSettingReason = "Confirmed in the console"

func settingReason(reason string) string {
	if t := strings.TrimSpace(reason); t != "" {
		return t
	}
	return defaultSettingReason
}
