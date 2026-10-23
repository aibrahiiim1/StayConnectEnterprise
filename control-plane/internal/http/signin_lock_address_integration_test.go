package http

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

func TestSignInLockHoldsOnlyTheAddressItCameFrom(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct-horse-battery")
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('admin@central.test',$1,'active') RETURNING id::text`,
		hash).Scan(&id); err != nil {
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
	d := authDeps{Repo: &auth.Repo{DB: pool}, DB: pool, Failures: ratebudget.New(50, 300, time.Minute), MFA: mfa}
	type from struct{ remote, realIP string }
	direct := func(ip string) from { return from{remote: ip + ":40000"} }
	viaCaddy := func(ip string) from { return from{remote: "127.0.0.1:50000", realIP: ip} }
	send := func(h http.HandlerFunc, f from, body any, sess *auth.Session) (int, string) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.RemoteAddr = f.remote
		if f.realIP != "" {
			req.Header.Set("X-Real-IP", f.realIP)
		}
		if sess != nil {
			req = req.WithContext(auth.ContextWithSession(req.Context(), sess))
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		e, _ := out["error"].(string)
		return rec.Code, e
	}
	login := func(f from, email, pw string) (int, string) {
		return send(d.login, f, map[string]string{"email": email, "password": pw}, nil)
	}

	a, b := viaCaddy("198.51.100.10"), viaCaddy("198.51.100.20")
	for i := 0; i < 2; i++ {
		if code, _ := login(a, "admin@central.test", "wrong"); code != 401 {
			t.Fatalf("wrong password %d from A = %d", i+1, code)
		}
	}
	if code, e := login(a, "admin@central.test", "wrong"); code != 429 || e != "signin_locked" {
		t.Fatalf("third wrong password from A = %d %q", code, e)
	}
	if code, e := login(a, "admin@central.test", "correct-horse-battery"); code != 429 || e != "signin_locked" {
		t.Fatalf("the right password from A during its lock = %d %q", code, e)
	}
	// The lock holds only at A.
	if code, _ := login(b, "admin@central.test", "correct-horse-battery"); code != 200 {
		t.Fatalf("the right password from B while A is locked = %d", code)
	}
	// Only Caddy on loopback may set the address via X-Real-IP.
	if code, _ := login(from{remote: "198.51.100.10:1", realIP: "198.51.100.30"}, "admin@central.test", "correct-horse-battery"); code != 429 {
		t.Fatalf("a direct client from A claiming another address = %d, want the lock", code)
	}
	if code, _ := login(direct("198.51.100.30"), "admin@central.test", "correct-horse-battery"); code != 200 {
		t.Fatalf("a direct client from another address = %d", code)
	}

	sess := &auth.Session{OperatorID: id, Email: "admin@central.test"}
	if code, e := send(d.reauth, a, map[string]string{"password": "correct-horse-battery"}, sess); code != 429 || e != "signin_locked" {
		t.Fatalf("step-up from A during its lock = %d %q", code, e)
	}
	if code, _ := send(d.reauth, b, map[string]string{"password": "correct-horse-battery"}, sess); code != 200 {
		t.Fatalf("step-up from B = %d", code)
	}

	for i := 0; i < 2; i++ {
		if code, _ := login(a, "nobody@central.test", "x"); code != 401 {
			t.Fatalf("unknown name from A, attempt %d = %d", i+1, code)
		}
	}
	if code, e := login(a, "nobody@central.test", "x"); code != 429 || e != "signin_locked" {
		t.Fatalf("unknown name, third attempt from A = %d %q", code, e)
	}
	if code, _ := login(b, "nobody@central.test", "x"); code != 401 {
		t.Fatalf("unknown name from B = %d", code)
	}

	// No row holds the address, the email or a plain hash of the name: only HMACs.
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_signin_throttle WHERE key_hash LIKE '%198.51%' OR name_hash LIKE '%@%'`).Scan(&n)
	if n != 0 {
		t.Fatal("the throttle stores an address or an email")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operator_signin_throttle WHERE name_hash = $1`, mfa.SigninName("admin@central.test")).Scan(&n)
	if n != 1 {
		t.Fatalf("the administrator has %d counter rows, want the one for A (B's success cleared nothing it did not own)", n)
	}

	if c, err := auth.ClearSigninLocks(ctx, pool, mfa.SigninName("admin@central.test")); err != nil || c != 1 {
		t.Fatalf("clear = %d %v", c, err)
	}
	if code, _ := login(a, "admin@central.test", "correct-horse-battery"); code != 200 {
		t.Fatalf("A after the unlock = %d", code)
	}
}
