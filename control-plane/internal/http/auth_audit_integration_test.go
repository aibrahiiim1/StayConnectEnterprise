package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

func TestFailedLoginsAreAuditedWithinABudget(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('ops@central.test',$1,'active') RETURNING id::text`, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Raise the lockout out of the way: this test is about the audit budget.
	if _, err := pool.Exec(ctx, `UPDATE central_security_settings SET signin_lock_failures = 20 WHERE id`); err != nil {
		t.Fatal(err)
	}
	d := authDeps{Repo: &auth.Repo{DB: pool}, DB: pool, Failures: ratebudget.New(5, 300, time.Minute)}
	post := func(body, ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(body))
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		d.login(rec, req)
		return rec.Code
	}
	for i := 0; i < 8; i++ {
		if c := post(`{"email":"ops@central.test","password":"wrong-password"}`, "203.0.113.20"); c != http.StatusUnauthorized {
			t.Fatalf("wrong password: %d", c)
		}
	}
	if c := post(`{"email":"nobody@central.test","password":"whatever"}`, "203.0.113.21"); c != http.StatusUnauthorized {
		t.Fatalf("unknown email: %d", c)
	}
	var fromFirst, unknown int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='auth.login_failed' AND host(ip)='203.0.113.20'
                             AND actor_id=$1 AND payload->>'reason'='wrong password'`, id).Scan(&fromFirst)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='auth.login_failed' AND actor_type='api'
                             AND payload->>'reason'='unknown email'`).Scan(&unknown)
	if fromFirst != 5 {
		t.Fatalf("one address records at most 5 failures per minute, got %d", fromFirst)
	}
	if unknown != 1 {
		t.Fatalf("an unknown-email failure must be audited too, got %d", unknown)
	}
	// A typed email may be anyone's address, so it is never stored.
	var typed int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action LIKE 'auth.%' AND payload::text LIKE '%@central.test%'`).Scan(&typed)
	if typed != 0 {
		t.Fatalf("%d sign-in audit entries hold a typed email address", typed)
	}
}
