//go:build integration

package main

// The Admin Console authenticator against the full appliance schema as svc_scd: sealed, operator-bound secret, one-time codes,
// single-use recovery codes stored as MACs, reset, and no route open to the guest portal.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/totp"
)

func TestOperatorMFARoutesAreEdgedOnly(t *testing.T) {
	for _, p := range []string{"enrol", "confirm", "verify", "recovery-codes", "reset"} {
		if classifyRoute("/v1/admin/operator-mfa/"+p) != adminRoute {
			t.Fatalf("/v1/admin/operator-mfa/%s is reachable by the guest portal", p)
		}
	}
}

func TestIntegrationOperatorMFA_SealedSingleUseAndReset(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE svc_scd`)
		return err
	}
	svc, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	s := &server{db: svc}
	k, _ := sealbox.NewKey(operatorMFADomain, raw)
	s.operatorMFA = operatorMFAKeys{seal: k, mac: append([]byte(nil), raw...)}

	var op, other string
	for _, dst := range []*string{&op, &other} {
		if err := owner.QueryRow(ctx, `INSERT INTO operators (email, display_name, password_hash, status)
			VALUES ('mfa-'||gen_random_uuid()||'@test.local','T','x','active') RETURNING id::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = owner.Exec(ctx, `DELETE FROM operators WHERE id = ANY($1)`, []string{op, other}) })

	post := func(h http.HandlerFunc, body map[string]string) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// A set-up started twice (two-step sign-in is turned on from the account page, D68): the second replaces the
	// unconfirmed first, whose codes then confirm nothing.
	st, abandoned := post(s.operatorMFAEnrol, map[string]string{"operator_id": op, "account": "ada@hotel.example"})
	if st != 200 {
		t.Fatalf("first enrol = %d %v", st, abandoned)
	}
	st, e := post(s.operatorMFAEnrol, map[string]string{"operator_id": op, "account": "ada@hotel.example"})
	if st != 200 || e["secret"] == abandoned["secret"] {
		t.Fatalf("enrol again = %d %v", st, e)
	}
	var rows int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM public.operator_mfa WHERE operator_id=$1 AND confirmed_at IS NULL`, op).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d unconfirmed authenticators after two set-ups", rows)
	}
	old, _ := totpSecret(abandoned["secret"].(string))
	if st, c := post(s.operatorMFAConfirm, map[string]string{"operator_id": op, "code": totp.Code(old, totp.Step(time.Now()))}); st != 200 || c["confirmed"] != false {
		t.Fatalf("the replaced set-up's code = %d %v", st, c)
	}
	u, _ := url.Parse(e["otpauth_uri"].(string))
	secretB32 := u.Query().Get("secret")
	if secretB32 != e["secret"] {
		t.Fatal("the QR code and the typed secret differ")
	}
	secret, _ := totpSecret(secretB32)
	var ct []byte
	_ = owner.QueryRow(ctx, `SELECT ciphertext FROM public.operator_mfa WHERE operator_id=$1`, op).Scan(&ct)
	if len(ct) == 0 || bytes.Contains(ct, secret) {
		t.Fatal("the secret is stored in the clear")
	}
	if _, _, err := func() ([]byte, *int64, error) {
		var out []byte
		var last *int64
		err := pgx.BeginFunc(ctx, svc, func(tx pgx.Tx) error {
			var e2 error
			if _, e2 = tx.Exec(ctx, `INSERT INTO public.operator_mfa (operator_id,key_id,nonce,ciphertext,cipher_version)
				SELECT $2,key_id,nonce,ciphertext,cipher_version FROM public.operator_mfa WHERE operator_id=$1`, op, other); e2 != nil {
				return e2
			}
			out, last, e2 = s.openOperatorSecret(ctx, tx, other, false)
			return e2
		})
		return out, last, err
	}(); err == nil {
		t.Fatal("a sealed secret opened for another operator")
	}

	if st, c := post(s.operatorMFAConfirm, map[string]string{"operator_id": op, "code": "000000"}); st != 200 || c["confirmed"] != false {
		t.Fatalf("wrong confirm = %d %v", st, c)
	}
	now := time.Now()
	st, c := post(s.operatorMFAConfirm, map[string]string{"operator_id": op, "code": totp.Code(secret, totp.Step(now))})
	codes, _ := c["recovery_codes"].([]any)
	if st != 200 || c["confirmed"] != true || len(codes) != 10 {
		t.Fatalf("confirm = %d %v", st, c)
	}
	var stored string
	_ = owner.QueryRow(ctx, `SELECT string_agg(code_mac, ',') FROM public.operator_mfa_recovery_codes WHERE operator_id=$1`, op).Scan(&stored)
	if strings.Contains(strings.ToUpper(stored), strings.ReplaceAll(codes[0].(string), "-", "")) {
		t.Fatal("a recovery code is stored in the clear")
	}
	if st, _ := post(s.operatorMFAEnrol, map[string]string{"operator_id": op, "account": "x"}); st != http.StatusConflict {
		t.Fatalf("a confirmed authenticator was replaced: %d", st)
	}

	if st, v := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": totp.Code(secret, totp.Step(now))}); st != 200 || v["ok"] != false {
		t.Fatalf("a used code was accepted again: %d %v", st, v)
	}
	next := totp.Code(secret, totp.Step(now)+1)
	if st, v := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": next}); st != 200 || v["ok"] != true || v["method"] != "totp" {
		t.Fatalf("next code = %d %v", st, v)
	}
	if _, v := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": next}); v["ok"] != false {
		t.Fatal("the same code was accepted twice")
	}

	rc := strings.ToLower(codes[3].(string))
	if _, v := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": rc}); v["ok"] != true || v["recovery_codes_left"] != float64(9) {
		t.Fatalf("recovery code = %v", v)
	}
	if _, v := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": rc}); v["ok"] != false {
		t.Fatal("a recovery code worked twice")
	}
	if _, v := post(s.operatorMFAVerify, map[string]string{"operator_id": other, "code": codes[4].(string)}); v["ok"] == true {
		t.Fatal("a recovery code worked for another operator")
	}
	// A new set needs a current authenticator code; the record is wound back to leave one usable code (now-1).
	if _, err := owner.Exec(ctx, `UPDATE public.operator_mfa SET last_used_step = $2 WHERE operator_id = $1`, op, totp.Step(now)-2); err != nil {
		t.Fatal(err)
	}
	if _, v := post(s.operatorMFARecoveryCodes, map[string]string{"operator_id": op}); v["ok"] != false || v["recovery_codes"] != nil {
		t.Fatalf("new codes without a code = %v", v)
	}
	if _, v := post(s.operatorMFARecoveryCodes, map[string]string{"operator_id": op, "code": codes[6].(string)}); v["ok"] != false {
		t.Fatalf("new codes with a recovery code = %v", v)
	}
	var unspent int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM public.operator_mfa_recovery_codes WHERE operator_id=$1 AND used_at IS NULL`, op).Scan(&unspent)
	if unspent != 9 {
		t.Fatalf("trying a recovery code for new codes spent it: %d left", unspent)
	}
	if _, v := post(s.operatorMFARecoveryCodes, map[string]string{"operator_id": op, "code": "000000"}); v["ok"] != false && totp.Code(secret, totp.Step(time.Now())) != "000000" {
		t.Fatalf("new codes with a wrong code = %v", v)
	}
	prev := totp.Code(secret, totp.Step(now)-1)
	_, nc := post(s.operatorMFARecoveryCodes, map[string]string{"operator_id": op, "code": prev})
	if fresh, _ := nc["recovery_codes"].([]any); nc["ok"] != true || len(fresh) != 10 {
		t.Fatalf("new codes = %v", nc)
	}
	if _, v := post(s.operatorMFARecoveryCodes, map[string]string{"operator_id": op, "code": prev}); v["ok"] != false {
		t.Fatalf("a used code made new codes again: %v", v)
	}
	if _, v := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": codes[5].(string)}); v["ok"] != false {
		t.Fatal("an old recovery code survived a new set")
	}

	if st, r := post(s.operatorMFAReset, map[string]string{"operator_id": op}); st != 200 || r["removed"] != true {
		t.Fatalf("reset = %d %v", st, r)
	}
	var n int
	_ = owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.operator_mfa WHERE operator_id=$1)
		+ (SELECT count(*) FROM public.operator_mfa_recovery_codes WHERE operator_id=$1)`, op).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after a reset", n)
	}
	if st, _ := post(s.operatorMFAVerify, map[string]string{"operator_id": op, "code": next}); st != http.StatusConflict {
		t.Fatalf("verify after reset = %d, want 409 not_enrolled", st)
	}
	// Without the key nothing is set up or checked: the code step fails closed in edged.
	s.operatorMFA = operatorMFAKeys{}
	if st, _ := post(s.operatorMFAEnrol, map[string]string{"operator_id": op, "account": "x"}); st != http.StatusServiceUnavailable {
		t.Fatalf("enrol without the key = %d", st)
	}
}

func totpSecret(b32 string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
}
