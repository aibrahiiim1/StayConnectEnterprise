package main

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// `ctrlapi unlock-signin` (0056, decision D-R1) clears every wrong-password lock on one name, from every address,
// and only that name's; it is audited by account id, never by email.
func TestUnlockSigninClearsEveryLockOnTheName(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('locked@central.test','x','active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	fail := func(email, addr string) {
		if _, err := auth.SigninFailed(ctx, pool, mfa.SigninKey(email, addr), mfa.SigninName(email)); err != nil {
			t.Fatal(err)
		}
	}
	fail("locked@central.test", "203.0.113.1")
	fail("locked@central.test", "203.0.113.2")
	fail("bystander@central.test", "203.0.113.1")

	gotID, n, err := unlockSignin(ctx, pool, mfa, " Locked@Central.test ", "locked out by a scanner")
	if err != nil || n != 2 || gotID != id {
		t.Fatalf("unlock = %q %d %v", gotID, n, err)
	}
	var left, bystander, audited int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_signin_throttle WHERE name_hash = $1`, mfa.SigninName("locked@central.test")).Scan(&left)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_signin_throttle WHERE name_hash = $1`, mfa.SigninName("bystander@central.test")).Scan(&bystander)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'user.signin_unlocked' AND target_id = $1
		AND payload->>'reason' = 'locked out by a scanner' AND payload->>'locks_cleared' = '2'
		AND payload::text NOT LIKE '%@%'`, id).Scan(&audited)
	if left != 0 || bystander != 1 || audited != 1 {
		t.Fatalf("after unlock: %d locks left, bystander %d, audited %d", left, bystander, audited)
	}

	// A name with no account is cleared the same way, and the audit row names no account.
	fail("ghost@central.test", "203.0.113.9")
	if gotID, n, err := unlockSignin(ctx, pool, mfa, "ghost@central.test", "test cleanup"); err != nil || n != 1 || gotID != "" {
		t.Fatalf("unknown name unlock = %q %d %v", gotID, n, err)
	}
}
