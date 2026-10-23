package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

func TestCheckKeyCatchesTheWrongKey(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	newKey := func() *MFA {
		k := make([]byte, 32)
		_, _ = rand.Read(k)
		m, err := NewMFA(pool, k)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	right, wrong := newKey(), newKey()
	if o, u, err := wrong.CheckKey(ctx); err != nil || o != 0 || u != 0 {
		t.Fatalf("no authenticators: %d %d %v", o, u, err)
	}

	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('key@central.test','x','active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	b32, _, err := right.Enrol(ctx, id, "key@central.test")
	if err != nil {
		t.Fatal(err)
	}
	// An authenticator that is set up but not yet confirmed proves nothing either way.
	if _, _, err := wrong.CheckKey(ctx); err != nil {
		t.Fatalf("an unconfirmed authenticator failed the check: %v", err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _, err := right.Confirm(ctx, id, totp.Code(secret, totp.Step(time.Now()))); err != nil || !ok {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	if o, u, err := right.CheckKey(ctx); err != nil || o != 1 || u != 0 {
		t.Fatalf("the right key: opened %d unreadable %d err %v", o, u, err)
	}
	if _, u, err := wrong.CheckKey(ctx); !errors.Is(err, ErrMFAWrongKey) || u != 1 {
		t.Fatalf("the wrong key: unreadable %d err %v", u, err)
	}
}
