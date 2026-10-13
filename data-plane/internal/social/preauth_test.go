package social

import "testing"

func TestEveryProviderDeclaresItsConsentPageHostsAndNothingElseDoes(t *testing.T) {
	for _, p := range []string{"google", "microsoft", "apple", "facebook"} {
		ds := PreAuthDomains(p)
		if len(ds) == 0 {
			t.Errorf("%s declares no pre-auth host", p)
		}
		for _, d := range ds {
			if d == "" || d[0] == '*' || d != trimLower(d) {
				t.Errorf("%s: %q must be a plain lower-case host name", p, d)
			}
		}
	}
	if PreAuthDomains("stub") != nil || PreAuthDomains("") != nil {
		t.Fatal("an unknown provider opens nothing")
	}
}

func trimLower(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == ' ' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
