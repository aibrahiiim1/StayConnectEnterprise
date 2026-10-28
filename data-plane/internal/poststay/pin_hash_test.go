package poststay

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// D2-CC1: a PIN hash written by the previous in-package implementation still verifies, and new hashes keep its
// parameters (never weaker).
func TestPINHashesStayCompatible(t *testing.T) {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("ABCD2345"), salt, 1, 64*1024, 4, 32)
	legacy := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", 64*1024, 1, 4,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	if !verifyPIN("ABCD2345", legacy) {
		t.Fatal("a stored PIN hash no longer verifies")
	}
	if verifyPIN("ABCD2346", legacy) {
		t.Fatal("a wrong PIN verified")
	}
	h, err := hashPIN("ABCD2345")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=1,p=4$") || !verifyPIN("ABCD2345", h) {
		t.Fatalf("new hash parameters changed: %s", h)
	}
	if verifyPIN("x", "not-a-hash") {
		t.Fatal("a malformed hash verified")
	}
}
