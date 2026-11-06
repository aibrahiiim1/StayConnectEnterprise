//go:build integration

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegration_PasswordResetRunsAsTheProductionRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set")
	}
	super := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var op string
	if err := super.QueryRow(ctx, `INSERT INTO operators(email, display_name, password_hash, status)
		VALUES ('role-reset-'||gen_random_uuid()||'@test.local', 'Role', 'x', 'active') RETURNING id::text`).Scan(&op); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM public.operator_password_reset_policy; DELETE FROM public.operator_password_reset_requests; DELETE FROM operators WHERE id = $1`, op)
	})

	var version int64
	if err := pool.QueryRow(ctx, `SELECT public.operator_password_reset_policy_set(true, 15, 3, 5, 20, false, NULL, 'svc_edged test', 'production role test')`).Scan(&version); err != nil {
		t.Fatalf("svc_edged cannot change the setting: %v", err)
	}
	s := &server{db: pool}
	p, err := s.readResetPolicy(ctx)
	if err != nil || !p.Enabled || p.MaxAttempts != 3 {
		t.Fatalf("svc_edged reads %+v, %v", p, err)
	}
	if _, err := pool.Exec(ctx, `SELECT public.operator_password_reset_policy_set(false, 15, 5, 5, 20, false, 0, 'svc_edged test', 'stale version')`); err == nil {
		t.Fatal("a change made from a stale version must be refused")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.operator_password_reset_policy_changes`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("svc_edged cannot read the change history: %d %v", n, err)
	}

	account, address := throttleKey(resetScopeAccount, "role@test.local"), throttleKey(resetScopeAddress, "203.0.113.9")
	var accOK, addrOK bool
	if err := pool.QueryRow(ctx, `SELECT account_allowed, address_allowed FROM public.operator_password_reset_request_allowed($1, $2)`, account, address).Scan(&accOK, &addrOK); err != nil || !accOK || !addrOK {
		t.Fatalf("first request: %v %v %v", accOK, addrOK, err)
	}
	if err := pool.QueryRow(ctx, `SELECT account_allowed, address_allowed FROM public.operator_password_reset_request_allowed($1, $2)`, account, address).Scan(&accOK, &addrOK); err != nil || accOK || !addrOK {
		t.Fatalf("a second request within a minute: %v %v %v", accOK, addrOK, err)
	}

	hash := resetCodeHash(op, "12345678")
	if _, err := pool.Exec(ctx, `SELECT public.operator_password_reset_issue($1, $2)`, op, hash); err != nil {
		t.Fatalf("svc_edged cannot issue a code: %v", err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT public.operator_password_reset_check($1, $2)`, op, resetCodeHash(op, "00000000")).Scan(&state); err != nil || state != "wrong" {
		t.Fatalf("wrong code: %q %v", state, err)
	}
	if err := pool.QueryRow(ctx, `SELECT public.operator_password_reset_fail($1)`, op).Scan(&state); err != nil || state != "wrong" {
		t.Fatalf("a wrong authenticator code: %q %v", state, err)
	}
	if err := pool.QueryRow(ctx, `SELECT public.operator_password_reset_check($1, $2)`, op, hash).Scan(&state); err != nil || state != "ok" {
		t.Fatalf("right code: %q %v", state, err)
	}
	var consumed bool
	if err := pool.QueryRow(ctx, `SELECT public.operator_password_reset_consume($1, $2)`, op, hash).Scan(&consumed); err != nil || !consumed {
		t.Fatalf("consume: %v %v", consumed, err)
	}
	if err := pool.QueryRow(ctx, `SELECT public.operator_password_reset_consume($1, $2)`, op, hash).Scan(&consumed); err != nil || consumed {
		t.Fatalf("a code must be used once: %v %v", consumed, err)
	}
	if _, err := pool.Exec(ctx, `SELECT public.operator_password_reset_issue($1, $2)`, op, hash); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_ = pool.QueryRow(ctx, `SELECT public.operator_password_reset_check($1, $2)`, op, resetCodeHash(op, "00000000")).Scan(&state)
	}
	if state != "exhausted" {
		t.Fatalf("after 3 wrong codes: %q", state)
	}
	if _, err := pool.Exec(ctx, `SELECT public.operator_password_reset_discard($1)`, op); err != nil {
		t.Fatalf("svc_edged cannot discard a code: %v", err)
	}

	for _, q := range []string{
		`SELECT code_hash FROM public.operator_password_resets LIMIT 1`,
		`DELETE FROM public.operator_password_reset_requests`,
		`UPDATE public.operator_password_reset_policy SET enabled = true`,
		`DELETE FROM public.operator_password_reset_policy_changes`,
	} {
		if _, err := pool.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("svc_edged must not run %q directly: %v", q, err)
		}
	}
}
