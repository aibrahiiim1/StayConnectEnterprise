package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

// AD-6: wrong second-step codes lock the address they came from; the account locks only at 4x across addresses.
func TestSecondStepLocksPerAddressWithAccountBackstop(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	m, err := NewMFA(pool, k)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('lock@central.test','x','active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	b32, _, err := m.Enrol(ctx, id, "lock@central.test")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
	step := totp.Step(time.Now())
	if _, ok, _, err := m.Confirm(WithClientAddr(ctx, "198.51.100.1"), id, totp.Code(secret, step)); err != nil || !ok {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	attacker, owner := WithClientAddr(ctx, "203.0.113.66"), WithClientAddr(ctx, "198.51.100.1")

	// signin_lock_failures (5) wrong codes from one address lock that address.
	var locked *time.Time
	for i := 0; i < DefaultSigninLockFailures; i++ {
		_, _, ok, until, err := m.Verify(attacker, id, "000000")
		if err != nil || ok {
			t.Fatalf("wrong code %d: ok=%v err=%v", i, ok, err)
		}
		locked = until
	}
	if locked == nil {
		t.Fatal("the fifth wrong code from one address did not start a lock")
	}
	var le ErrMFALocked
	if _, _, _, _, err := m.Verify(attacker, id, "000000"); !errors.As(err, &le) {
		t.Fatalf("the locked address was still checked: %v", err)
	}
	// The real user, from another address, is not locked out and can sign in.
	if _, _, ok, _, err := m.Verify(owner, id, totp.Code(secret, step+1)); err != nil || !ok {
		t.Fatalf("the real user was locked out by someone else's wrong codes: ok=%v err=%v", ok, err)
	}

	// Wrong codes spread over many addresses reach the account-wide backstop at 4x the limit.
	n := DefaultSigninLockFailures * MFAAccountBackstopFactor
	var accountLock *time.Time
	for i := 0; i < n; i++ {
		_, _, _, until, err := m.Verify(WithClientAddr(ctx, fmt.Sprintf("192.0.2.%d", i+1)), id, "000000")
		if err != nil {
			t.Fatalf("spread attempt %d: %v", i, err)
		}
		if until != nil {
			accountLock = until
		}
	}
	if accountLock == nil {
		t.Fatalf("%d wrong codes from distinct addresses never locked the account", n)
	}
	if _, _, _, _, err := m.Verify(owner, id, totp.Code(secret, step+1)); !errors.As(err, &le) {
		t.Fatalf("the account backstop did not hold for the owner's address: %v", err)
	}
	// Resetting the second step clears the address locks too.
	if _, err := ResetMFA(ctx, pool, id); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_mfa_address_locks WHERE operator_id=$1::uuid`, id).Scan(&left)
	if left != 0 {
		t.Fatalf("%d address locks survived a reset", left)
	}
}
