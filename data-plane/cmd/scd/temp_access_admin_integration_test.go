package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
)

// STAFF HELP FOR SMS / WHATSAPP CODES AGAINST THE DATABASE (temp_access_admin.go, migrations 0113/0114).
// Needs PHASE3_TEST_DSN; skipped otherwise. The temporary-internet window and its "clear" are removed with the
// email code, and so are their tests.

func adminCall(t *testing.T, s *server, method, path, body string) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	s.tempAccessAdminRoutes(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func codeAdminFixture(t *testing.T) (*server, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const tenant, site = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	// The shared integration tenant and site, created if this suite is the first to need them (auth_otps has a
	// tenant foreign key). Idempotent, and never removed: other suites in the same database use them too.
	_, _ = pool.Exec(context.Background(), `INSERT INTO public.tenants (id, slug, name) VALUES ($1::uuid, $2, 'integration')
	     ON CONFLICT DO NOTHING`, tenant, tenant)
	_, _ = pool.Exec(context.Background(), `INSERT INTO public.sites (id, tenant_id, code, name) VALUES ($2::uuid, $1::uuid, $3, 'integration')
	     ON CONFLICT DO NOTHING`, tenant, site, site)
	return &server{db: pool, tenID: tenant, siteID: site}, pool
}

func TestIntegration_ACodeCopyIsReadableOnlyWhileTheCodeIsLive(t *testing.T) {
	s, pool := codeAdminFixture(t)
	ctx := context.Background()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	k, err := sealbox.NewKey(otpEscrowDomain, raw)
	if err != nil {
		t.Fatal(err)
	}
	s.otpEscrowKey = k
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO public.auth_otps (tenant_id, channel, destination, code_hash, expires_at)
	     VALUES ($1,'sms','+201000000917','x', now() + interval '10 minutes') RETURNING id::text`, s.tenID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM iam_v2.otp_code_reveals WHERE challenge_id=$1::uuid`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM iam_v2.otp_code_escrow WHERE challenge_id=$1::uuid`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.auth_otps WHERE id=$1::uuid`, id)
	})
	var expires = mustTime(t, pool, id)
	s.escrowOTPCode(ctx, id, "482913", expires)

	var stored []byte
	_ = pool.QueryRow(ctx, `SELECT ciphertext FROM iam_v2.otp_code_escrow WHERE challenge_id=$1::uuid`, id).Scan(&stored)
	if len(stored) == 0 || strings.Contains(string(stored), "482913") {
		t.Fatal("the copy must be stored sealed")
	}
	if code, out := adminCall(t, s, http.MethodPost, "/v1/admin/otp/"+id+"/reveal", `{"operator":"desk"}`); code != 200 || out["code"] != "482913" {
		t.Fatalf("reveal a live code: %d %v", code, out)
	}
	var by string
	if err := pool.QueryRow(ctx, `SELECT revealed_by FROM iam_v2.otp_code_reveals WHERE challenge_id=$1::uuid`, id).Scan(&by); err != nil || by != "desk" {
		t.Fatalf("the reveal must be recorded before the code leaves (0114): %q %v", by, err)
	}
	if code, _ := adminCall(t, s, http.MethodPost, "/v1/admin/otp/"+id+"/reveal", `{}`); code != 400 {
		t.Fatal("a reveal needs an operator")
	}
	// The list says a code can be read, and never carries it.
	code, out := adminCall(t, s, http.MethodGet, "/v1/admin/temporary-access", "")
	if code != 200 || strings.Contains(mustJSON(out), "482913") {
		t.Fatalf("list: %d", code)
	}
	found := false
	for _, r := range out["requests"].([]any) {
		m := r.(map[string]any)
		if m["challenge_id"] == id {
			found = m["code_available"] == true && m["state"] == "PENDING"
		}
	}
	if !found {
		t.Fatal("the list must show the pending code as readable")
	}
	// USED: no longer readable, and the prune removes the copy.
	_, _ = pool.Exec(ctx, `UPDATE public.auth_otps SET consumed_at=now() WHERE id=$1::uuid`, id)
	if code, out := adminCall(t, s, http.MethodPost, "/v1/admin/otp/"+id+"/reveal", `{"operator":"desk"}`); code != 404 || out["code"] != nil {
		t.Fatalf("a used code must not be revealed: %d %v", code, out)
	}
	s.pruneOTPEscrow(ctx)
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.otp_code_escrow WHERE challenge_id=$1::uuid`, id).Scan(&left)
	if left != 0 {
		t.Fatal("the copy of a used code must be pruned")
	}
	// Without the key, nothing is sealed and a reveal says so.
	s.otpEscrowKey = sealbox.Key{}
	if code, out := adminCall(t, s, http.MethodPost, "/v1/admin/otp/"+id+"/reveal", `{"operator":"desk"}`); code != 503 || out["error"] != "CODE_COPY_UNAVAILABLE" {
		t.Fatalf("no key: %d %v", code, out)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func mustTime(t *testing.T, pool *pgxpool.Pool, id string) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(context.Background(), `SELECT expires_at FROM public.auth_otps WHERE id=$1::uuid`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// AN EXISTING CLIENT'S CODE IS NEVER SHOWN (contract §6.7): a staff-shown code could not sign them in, and the list
// says so, so the desk lets the device try again instead.
func TestIntegration_AnExistingClientsCodeIsNeverShown(t *testing.T) {
	s, pool := codeAdminFixture(t)
	ctx := context.Background()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	s.otpEscrowKey, _ = sealbox.NewKey(otpEscrowDomain, raw)
	const addr = "+201000000918"
	var pid, id string
	if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, s.tenID).Scan(&pid); err != nil {
		t.Skipf("cannot seed a principal here: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO iam_v2.guest_principal_identities (tenant_id, guest_principal_id, factor_type, factor_value_norm)
	     VALUES ($1,$2::uuid,'PHONE',$3)`, s.tenID, pid, addr); err != nil {
		t.Skipf("cannot seed an identity here: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO public.auth_otps (tenant_id, channel, destination, code_hash, expires_at)
	     VALUES ($1,'sms',$2,'x', now() + interval '10 minutes') RETURNING id::text`, s.tenID, addr).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM iam_v2.otp_code_reveals WHERE challenge_id=$1::uuid`, id)
		_, _ = pool.Exec(bg, `DELETE FROM iam_v2.otp_code_escrow WHERE challenge_id=$1::uuid`, id)
		_, _ = pool.Exec(bg, `DELETE FROM public.auth_otps WHERE id=$1::uuid`, id)
		_, _ = pool.Exec(bg, `DELETE FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1::uuid`, pid)
		_, _ = pool.Exec(bg, `DELETE FROM iam_v2.guest_principals WHERE id=$1::uuid`, pid)
	})
	s.escrowOTPCode(ctx, id, "771204", mustTime(t, pool, id))
	if code, out := adminCall(t, s, http.MethodPost, "/v1/admin/otp/"+id+"/reveal", `{"operator":"desk"}`); code != 409 || out["error"] != "EXISTING_CLIENT" || out["code"] != nil {
		t.Fatalf("an existing client's code must not be shown: %d %v", code, out)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.otp_code_reveals WHERE challenge_id=$1::uuid`, id).Scan(&n)
	if n != 0 {
		t.Fatal("a refused reveal records nothing")
	}
	_, out := adminCall(t, s, http.MethodGet, "/v1/admin/temporary-access", "")
	for _, r := range out["requests"].([]any) {
		m := r.(map[string]any)
		if m["challenge_id"] == id && (m["existing_client"] != true || m["code_available"] != false) {
			t.Fatalf("the list must flag the existing client and offer no code: %v", m)
		}
	}
}

// A code's device binding older than a day is removed by the minute sweep, not only when the next code is sent,
// so a site where nobody asks for a code does not keep every device MAC it bound (GDPR plan C1 gap).
func TestIntegration_OldDeviceBindingsAreSweptWithoutANewCode(t *testing.T) {
	s, pool := codeAdminFixture(t)
	ctx := context.Background()
	var old, fresh string
	if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.otp_device_bindings (challenge_id, tenant_id, device_mac, issued_at)
	     VALUES (gen_random_uuid(), $1::uuid, '02:00:00:00:b1:01', now() - interval '2 days') RETURNING challenge_id::text`, s.tenID).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.otp_device_bindings (challenge_id, tenant_id, device_mac)
	     VALUES (gen_random_uuid(), $1::uuid, '02:00:00:00:b1:02') RETURNING challenge_id::text`, s.tenID).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM iam_v2.otp_device_bindings WHERE challenge_id = ANY($1::uuid[])`, []string{old, fresh})
	})
	s.pruneOTPEscrow(ctx)
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.otp_device_bindings WHERE challenge_id = $1::uuid`, old).Scan(&n)
	if n != 0 {
		t.Fatal("a two-day-old device binding survived the sweep")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.otp_device_bindings WHERE challenge_id = $1::uuid`, fresh).Scan(&n)
	if n != 1 {
		t.Fatal("a live device binding was swept")
	}
}
