package main

import "testing"

func TestValidOperatorAccountName(t *testing.T) {
	for name, want := range map[string]bool{
		"ops@example.com": true,
		"frontdesk1":      true,
		"night.manager":   true,
		"it-admin_2":      true,
		"abc":             true,
		"ab":              false,
		"":                false,
		"-lead":           false,
		"front desk":      false,
		"admin@":          false,
		"@example.com":    false,
		"a@b@c":           false,
		"user;drop":       false,
		"ünicode":         false,
		"a234567890123456789012345678901234567890123456789012345678901234":  true,
		"a2345678901234567890123456789012345678901234567890123456789012345": false,
	} {
		if got := validOperatorAccountName(name); got != want {
			t.Errorf("validOperatorAccountName(%q) = %v, want %v", name, got, want)
		}
	}
}
