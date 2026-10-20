package auth

import (
	"testing"
	"time"
)

// A name Central does not know costs the same as a wrong password for one it does.
func TestSpendPasswordCheck_TakesAsLongAsARealCheck(t *testing.T) {
	h, err := HashPassword("right")
	if err != nil {
		t.Fatal(err)
	}
	SpendPasswordCheck("warm up")
	t0 := time.Now()
	_ = VerifyPassword(h, "wrong")
	real := time.Since(t0)
	t0 = time.Now()
	SpendPasswordCheck("wrong")
	decoy := time.Since(t0)
	if decoy < real/2 {
		t.Fatalf("decoy %v is much faster than a real check %v", decoy, real)
	}
}
