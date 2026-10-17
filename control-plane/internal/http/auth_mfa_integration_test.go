package http

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

// GDPR plan C10 (migration 0052, decision D-C10): Central's console signs in with a password AND an authenticator
// code. The password alone opens nothing; the first sign-in sets the app up and hands out ten recovery codes;
// a code is accepted once; a recovery code works once; wrong codes lock; the secret is never stored in the clear.
func TestConsoleSignInNeedsTheAuthenticatorCode(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct-horse-battery")
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('mfa@central.test',$1,'active') RETURNING id::text`, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_admin')`, id); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	sessions := 0
	d := authDeps{Repo: &auth.Repo{DB: pool}, DB: pool, Failures: ratebudget.New(5, 300, time.Minute), MFA: mfa,
		NewSession: func(_ context.Context, s auth.Session) (*auth.Session, error) {
			sessions++
			s.Token = "tok-" + strings.Repeat("x", sessions)
			s.CreatedAt, s.LastSeenAt, s.ExpiresAt = time.Now(), time.Now(), time.Now().Add(12*time.Hour)
			return &s, nil
		}}

	type res struct {
		code    int
		body    map[string]any
		pending string
		session string
	}
	call := func(h http.HandlerFunc, body any, pending string) res {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.RemoteAddr = "203.0.113.40:40000"
		if pending != "" {
			req.AddCookie(&http.Cookie{Name: auth.PendingCookieName, Value: pending})
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		out := res{code: rec.Code, body: map[string]any{}}
		_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
		for _, c := range rec.Result().Cookies() {
			switch {
			case c.Name == auth.PendingCookieName && c.MaxAge >= 0:
				out.pending = c.Value
			case c.Name == auth.SessionCookieName && c.Value != "":
				out.session = c.Value
			}
		}
		return out
	}
	password := func() res {
		return call(d.login, map[string]string{"email": "mfa@central.test", "password": "correct-horse-battery"}, "")
	}

	// The password alone opens nothing.
	p := password()
	if p.code != 200 || p.body["second_step"] != "enrol" || p.session != "" || p.pending == "" || sessions != 0 {
		t.Fatalf("password step = %d %v session=%q", p.code, p.body, p.session)
	}
	if r := call(d.loginCode, map[string]string{"code": "123456"}, ""); r.code != 401 {
		t.Fatalf("a code without the password step = %d", r.code)
	}
	e := call(d.loginEnrol, nil, p.pending)
	secretB32, _ := e.body["secret"].(string)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if e.code != 200 || err != nil || len(secret) != totp.SecretBytes {
		t.Fatalf("set-up = %d %v", e.code, e.body)
	}
	var ct []byte
	_ = pool.QueryRow(ctx, `SELECT ciphertext FROM operator_mfa WHERE operator_id=$1`, id).Scan(&ct)
	if len(ct) == 0 || bytes.Contains(ct, secret) {
		t.Fatal("the secret is stored in the clear")
	}
	if r := call(d.loginCode, map[string]string{"code": "000000"}, p.pending); r.code != 401 || r.body["error"] != "wrong_code" {
		t.Fatalf("wrong first code = %d %v", r.code, r.body)
	}
	now := time.Now()
	first := totp.Code(secret, totp.Step(now))
	done := call(d.loginCode, map[string]string{"code": first}, p.pending)
	codes, _ := done.body["recovery_codes"].([]any)
	if done.code != 200 || done.session == "" || len(codes) != 10 || done.body["operator_id"] != id {
		t.Fatalf("confirming the app = %d %v", done.code, done.body)
	}

	// From now on: the password, then the code -- never the same code twice.
	p2 := password()
	if p2.body["second_step"] != "code" {
		t.Fatalf("second sign-in = %v", p2.body)
	}
	if r := call(d.loginCode, map[string]string{"code": first}, p2.pending); r.code != 401 {
		t.Fatalf("a used code was accepted again: %d", r.code)
	}
	if r := call(d.loginCode, map[string]string{"code": totp.Code(secret, totp.Step(now)+1)}, p2.pending); r.code != 200 || r.session == "" {
		t.Fatalf("next code = %d %v", r.code, r.body)
	}
	// A recovery code works once.
	rc := codes[2].(string)
	p3 := password()
	if r := call(d.loginCode, map[string]string{"code": strings.ToLower(rc)}, p3.pending); r.code != 200 || r.body["recovery_codes_left"] != float64(9) {
		t.Fatalf("recovery code = %d %v", r.code, r.body)
	}
	p4 := password()
	if r := call(d.loginCode, map[string]string{"code": rc}, p4.pending); r.code != 401 {
		t.Fatalf("a used recovery code worked again: %d", r.code)
	}
	// Five wrong codes lock it; the right one then waits too.
	for i := 0; i < 4; i++ {
		_ = call(d.loginCode, map[string]string{"code": "000000"}, p4.pending)
	}
	if r := call(d.loginCode, map[string]string{"code": totp.Code(secret, totp.Step(time.Now())+1)}, p4.pending); r.code != http.StatusTooManyRequests {
		t.Fatalf("the right code during a lockout = %d %v", r.code, r.body)
	}
	var failed, locked int
	_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='auth.mfa_failed'), count(*) FILTER (WHERE action='auth.mfa_locked')
		FROM audit_log WHERE target_id=$1`, id).Scan(&failed, &locked)
	if failed < 5 || locked != 1 {
		t.Fatalf("wrong codes audited %d, lockouts %d", failed, locked)
	}

	// Without the key, sign-in is refused rather than let through on a password.
	d.MFA = nil
	if r := password(); r.code != http.StatusServiceUnavailable || r.session != "" {
		t.Fatalf("sign-in without the two-step key = %d", r.code)
	}
}
