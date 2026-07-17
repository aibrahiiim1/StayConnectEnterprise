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
	// changing any bound field must fail verification
	mods := []Challenge{
		{TenantID: "OTHER", Channel: "email", Destination: "a@example.com", ChallengeID: "cid-1"},
		{TenantID: "t1", Channel: "sms", Destination: "a@example.com", ChallengeID: "cid-1"},   // cross-channel
		{TenantID: "t1", Channel: "email", Destination: "b@example.com", ChallengeID: "cid-1"}, // cross-identity
		{TenantID: "t1", Channel: "email", Destination: "a@example.com", ChallengeID: "cid-2"}, // cross-challenge (replay)
	}
	for i, c := range mods {
		if ok, _ := r.Verify(1, dig, c, "123456"); ok {
			t.Fatalf("mod %d must be rejected", i)
		}
	}
}

func TestRotationKeepsOldVerifiable(t *testing.T) {
	r1, _ := NewRing(map[int][]byte{1: key(1)}, 1)
	gen1, dig1, _ := r1.Issue(chal(), "123456") // pinned to gen 1
	// rotate: gen 2 active, gen 1 retained
	r2, _ := NewRing(map[int][]byte{1: key(1), 2: key(2)}, 2)
	if r2.Active() != 2 {
		t.Fatal("active should be 2 after rotation")
	}
	// old OTP (pinned gen1) still verifies via retained key
	if ok, _ := r2.Verify(gen1, dig1, chal(), "123456"); !ok {
		t.Fatal("rotation must keep old-generation OTPs verifiable")
	}
	// new issuance uses gen 2
	genN, digN, _ := r2.Issue(chal(), "654321")
	if genN != 2 {
		t.Fatal("new issuance must use active gen 2")
	}
	// gen2 digest differs from gen1 digest for same code (different key)
	_, dig1same, _ := r1.Issue(chal(), "654321")
	if digN == dig1same {
		t.Fatal("different generations must produce different digests")
	}
	// once gen1 is removed (past TTL), gen1 verify fails closed
	r3, _ := NewRing(map[int][]byte{2: key(2)}, 2)
	if ok, err := r3.Verify(1, dig1, chal(), "123456"); ok || err == nil {
		t.Fatal("removed generation must fail closed")
	}
}

func TestLegacyCompat(t *testing.T) {
	// build a legacy "salt:sha256(salt|code)" digest exactly like the current otp.go format
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
	// a keyed-HMAC digest (bare 64-hex, no salt colon) is NOT legacy format
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
