package main

import (
	"strings"
	"testing"
)

// Audit P7 L8: a key setting is decoded and checked at start-up; the error names no value.
func TestDecodeKeyEnv(t *testing.T) {
	good := strings.Repeat("ab", 32)
	cases := []struct {
		name, val string
		wantLen   int
		wantErr   bool
	}{
		{"unset", "", 0, false},
		{"valid 32 bytes", good, 32, false},
		{"valid with spaces", "  " + good + "\n", 32, false},
		{"longer", good + "cd", 33, false},
		{"not hex", strings.Repeat("zz", 32), 0, true},
		{"short", strings.Repeat("ab", 16), 0, true},
	}
	for _, c := range cases {
		b, err := decodeKeyEnv(func(string) string { return c.val }, "PMSD_EVIDENCE_KEY_HEX")
		if (err != nil) != c.wantErr || len(b) != c.wantLen {
			t.Errorf("%s: len=%d err=%v, want len=%d err=%v", c.name, len(b), err, c.wantLen, c.wantErr)
		}
		if err != nil && c.val != "" && strings.Contains(err.Error(), strings.TrimSpace(c.val)) {
			t.Errorf("%s: error carries the value", c.name)
		}
	}
}
