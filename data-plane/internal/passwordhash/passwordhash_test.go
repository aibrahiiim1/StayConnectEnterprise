package passwordhash

import (
	"errors"
	"strings"
	"testing"
)

func TestHashVerifiesAndIsSalted(t *testing.T) {
	h1, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := Hash("correct horse")
	if h1 == h2 {
		t.Fatal("two hashes of one password must differ (fresh salt)")
	}
	if !strings.HasPrefix(h1, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Fatalf("issuing format changed: %s", h1)
	}
	if ok, err := Verify("correct horse", h1); err != nil || !ok {
		t.Fatalf("own hash must verify: %v %v", ok, err)
	}
	if ok, _ := Verify("Correct horse", h1); ok {
		t.Fatal("a different password must not verify")
	}
}

// The verifier refuses to run argon2 on a hostile hash rather than allocate what it asks for.
func TestVerifyBoundsEveryParameter(t *testing.T) {
	for _, bad := range []string{
		"", "plain", "$argon2i$v=19$m=65536,t=1,p=4$MDEyMzQ1Njc4OWFiY2RlZg$AAAA",
		"$argon2id$v=18$m=65536,t=1,p=4$MDEyMzQ1Njc4OWFiY2RlZg$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=4194304,t=1,p=4$MDEyMzQ1Njc4OWFiY2RlZg$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=99,p=4$MDEyMzQ1Njc4OWFiY2RlZg$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=1,p=300$MDEyMzQ1Njc4OWFiY2RlZg$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=1,p=4$AA$" + strings.Repeat("A", 43),
	} {
		if ok, err := Verify("x", bad); ok || !errors.Is(err, ErrFormat) {
			t.Errorf("%q: ok=%v err=%v, want ErrFormat", bad, ok, err)
		}
	}
	if ok, err := Verify("x", Dummy()); err != nil || !ok {
		t.Fatalf("the dummy hash is a real hash of x: %v %v", ok, err)
	}
}

func TestSelfServicePolicy(t *testing.T) {
	for pw, want := range map[string]bool{
		"":                             false,
		"short7c":                      false, // 7
		"eight8ch":                     true,  // 8
		strings.Repeat("ab", 64):       true,  // 128
		strings.Repeat("ab", 64) + "c": false, // 129
		"aaaaaaaaaaaa":                 false, // all one character
		"ann@example.com":              false, // the address itself
		" ANN@Example.com ":            false, // ... in any case, trimmed
		"ann@example.com1":             true,
		"كلمةسرطويلة":                  true, // counted in characters, not bytes
		"\u0000abcdefgh":               false,
		"pass\nword123":                false,
		string([]byte{0xff, 0xfe, 'a', 'b', 'c', 'd', 'e', 'f'}): false,
	} {
		err := CheckSelfService(pw, "ann@example.com")
		if (err == nil) != want {
			t.Errorf("%q: err=%v, want ok=%v", pw, err, want)
		}
	}
}
