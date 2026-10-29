package applianceauth

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

// Golden vector, byte-identical in the other module's applianceauth package (AD-1, AD-61). If one side changes the
// derivation, both tests must change together or activation codes stop matching.
func TestActivationCodeGoldenVector(t *testing.T) {
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for i := range pub {
		pub[i] = byte(i)
	}
	got := ActivationCode(pub)
	if got != "HCLA-YZKT" {
		t.Fatalf("activation code = %s", got)
	}
	for _, typed := range []string{got, " " + got + " ", strings.ToLower(got), NormalizeActivationCode(got)} {
		if !ActivationCodeMatches(pub, typed) {
			t.Fatalf("%q should match", typed)
		}
	}
	for _, typed := range []string{"", "AAAA-AAAA", got[:4], got + "X"} {
		if ActivationCodeMatches(pub, typed) {
			t.Fatalf("%q must not match", typed)
		}
	}
}
