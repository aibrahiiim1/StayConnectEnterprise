package mailreach

import (
	"strings"
	"testing"
)

// THE PRESETS ARE HOST NAMES A MAIL APP CONTACTS, and a site's own entries are held to the same shape.
func TestPresetsAreValidHostNamesAndExpandDeduplicated(t *testing.T) {
	ids := map[string]bool{}
	for _, p := range Providers {
		if p.ID == "" || p.Name == "" || len(p.Domains) == 0 || ids[p.ID] {
			t.Fatalf("preset %+v is incomplete or duplicated", p)
		}
		ids[p.ID] = true
		for _, d := range p.Domains {
			if !ValidHost(d) || strings.ContainsAny(d, "*/: ") {
				t.Fatalf("preset %s carries a non-host entry %q", p.ID, d)
			}
		}
	}
	got := Domains([]string{"gmail", "GMAIL ", "nosuch"}, []string{"Mail.Example.com", "mail.example.com", "*.bad", "", "x"})
	seen := map[string]int{}
	for _, d := range got {
		seen[d]++
		if seen[d] > 1 {
			t.Fatalf("duplicate %q", d)
		}
	}
	if seen["mail.google.com"] == 0 || seen["mail.example.com"] == 0 || seen["*.bad"] > 0 || seen["x"] > 0 {
		t.Fatalf("expansion wrong: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatal("not sorted")
		}
	}
}

func TestValidateRefusesUnknownPresetsAndNonHosts(t *testing.T) {
	for _, bad := range []string{"*.example.com", "example.com/path", "http://example.com", "10.0.0.1", "com", "mail..example.com", "-a.example.com", "a.example.com:443"} {
		if _, ok := Validate(nil, []string{bad}); ok {
			t.Errorf("%q must be refused", bad)
		}
	}
	if _, ok := Validate([]string{"hotmail"}, nil); ok {
		t.Error("an unknown preset id must be refused")
	}
	if _, ok := Validate([]string{"outlook", "yahoo"}, []string{"mail.example.com", "imap.example.co.uk"}); !ok {
		t.Error("known presets and host names are accepted")
	}
	many := make([]string, MaxCustomDomains+1)
	for i := range many {
		many[i] = "h" + strings.Repeat("x", i%3) + ".example.com"
	}
	if bad, ok := Validate(nil, many); ok || bad != "too many" {
		t.Error("the custom list is bounded")
	}
}
