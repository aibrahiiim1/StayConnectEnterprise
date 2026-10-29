package applianceauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"strings"
)

// ActivationCode is the short code an appliance shows on its own Admin Console and an operator types into Central's
// Activate dialog (AD-1). It is derived from the identity PUBLIC key, so Central recomputes it from the key it holds
// and an attacker who registered a different key under a known serial cannot show the code of the real box.
//
// Layout (identical in control-plane and data-plane; golden vector in activationcode_test.go):
//
//	sum  = SHA-256("onegate-activation-code-v1" || 0x00 || raw 32-byte Ed25519 public key)
//	code = first 8 characters of RFC 4648 base32 (A-Z, 2-7, no padding) of sum, written XXXX-XXXX
func ActivationCode(pub ed25519.PublicKey) string {
	h := sha256.New()
	h.Write([]byte("onegate-activation-code-v1"))
	h.Write([]byte{0})
	h.Write(pub)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil))
	return enc[:4] + "-" + enc[4:8]
}

// NormalizeActivationCode upper-cases an operator-typed code and drops separators and spaces.
func NormalizeActivationCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "", "_", "").Replace(s)
}

// ActivationCodeMatches compares a typed code with the code of pub in constant time. An empty code never matches.
func ActivationCodeMatches(pub ed25519.PublicKey, typed string) bool {
	got := NormalizeActivationCode(typed)
	if len(got) != 8 || len(pub) != ed25519.PublicKeySize {
		return false
	}
	want := NormalizeActivationCode(ActivationCode(pub))
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
