package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// STAFF HELP AND THE RETRY WAIT AGAINST THE DATABASE (temp_access_admin.go, verify_access.go, migration 0113).
// Needs PHASE3_TEST_DSN on a database with 0111 and 0113; skipped otherwise.

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

func TestIntegration_ADeviceWaitsBeforeAnotherWindowExceptAfterChangingAddress(t *testing.T) {
	s, _, pool := verifyFixture(t, nil) // default wait: 30 minutes
	ctx := context.Background()
	mac, ip := "02:00:00:91:00:10", net.ParseIP("10.91.0.50")
	if _, ok := s.grantVerifyAccess(ctx, mac, ip, "", "w1@example.com"); !ok {
		t.Fatal("first window")
	}
	_, _ = pool.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET ended_at=now(), end_reason='EXPIRED' WHERE tenant_id=$1 AND ended_at IS NULL`, s.tenID)
	if _, ok, why := s.grantVerifyAccessDetail(ctx, mac, ip, "", "w1@example.com"); ok || why == nil || why.Reason != "device_retry_wait" || why.RetryAfterSeconds < 1790 || why.RetryAfterSeconds > 1801 {
		t.Fatalf("a window that just expired must be followed by the wait, said to the client: ok=%v %+v", ok, why)
	}
	// 31 minutes later the device may have another.
	_, _ = pool.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET started_at=started_at - interval '41 minutes', ended_at=now() - interval '31 minutes' WHERE tenant_id=$1`, s.tenID)
	if _, ok := s.grantVerifyAccess(ctx, mac, ip, "", "w1@example.com"); !ok {
		t.Fatal("after the wait the device gets another window")
	}
	// CHOOSING A DIFFERENT ADDRESS does not start the wait: a mistyped address is not punished.
	s.endVerifyAccess(ctx, mac, "CANCELLED")
	if _, ok := s.grantVerifyAccess(ctx, mac, ip, "", "w2@example.com"); !ok {
		t.Fatal("a window ended by changing address must not start the wait")
	}
}

func TestIntegration_ClearingLetsOneDeviceTryAgainAndKeepsTheRecord(t *testing.T) {
	one, zero := 1, 0
	s, _, pool := verifyFixture(t, &tenantcfg.TemporaryAccess{PerDeviceDaily: &one, RetryAfterMinutes: &zero})
	ctx := context.Background()
	mac, ip := "02:00:00:91:00:11", net.ParseIP("10.91.0.51")
	if _, ok := s.grantVerifyAccess(ctx, mac, ip, "", "c1@example.com"); !ok {
		t.Fatal("first window")
	}
	// A live window is never cleared.
	if code, out := adminCall(t, s, http.MethodPost, "/v1/admin/temporary-access/clear", `{"device_mac":"`+mac+`","operator":"desk","reason":"t"}`); code != 200 || out["cleared_windows"].(float64) != 0 || out["window_live"] != true {
		t.Fatalf("clearing must leave a live window alone: %d %v", code, out)
	}
	s.endVerifyAccess(ctx, mac, "EXPIRED")
	if _, ok := s.grantVerifyAccess(ctx, mac, ip, "", "c1@example.com"); ok {
		t.Fatal("the per-device limit must refuse a second window")
	}
	if code, _ := adminCall(t, s, http.MethodPost, "/v1/admin/temporary-access/clear", `{"device_mac":"`+mac+`","operator":""}`); code != 400 {
		t.Fatal("clearing needs an operator")
	}
	code, out := adminCall(t, s, http.MethodPost, "/v1/admin/temporary-access/clear", `{"device_mac":"`+strings.ToUpper(mac)+`","operator":"desk:alice","reason":"no mail"}`)
	if code != 200 || out["cleared_windows"].(float64) != 1 {
		t.Fatalf("clear: %d %v", code, out)
	}
	if _, ok := s.grantVerifyAccess(ctx, mac, ip, "", "c1@example.com"); !ok {
		t.Fatal("after clearing, the device may try again")
	}
	var rows, cleared int
	var by string
	_ = pool.QueryRow(ctx, `SELECT count(*), count(cleared_at), COALESCE(max(cleared_by),'') FROM iam_v2.verification_access_grants WHERE tenant_id=$1`, s.tenID).Scan(&rows, &cleared, &by)
	if rows != 2 || cleared != 1 || by != "desk:alice" {
		t.Fatalf("the cleared window stays on record, attributed: rows=%d cleared=%d by=%q", rows, cleared, by)
	}
}

func TestIntegration_ACodeCopyIsReadableOnlyWhileTheCodeIsLive(t *testing.T) {
	s, _, pool := verifyFixture(t, nil)
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
	     VALUES ($1,'email','reveal-test@example.com','x', now() + interval '10 minutes') RETURNING id::text`, s.tenID).Scan(&id); err != nil {
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
	s, _, pool := verifyFixture(t, nil)
	ctx := context.Background()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	s.otpEscrowKey, _ = sealbox.NewKey(otpEscrowDomain, raw)
	const addr = "existing-client-reveal@example.com"
	var pid, id string
	if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, s.tenID).Scan(&pid); err != nil {
		t.Skipf("cannot seed a principal here: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO iam_v2.guest_principal_identities (tenant_id, guest_principal_id, factor_type, factor_value_norm)
	     VALUES ($1,$2::uuid,'EMAIL',$3)`, s.tenID, pid, addr); err != nil {
		t.Skipf("cannot seed an identity here: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO public.auth_otps (tenant_id, channel, destination, code_hash, expires_at)
	     VALUES ($1,'email',$2,'x', now() + interval '10 minutes') RETURNING id::text`, s.tenID, "Existing-Client-Reveal@Example.com").Scan(&id); err != nil {
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
