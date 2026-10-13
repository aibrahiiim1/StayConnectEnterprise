package social

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"testing"
)

// RFC 7636 Appendix B: the worked S256 example.
func TestPKCEChallengeIsBase64urlOfSHA256OfTheVerifier(t *testing.T) {
	if got := PKCEChallengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %q", got)
	}
	v, _, err := NewAttemptSecrets(Binding{PKCE: true})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(v))
	if PKCEChallengeS256(v) != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("challenge is not BASE64URL(SHA256(verifier))")
	}
}

var unreserved43 = regexp.MustCompile(`^[A-Za-z0-9\-._~]{43}$`)

func TestAttemptSecretsAreFreshAndOnlyWhatTheProviderBinds(t *testing.T) {
	v1, n1, err := NewAttemptSecrets(Binding{PKCE: true, Nonce: true})
	if err != nil {
		t.Fatal(err)
	}
	v2, n2, _ := NewAttemptSecrets(Binding{PKCE: true, Nonce: true})
	if !unreserved43.MatchString(v1) || !unreserved43.MatchString(n1) {
		t.Fatalf("verifier/nonce must be 43 unreserved characters: %q %q", v1, n1)
	}
	if v1 == v2 || n1 == n2 || v1 == n1 {
		t.Fatal("attempt secrets repeat")
	}
	if v, n, _ := NewAttemptSecrets(Binding{Nonce: true}); v != "" || n == "" {
		t.Errorf("nonce-only binding: v=%q n=%q", v, n)
	}
	if v, n, _ := NewAttemptSecrets(Binding{}); v != "" || n != "" {
		t.Errorf("no binding must generate nothing: v=%q n=%q", v, n)
	}
}

func TestNonceMismatchIsRefused(t *testing.T) {
	c := &idClaims{raw: map[string]any{"nonce": "abc"}}
	if err := checkNonce(c, "abc"); err != nil {
		t.Fatalf("matching nonce refused: %v", err)
	}
	for _, want := range []string{"abd", "ab", "abcd", ""} {
		if err := checkNonce(c, want); !errors.Is(err, ErrAttemptBinding) {
			t.Errorf("nonce %q accepted for a token carrying abc", want)
		}
	}
	if err := checkNonce(&idClaims{raw: map[string]any{}}, "abc"); !errors.Is(err, ErrAttemptBinding) {
		t.Error("a token without a nonce was accepted")
	}
}
