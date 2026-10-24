package http

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

func TestSignInLocksAfterRepeatedWrongPasswords(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct-horse-battery")
	if _, err := pool.Exec(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('lock@central.test',$1,'active')`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE central_security_settings SET signin_lock_failures = 3, signin_lock_minutes = 10 WHERE id`); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	var sessions int
	d := authDeps{Repo: &auth.Repo{DB: pool}, DB: pool, Failures: ratebudget.New(50, 300, time.Minute), MFA: mfa,
		NewSession: testSessions(&sessions)}
	login := func(email, pw string) (int, string, string) {
		raw, _ := json.Marshal(map[string]string{"email": email, "password": pw})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.RemoteAddr = "203.0.113.41:40000"
		rec := httptest.NewRecorder()
		d.login(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		e, _ := body["error"].(string)
		return rec.Code, e, rec.Header().Get("Retry-After")
	}

	// A right password clears the count.
	for _, pw := range []string{"nope-1", "nope-2", "correct-horse-battery", "nope-3", "nope-4"} {
		if code, _, _ := login("lock@central.test", pw); code == http.StatusTooManyRequests {
			t.Fatalf("locked after %q, but a right password had cleared the count", pw)
		}
	}
	if code, e, after := login("LOCK@central.test", "nope-5"); code != 429 || e != "signin_locked" || after == "" {
		t.Fatalf("third wrong password = %d %q Retry-After=%q", code, e, after)
	}
	if code, e, _ := login("lock@central.test", "correct-horse-battery"); code != 429 || e != "signin_locked" {
		t.Fatalf("the right password during the lock = %d %q", code, e)
	}

	// An unknown name is locked exactly like a real one, so the lock reveals nothing about accounts.
	for i := 0; i < 2; i++ {
		if code, _, _ := login("nobody@central.test", "x"); code != 401 {
			t.Fatalf("unknown name, attempt %d = %d", i+1, code)
		}
	}
	if code, e, _ := login("nobody@central.test", "x"); code != 429 || e != "signin_locked" {
		t.Fatalf("unknown name, third attempt = %d %q", code, e)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_signin_throttle WHERE key_hash LIKE '%@%'`).Scan(&n)
	if n != 0 {
		t.Fatal("the throttle stores an email address")
	}
	// A plain hash would be reversible from a list of addresses; the key is an HMAC.
	plain := sha256.Sum256([]byte("lock@central.test"))
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_signin_throttle WHERE key_hash = $1`, hex.EncodeToString(plain[:])).Scan(&n)
	if n != 0 {
		t.Fatal("the throttle key is a plain SHA-256 of the email")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'auth.signin_locked'`).Scan(&n)
	if n != 2 {
		t.Fatalf("auth.signin_locked audited %d times, want 2", n)
	}

	if _, err := pool.Exec(ctx, `UPDATE operator_signin_throttle SET locked_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := login("lock@central.test", "correct-horse-battery"); code != 200 {
		t.Fatalf("after the lock = %d", code)
	}
}
