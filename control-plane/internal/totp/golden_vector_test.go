package totp

import (
	"testing"
	"time"
)

// AD-61: this file is byte-identical in control-plane/internal/totp and data-plane/internal/totp. The code for a fixed
// secret and step is pinned (RFC 6238 test secret, SHA-1, 6 digits, 30 s step), so the two copies cannot drift.
func TestGoldenVectorCode(t *testing.T) {
	secret := []byte("12345678901234567890")
	for step, want := range map[int64]string{1: goldenStep1, 37037036: goldenStep37037036} {
		if got := Code(secret, step); got != want {
			t.Fatalf("code(step %d) = %s, want %s", step, got, want)
		}
	}
	if got := Step(time.Unix(59, 0)); got != 1 {
		t.Fatalf("step at t=59 = %d, want 1", got)
	}
}
