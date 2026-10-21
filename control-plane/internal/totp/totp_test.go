package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA-1 column, truncated to 6 digits (the RFC prints 8).
func TestCodeMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		unix int64
		want string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		if got := Code(secret, Step(time.Unix(c.unix, 0))); got != c.want {
			t.Errorf("at %d: %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestCheckAcceptsOneStepEitherSideAndNothingElse(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	cur := Step(now)
	for d, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		step, ok := Check(secret, Code(secret, cur+d), now)
		if ok != want || (ok && step != cur+d) {
			t.Errorf("step %+d: ok=%v step=%d", d, ok, step)
		}
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "05047!"} {
		if _, ok := Check(secret, bad, now); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, ok := Check(secret, " 050 471 ", now); !ok {
		t.Error("a code typed with spaces was refused")
	}
}

func TestURIAndSecret(t *testing.T) {
	s, err := NewSecret()
	if err != nil || len(s) != SecretBytes {
		t.Fatalf("secret %d bytes, %v", len(s), err)
	}
	u := URI(s, "StayConnect Grand Hotel", "ada@hotel.example")
	if !strings.HasPrefix(u, "otpauth://totp/StayConnect%20Grand%20Hotel:ada@hotel.example?") ||
		!strings.Contains(u, "secret="+EncodeSecret(s)) || !strings.Contains(u, "digits=6") || !strings.Contains(u, "period=30") {
		t.Fatalf("uri %s", u)
	}
}
