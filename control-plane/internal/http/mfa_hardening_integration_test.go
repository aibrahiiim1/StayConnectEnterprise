package http

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

// enrolledOperator returns the id, secret, step used by the confirming code, and recovery codes.
func enrolledOperator(t *testing.T, mfa *auth.MFA, email string) (string, []byte, int64, []string) {
	t.Helper()
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct-horse-battery")
	var id string
	if err := mfa.DB.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ($1,$2,'active') RETURNING id::text`,
		email, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	b32, _, err := mfa.Enrol(ctx, id, email)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
	if err != nil {
		t.Fatal(err)
	}
	step := totp.Step(time.Now()) - 1
	codes, ok, _, err := mfa.Confirm(ctx, id, totp.Code(secret, step))
	if err != nil || !ok {
		t.Fatalf("confirm = %v %v", ok, err)
	}
	return id, secret, step, codes
}

func newTestMFA(t *testing.T) *auth.MFA {
	t.Helper()
	pool := testdb.New(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	return mfa
}

func TestNewRecoveryCodesNeedACurrentAuthenticatorCode(t *testing.T) {
	mfa := newTestMFA(t)
	ctx := context.Background()
	id, secret, used, oldCodes := enrolledOperator(t, mfa, "rc@central.test")
	d := authDeps{Repo: &auth.Repo{DB: mfa.DB}, DB: mfa.DB, Failures: ratebudget.New(50, 300, time.Minute), MFA: mfa}
	sess := &auth.Session{OperatorID: id, Email: "rc@central.test"}
	call := func(body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/mfa/recovery-codes", bytes.NewReader(raw))
		req.RemoteAddr = "203.0.113.50:40000"
		req = req.WithContext(auth.ContextWithSession(req.Context(), sess))
		rec := httptest.NewRecorder()
		d.mfaRecoveryCodes(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	failed := func() (n int) {
		_ = mfa.DB.QueryRow(ctx, `SELECT failed_codes FROM operator_mfa WHERE operator_id=$1`, id).Scan(&n)
		return n
	}

	if code, b := call(map[string]any{}); code != 400 || b["error"] != "code_required" {
		t.Fatalf("no code = %d %v", code, b)
	}
	if code, b := call(map[string]string{"code": oldCodes[0]}); code != 400 || b["error"] != "code_required" {
		t.Fatalf("a recovery code = %d %v", code, b)
	}
	if failed() != 0 {
		t.Fatal("a malformed code was counted as a wrong code")
	}
	if code, b := call(map[string]string{"code": "000000"}); code != 400 || b["error"] != "wrong_code" {
		t.Fatalf("a wrong code = %d %v", code, b)
	}
	if failed() != 1 {
		t.Fatalf("a wrong code was not counted (failed_codes %d)", failed())
	}
	if code, b := call(map[string]string{"code": totp.Code(secret, used)}); code != 400 || b["error"] != "wrong_code" {
		t.Fatalf("a replayed code = %d %v", code, b)
	}
	if failed() != 2 {
		t.Fatalf("a replayed code was not counted (failed_codes %d)", failed())
	}
	now := totp.Code(secret, totp.Step(time.Now()))
	code, b := call(map[string]string{"code": now})
	fresh, _ := b["recovery_codes"].([]any)
	if code != 200 || len(fresh) != 10 {
		t.Fatalf("the current code = %d %v", code, b)
	}
	if failed() != 0 {
		t.Fatal("a right code did not clear the count")
	}
	if code, _ := call(map[string]string{"code": now}); code != 400 {
		t.Fatalf("the same code twice = %d", code)
	}
	if _, _, ok, _, err := mfa.Verify(ctx, id, oldCodes[1]); ok || err != nil {
		t.Fatalf("an old recovery code still works: %v %v", ok, err)
	}
	var replaced int
	_ = mfa.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='auth.mfa_recovery_codes_replaced' AND target_id=$1`, id).Scan(&replaced)
	if replaced != 1 {
		t.Fatalf("replacement audited %d times", replaced)
	}

	if _, err := mfa.DB.Exec(ctx, `UPDATE central_security_settings SET signin_lock_failures = 3 WHERE id`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = call(map[string]string{"code": "111111"})
	}
	if code, b := call(map[string]string{"code": totp.Code(secret, totp.Step(time.Now())+1)}); code != http.StatusTooManyRequests {
		t.Fatalf("the right code during a lockout = %d %v", code, b)
	}
}

func TestParallelWrongCodesCannotOutrunTheLockout(t *testing.T) {
	mfa := newTestMFA(t)
	ctx := context.Background()
	id, _, _, _ := enrolledOperator(t, mfa, "burst@central.test")
	if _, err := mfa.DB.Exec(ctx, `UPDATE central_security_settings SET signin_lock_failures = 5 WHERE id`); err != nil {
		t.Fatal(err)
	}
	const n = 30
	var wg sync.WaitGroup
	var mu sync.Mutex
	checked, locked := 0, 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			wrong := "00000" + string(rune('0'+i%10))
			_, _, ok, _, err := mfa.Verify(ctx, id, wrong)
			mu.Lock()
			defer mu.Unlock()
			var le auth.ErrMFALocked
			switch {
			case errors.As(err, &le):
				locked++
			case err != nil:
				t.Errorf("verify: %v", err)
			case ok:
				t.Error("a wrong code was accepted")
			default:
				checked++
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if checked > 5 || checked+locked != n {
		t.Fatalf("%d wrong codes were checked before the lock (limit 5), %d refused as locked", checked, locked)
	}
}

func TestRecoveryCodeWithInnerWhitespace(t *testing.T) {
	mfa := newTestMFA(t)
	id, _, _, codes := enrolledOperator(t, mfa, "ws@central.test")
	c := strings.ToLower(strings.ReplaceAll(codes[0], "-", " \t"))
	c = c[:2] + "\n" + c[2:]
	method, _, ok, _, err := mfa.Verify(context.Background(), id, c)
	if err != nil || !ok || method != "recovery_code" {
		t.Fatalf("recovery code %q = %v %v %v", c, method, ok, err)
	}
}
