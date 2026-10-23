package otpkey

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func key(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func chal() Challenge {
	return Challenge{TenantID: "t1", Channel: "email", Destination: "a@example.com", ChallengeID: "cid-1"}
}

func TestNewRingValidation(t *testing.T) {
	if _, err := NewRing(nil, 1); err == nil {
		t.Fatal("empty ring should error")
	}
	if _, err := NewRing(map[int][]byte{1: []byte("short")}, 1); err == nil {
		t.Fatal("short key should error")
	}
	if _, err := NewRing(map[int][]byte{1: key(1)}, 2); err == nil {
		t.Fatal("active must exist")
	}
}

func TestIssueVerifyCorrectAndWrong(t *testing.T) {
	r, _ := NewRing(map[int][]byte{1: key(1)}, 1)
	gen, dig, err := r.Issue(chal(), "123456")
	if err != nil || gen != 1 {
		t.Fatalf("issue: gen=%d err=%v", gen, err)
	}
	if ok, _ := r.Verify(gen, dig, chal(), "123456"); !ok {
		t.Fatal("correct code must verify")
	}
	if ok, _ := r.Verify(gen, dig, chal(), "000000"); ok {
		t.Fatal("wrong code must not verify")
	}
}

func TestCrossContextRejected(t *testing.T) {
	r, _ := NewRing(map[int][]byte{1: key(1)}, 1)
	_, dig, _ := r.Issue(chal(), "123456")
	mods := []Challenge{
		{TenantID: "OTHER", Channel: "email", Destination: "a@example.com", ChallengeID: "cid-1"},
		{TenantID: "t1", Channel: "sms", Destination: "a@example.com", ChallengeID: "cid-1"},
		{TenantID: "t1", Channel: "email", Destination: "b@example.com", ChallengeID: "cid-1"},
		{TenantID: "t1", Channel: "email", Destination: "a@example.com", ChallengeID: "cid-2"},
	}
	for i, c := range mods {
		if ok, _ := r.Verify(1, dig, c, "123456"); ok {
			t.Fatalf("mod %d must be rejected", i)
		}
	}
}

func TestRotationKeepsOldVerifiable(t *testing.T) {
	r1, _ := NewRing(map[int][]byte{1: key(1)}, 1)
	gen1, dig1, _ := r1.Issue(chal(), "123456")
	r2, _ := NewRing(map[int][]byte{1: key(1), 2: key(2)}, 2)
	if r2.Active() != 2 {
		t.Fatal("active should be 2 after rotation")
	}
	if ok, _ := r2.Verify(gen1, dig1, chal(), "123456"); !ok {
		t.Fatal("rotation must keep old-generation OTPs verifiable")
	}
	genN, digN, _ := r2.Issue(chal(), "654321")
	if genN != 2 {
		t.Fatal("new issuance must use active gen 2")
	}
	_, dig1same, _ := r1.Issue(chal(), "654321")
	if digN == dig1same {
		t.Fatal("different generations must produce different digests")
	}
	r3, _ := NewRing(map[int][]byte{2: key(2)}, 2)
	if ok, err := r3.Verify(1, dig1, chal(), "123456"); ok || err == nil {
		t.Fatal("removed generation must fail closed")
	}
}

func TestLegacyCompat(t *testing.T) {
	salt := "deadbeef"
	sum := sha256.Sum256([]byte(salt + "|" + "123456"))
	stored := salt + ":" + hex.EncodeToString(sum[:])
	if !IsLegacyFormat(stored) {
		t.Fatal("should detect legacy format")
	}
	if ok, _ := VerifyLegacy(stored, "123456"); !ok {
		t.Fatal("legacy correct code must verify")
	}
	if ok, _ := VerifyLegacy(stored, "000000"); ok {
		t.Fatal("legacy wrong code must not verify")
	}
	r, _ := NewRing(map[int][]byte{1: key(1)}, 1)
	_, dig, _ := r.Issue(chal(), "123456")
	if IsLegacyFormat(dig) {
		t.Fatal("HMAC digest must not be classified legacy")
	}
}

func TestNoPlaintextLeak(t *testing.T) {
	r, _ := NewRing(map[int][]byte{1: key(7)}, 1)
	_, dig, _ := r.Issue(Challenge{TenantID: "t1", Channel: "email", Destination: "a@x.com", ChallengeID: "c"}, "424242")
	if strings.Contains(dig, "424242") {
		t.Fatal("code leaked into digest")
	}
	if strings.Contains(dig, string(key(7))) {
		t.Fatal("key leaked into digest")
	}
}
