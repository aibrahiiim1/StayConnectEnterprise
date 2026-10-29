package http

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

// Two-step sign-in is optional per account (D68): the password alone opens the session until the operator turns it
// on; from then on only the code does, and turning it off needs a code too.
func TestConsoleTwoStepSignInIsOptionalAndEnforcedOnceOn(t *testing.T) {
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
	own := &auth.Session{OperatorID: id, Email: "mfa@central.test", Roles: []string{"platform_admin"}}
	call := func(h http.HandlerFunc, body any, pending string, sess *auth.Session) res {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.RemoteAddr = "203.0.113.40:40000"
		if pending != "" {
			req.AddCookie(&http.Cookie{Name: auth.PendingCookieName, Value: pending})
		}
		if sess != nil {
			req = req.WithContext(auth.ContextWithSession(req.Context(), sess))
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
		return call(d.login, map[string]string{"email": "mfa@central.test", "password": "correct-horse-battery"}, "", nil)
	}
	count := func(action, extra string) int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action=$1 AND target_id=$2 AND `+extra, action, id).Scan(&n)
		return n
	}

	p := password()
	if p.code != 200 || p.body["second_step"] != "none" || p.session == "" || p.pending != "" || p.body["operator_id"] != id || sessions != 1 {
		t.Fatalf("password-only sign-in = %d %v session=%q pending=%q", p.code, p.body, p.session, p.pending)
	}
	if n := count("auth.login_succeeded", `payload->>'second_step'='none'`); n != 1 {
		t.Fatalf("password-only sign-in audited %d times", n)
	}

	// A set-up never confirmed does not count, and starting again replaces it.
	s1 := call(d.mfaSetup, nil, "", own)
	if s1.code != 200 || s1.body["secret"] == "" {
		t.Fatalf("set-up = %d %v", s1.code, s1.body)
	}
	if r := password(); r.body["second_step"] != "none" || r.session == "" {
		t.Fatalf("an unconfirmed set-up changed sign-in: %v", r.body)
	}
	s2 := call(d.mfaSetup, nil, "", own)
	secretB32, _ := s2.body["secret"].(string)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if s2.code != 200 || err != nil || len(secret) != totp.SecretBytes || secretB32 == s1.body["secret"] {
		t.Fatalf("set-up again = %d %v", s2.code, s2.body)
	}
	var ct []byte
	_ = pool.QueryRow(ctx, `SELECT ciphertext FROM operator_mfa WHERE operator_id=$1`, id).Scan(&ct)
	if len(ct) == 0 || bytes.Contains(ct, secret) {
		t.Fatal("the secret is stored in the clear")
	}
	old, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s1.body["secret"].(string))
	if r := call(d.mfaConfirm, map[string]string{"code": totp.Code(old, totp.Step(time.Now()))}, "", own); r.code != 400 || r.body["error"] != "wrong_code" {
		t.Fatalf("the replaced set-up's code = %d %v", r.code, r.body)
	}
	if r := call(d.mfaConfirm, map[string]string{"code": "ABCDE-FGHJK"}, "", own); r.code != 400 || r.body["error"] != "code_required" {
		t.Fatalf("confirm with a recovery code = %d %v", r.code, r.body)
	}
	now := time.Now()
	first := totp.Code(secret, totp.Step(now))
	done := call(d.mfaConfirm, map[string]string{"code": first}, "", own)
	codes, _ := done.body["recovery_codes"].([]any)
	if done.code != 200 || len(codes) != 10 {
		t.Fatalf("confirming the app = %d %v", done.code, done.body)
	}
	if r := call(d.mfaConfirm, map[string]string{"code": first}, "", own); r.code != 409 {
		t.Fatalf("a confirmed authenticator was confirmed again: %d %v", r.code, r.body)
	}
	if r := call(d.mfaSetup, nil, "", own); r.code != 409 || r.body["error"] != "already_enrolled" {
		t.Fatalf("set-up replaced a confirmed authenticator: %d %v", r.code, r.body)
	}
	if count("auth.mfa_setup_started", "true") != 2 || count("auth.mfa_enrolled", "true") != 1 {
		t.Fatal("set-up and confirmation were not audited")
	}

	// Once on, the password opens no session.
	before := sessions
	p2 := password()
	if p2.code != 200 || p2.body["second_step"] != "code" || p2.session != "" || p2.pending == "" || sessions != before || p2.body["operator_id"] != nil {
		t.Fatalf("password step with two-step on = %d %v session=%q", p2.code, p2.body, p2.session)
	}
	if r := call(d.loginCode, map[string]string{"code": "123456"}, "", nil); r.code != 401 {
		t.Fatalf("a code without the password step = %d", r.code)
	}
	if r := call(d.loginCode, map[string]string{"code": first}, p2.pending, nil); r.code != 401 || r.session != "" {
		t.Fatalf("a used code was accepted again: %d", r.code)
	}
	if r := call(d.loginCode, map[string]string{"code": totp.Code(secret, totp.Step(now)+1)}, p2.pending, nil); r.code != 200 || r.session == "" {
		t.Fatalf("next code = %d %v", r.code, r.body)
	}
	if n := count("auth.login_succeeded", `payload->>'second_step'='totp'`); n != 1 {
		t.Fatalf("code sign-in audited %d times", n)
	}
	rc := codes[2].(string)
	p3 := password()
	if r := call(d.loginCode, map[string]string{"code": strings.ToLower(rc)}, p3.pending, nil); r.code != 200 || r.body["recovery_codes_left"] != float64(9) {
		t.Fatalf("recovery code = %d %v", r.code, r.body)
	}
	p4 := password()
	if r := call(d.loginCode, map[string]string{"code": rc}, p4.pending, nil); r.code != 401 {
		t.Fatalf("a used recovery code worked again: %d", r.code)
	}

	// Turning it off needs a code from the second factor; a wrong one changes nothing and counts.
	if r := call(d.mfaDisable, map[string]string{}, "", own); r.code != 400 || r.body["error"] != "code_required" {
		t.Fatalf("off without a code = %d %v", r.code, r.body)
	}
	if r := call(d.mfaDisable, map[string]string{"code": rc}, "", own); r.code != 400 || r.body["error"] != "wrong_code" {
		t.Fatalf("off with a used recovery code = %d %v", r.code, r.body)
	}
	if st, _ := mfa.Status(ctx, id); !st.Enrolled {
		t.Fatal("a refused request turned two-step sign-in off")
	}
	for i := 0; i < 3; i++ {
		_ = call(d.loginCode, map[string]string{"code": "000000"}, p4.pending, nil)
	}
	if r := call(d.loginCode, map[string]string{"code": totp.Code(secret, totp.Step(time.Now())+1)}, p4.pending, nil); r.code != http.StatusTooManyRequests {
		t.Fatalf("the right code during a lockout = %d %v", r.code, r.body)
	}
	if r := call(d.mfaDisable, map[string]string{"code": codes[3].(string)}, "", own); r.code != http.StatusTooManyRequests {
		t.Fatalf("off during a code lockout = %d %v", r.code, r.body)
	}
	if count("auth.mfa_failed", "true") < 5 || count("auth.mfa_locked", "true") != 1 {
		t.Fatalf("wrong codes audited %d, lockouts %d", count("auth.mfa_failed", "true"), count("auth.mfa_locked", "true"))
	}
	// AD-6: the wrong codes above locked this client address (operator_mfa_address_locks), not the whole account.
	if _, err := pool.Exec(ctx, `UPDATE operator_mfa SET locked_until = NULL WHERE operator_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE operator_mfa_address_locks SET locked_until = NULL WHERE operator_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if r := call(d.mfaDisable, map[string]string{"code": codes[3].(string)}, "", own); r.code != 200 || r.body["enrolled"] != false {
		t.Fatalf("off with a recovery code = %d %v", r.code, r.body)
	}
	if n := count("auth.mfa_disabled", `payload->>'second_step'='recovery_code'`); n != 1 {
		t.Fatalf("turning it off audited %d times", n)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operator_mfa WHERE operator_id=$1) + (SELECT count(*) FROM operator_mfa_recovery_codes WHERE operator_id=$1)`, id).Scan(&left)
	if left != 0 {
		t.Fatalf("%d authenticator rows left after turning it off", left)
	}
	if r := call(d.loginCode, map[string]string{"code": totp.Code(secret, totp.Step(time.Now())+1)}, p4.pending, nil); r.code == 200 {
		t.Fatal("a pending sign-in outlived turning two-step sign-in off")
	}
	if r := password(); r.body["second_step"] != "none" || r.session == "" {
		t.Fatalf("after turning it off the password alone must sign in: %v", r.body)
	}
	if r := call(d.mfaDisable, map[string]string{"code": "123456"}, "", own); r.code != 409 {
		t.Fatalf("off when already off = %d %v", r.code, r.body)
	}

	// Without the key, sign-in is refused rather than let through on the password alone.
	d.MFA = nil
	if r := password(); r.code != http.StatusServiceUnavailable || r.session != "" {
		t.Fatalf("sign-in without the two-step key = %d", r.code)
	}
}

// Through the real router: turning two-step sign-in on or off needs a fresh password step-up, and nothing but the
// code step opens a session for an account that has it on.
func TestConsoleTwoStepManagementNeedsTheStepUp(t *testing.T) {
	pool := testdb.New(t)
	url := os.Getenv("CTRLAPI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("CTRLAPI_TEST_REDIS_URL is not set: this test needs a Redis")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	ctx := context.Background()
	email := "router-" + strings.ToLower(rand.Text()[:8]) + "@central.test"
	hash, _ := auth.HashPassword("correct-horse-battery")
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ($1,$2,'active') RETURNING id::text`, email, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_admin')`, id); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, _ := auth.NewMFA(pool, key)
	srv := httptest.NewServer(NewRouter(Deps{DB: pool, Redis: rdb, MFA: mfa, rateCounter: noLimit{}}))
	defer srv.Close()

	client := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, Timeout: 10 * time.Second}
	}
	do := func(cl *http.Client, method, path string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		res, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	creds := map[string]string{"email": email, "password": "correct-horse-battery"}

	cl := client()
	if st, b := do(cl, "POST", "/v1/auth/login", creds); st != 200 || b["second_step"] != "none" {
		t.Fatalf("password-only sign-in = %d %v", st, b)
	}
	if st, b := do(cl, "GET", "/v1/auth/whoami", nil); st != 200 || b["operator_id"] != id {
		t.Fatalf("whoami = %d %v", st, b)
	}
	elsewhere := client()
	if st, b := do(elsewhere, "POST", "/v1/auth/login", creds); st != 200 || b["second_step"] != "none" {
		t.Fatalf("a second password-only sign-in = %d %v", st, b)
	}
	if st, b := do(cl, "POST", "/v1/auth/mfa/setup", nil); st != 403 || b["error"] != "reauth_required" {
		t.Fatalf("set-up without the step-up = %d %v", st, b)
	}
	if st, b := do(cl, "POST", "/v1/auth/mfa/confirm", map[string]string{"code": "123456"}); st != 403 || b["error"] != "reauth_required" {
		t.Fatalf("confirm without the step-up = %d %v", st, b)
	}
	if st, _ := do(cl, "POST", "/v1/auth/reauth", map[string]string{"password": "correct-horse-battery"}); st != 200 {
		t.Fatalf("step-up = %d", st)
	}
	st, b := do(cl, "POST", "/v1/auth/mfa/setup", nil)
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(b["secret"].(string)))
	if st != 200 || len(secret) != totp.SecretBytes {
		t.Fatalf("set-up = %d %v", st, b)
	}
	step := totp.Step(time.Now())
	st, b = do(cl, "POST", "/v1/auth/mfa/confirm", map[string]string{"code": totp.Code(secret, step)})
	codes, _ := b["recovery_codes"].([]any)
	if st != 200 || len(codes) != 10 {
		t.Fatalf("confirm = %d %v", st, b)
	}
	// Turning it on ends every other session (a stolen cookie included); the caller continues on a fresh one.
	if st, _ := do(elsewhere, "GET", "/v1/auth/whoami", nil); st != 401 {
		t.Fatalf("another session survived turning two-step sign-in on: %d", st)
	}
	if st, _ := do(cl, "GET", "/v1/auth/whoami", nil); st != 200 {
		t.Fatalf("the caller's renewed session = %d", st)
	}

	// The pending state opens nothing: not whoami, not the step-up, not the account routes.
	other := client()
	if st, b := do(other, "POST", "/v1/auth/login", creds); st != 200 || b["second_step"] != "code" {
		t.Fatalf("password step with two-step on = %d %v", st, b)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/auth/whoami"}, {"GET", "/v1/auth/mfa"}, {"POST", "/v1/auth/reauth"}, {"POST", "/v1/auth/mfa/disable"},
	} {
		if st, _ := do(other, c.method, c.path, map[string]string{"password": "correct-horse-battery", "code": codes[0].(string)}); st != 401 {
			t.Fatalf("%s %s with only the password step = %d", c.method, c.path, st)
		}
	}
	if st, _ := do(other, "POST", "/v1/auth/login/enrol", nil); st != 404 && st != 405 {
		t.Fatalf("the removed enrol step still answers: %d", st)
	}
	if st, b := do(other, "POST", "/v1/auth/login/code", map[string]string{"code": totp.Code(secret, step+1)}); st != 200 || b["operator_id"] != id {
		t.Fatalf("code step = %d %v", st, b)
	}

	// Off: the step-up is not enough on its own, and a code is not enough without it.
	if st, b := do(other, "POST", "/v1/auth/mfa/disable", map[string]string{"code": codes[0].(string)}); st != 403 || b["error"] != "reauth_required" {
		t.Fatalf("off without the step-up = %d %v", st, b)
	}
	if st, _ := do(other, "POST", "/v1/auth/reauth", map[string]string{"password": "correct-horse-battery"}); st != 200 {
		t.Fatalf("step-up = %d", st)
	}
	if st, b := do(other, "POST", "/v1/auth/mfa/disable", map[string]string{}); st != 400 || b["error"] != "code_required" {
		t.Fatalf("off without a code = %d %v", st, b)
	}
	third := client()
	if st, _ := do(third, "POST", "/v1/auth/login", creds); st != 200 {
		t.Fatalf("third sign-in password step = %d", st)
	}
	if st, _ := do(third, "POST", "/v1/auth/login/code", map[string]string{"code": codes[1].(string)}); st != 200 {
		t.Fatalf("third sign-in with a recovery code = %d", st)
	}
	if st, b := do(other, "POST", "/v1/auth/mfa/disable", map[string]string{"code": codes[0].(string)}); st != 200 || b["enrolled"] != false {
		t.Fatalf("off = %d %v", st, b)
	}
	if st, _ := do(third, "GET", "/v1/auth/whoami", nil); st != 401 {
		t.Fatalf("another session survived turning two-step sign-in off: %d", st)
	}
	if st, _ := do(other, "GET", "/v1/auth/whoami", nil); st != 200 {
		t.Fatalf("the caller's renewed session after turning it off = %d", st)
	}
	if st, b := do(client(), "POST", "/v1/auth/login", creds); st != 200 || b["second_step"] != "none" {
		t.Fatalf("after turning it off = %d %v", st, b)
	}
}

// testSessions stands in for the Redis session store, counting the sessions opened.
func testSessions(n *int) func(context.Context, auth.Session) (*auth.Session, error) {
	return func(_ context.Context, s auth.Session) (*auth.Session, error) {
		*n++
		s.Token = "tok-" + strings.Repeat("x", *n)
		s.CreatedAt, s.LastSeenAt, s.ExpiresAt = time.Now(), time.Now(), time.Now().Add(12*time.Hour)
		return &s, nil
	}
}

type noLimit struct{}

func (noLimit) Incr(context.Context, string, time.Duration) (int64, error) { return 1, nil }
