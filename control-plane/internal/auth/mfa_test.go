package auth

import "testing"

// A recovery code is accepted however it was typed or pasted: lower case, a dash, and spaces, tabs or line
// breaks anywhere in it, not only at the ends. The result is the code as generated (recoveryAlphabet, upper
// case, no separator).
func TestRecoveryCodeNormalisation(t *testing.T) {
	for _, in := range []string{
		"ABCDE-FGHJK", "abcde-fghjk", "ABCDEFGHJK", " ABCDE FGHJK ", "ABCDE\tFGHJK", "ABCDE-\nFGHJK",
		"AB CDE-FG HJK", "a-b-c-d-e-f-g-h-j-k", " ABCDE FGHJK\r\n",
	} {
		if got := normalizeRecoveryCode(in); got != "ABCDEFGHJK" {
			t.Errorf("normalizeRecoveryCode(%q) = %q", in, got)
		}
	}
	m, err := NewMFA(nil, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if m.recoveryMAC("op", "abcde fghjk") != m.recoveryMAC("op", "ABCDE-FGHJK") {
		t.Fatal("the same code typed two ways has two MACs")
	}
	if m.recoveryMAC("op", "ABCDE-FGHJK") == m.recoveryMAC("op", "ABCDE-FGHJM") {
		t.Fatal("different codes share a MAC")
	}
}
