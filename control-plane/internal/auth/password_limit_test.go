package auth

import (
	"errors"
	"testing"
	"time"
)

// X-12: at most argonSlots password evaluations run at once; the rest wait and then fail with ErrBusy, so
// concurrent sign-in attempts from many addresses cannot each allocate 64 MiB at the same time.
func TestPasswordChecksAreConcurrencyBounded(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	old := argonWait
	argonWait = 50 * time.Millisecond
	defer func() { argonWait = old }()

	for i := 0; i < argonSlots; i++ {
		if err := acquireArgon(); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyPassword(hash, "correct-horse-battery"); !errors.Is(err, ErrBusy) {
		t.Fatalf("with every slot taken a check must fail busy, got %v", err)
	}
	for i := 0; i < argonSlots; i++ {
		releaseArgon()
	}
	if err := VerifyPassword(hash, "correct-horse-battery"); err != nil {
		t.Fatalf("with a free slot the check runs: %v", err)
	}
}

// A stored hash that asks for more memory than the bound is refused, never evaluated.
func TestOversizedHashParametersAreRefused(t *testing.T) {
	huge := "$argon2id$v=19$m=4194304,t=1,p=4$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	if err := VerifyPassword(huge, "x"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("a 4 GiB argon2 parameter must be refused, got %v", err)
	}
}
