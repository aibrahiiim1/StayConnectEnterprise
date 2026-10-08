package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// THE REMEMBERED DEVICE, END TO END AGAINST THE DATABASE (contract §2.3). Needs PHASE3_TEST_DSN; skipped otherwise.
//
// What is pinned: a credential is bound to ONE device; presenting it from another device refuses AND revokes
// it; the greeting names the Client only by a masked factor; sign-out forgets; a site that remembers for 0
// days issues nothing.

func TestIntegration_RememberedDeviceIsBoundToOneDevice(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const tenant, site, appl = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"
	_, _ = pool.Exec(ctx, `TRUNCATE iam_v2.principal_device_credentials, iam_v2.auth_contexts, iam_v2.devices, iam_v2.guest_principal_identities, iam_v2.guest_principals CASCADE`)

	var pid, dev string
	if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, tenant).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO iam_v2.guest_principal_identities (tenant_id, guest_principal_id, factor_type, factor_issuer, factor_value_norm, verified_at, attrs)
		VALUES ($1,$2,'EMAIL','','alice@example.com',now(),'{"source":"otp"}')`, tenant, pid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,'02:00:00:00:00:aa') RETURNING id::text`, tenant, site, appl).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	days := 30
	s := &server{db: pool, tenID: tenant, siteID: site, resumeKey: key, resumeLimiter: &recoveryLimiter{hits: map[string][]time.Time{}},
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return &tenantcfg.AuthMethods{Email: &tenantcfg.AuthMethod{Enabled: true}, Portal: &tenantcfg.PortalConfig{RememberDeviceDays: &days}}, nil
		}}

	tok := s.issueResumeCredential(ctx, pid, dev, iamv2.MethodOTP)
	if tok == "" {
		t.Fatal("a verified sign-in must earn a credential")
	}
	var stored int
	var expiresIn time.Duration
	if err := pool.QueryRow(ctx, `SELECT count(*), max(expires_at) - now() FROM iam_v2.principal_device_credentials WHERE guest_principal_id=$1::uuid AND revoked_at IS NULL`, pid).Scan(&stored, &expiresIn); err != nil {
		t.Fatal(err)
	}
	if stored != 1 || expiresIn < 29*24*time.Hour || expiresIn > 31*24*time.Hour {
		t.Fatalf("one live credential for the site's period, got %d expiring in %v", stored, expiresIn)
	}
	// Only the HMAC is stored: the token itself appears nowhere.
	var leaked bool
	_ = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam_v2.principal_device_credentials WHERE encode(secret_hmac,'base64') = $1 OR key_id = $1)`, tok).Scan(&leaked)
	if leaked {
		t.Fatal("the plaintext token must never be stored")
	}

	peek := func(mac string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"ip": "10.50.0.9", "mac": mac, "resume_token": tok})
		rec := httptest.NewRecorder()
		s.resumePeek(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/resume-peek", bytes.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := peek("02:00:00:00:00:aa"); out["ok"] != true || out["identity_label"] != "a•••@example.com" || out["method"] != "OTP" {
		t.Fatalf("the right device is greeted by a masked factor: %v", out)
	}
	// Presented from another device: refused, and the credential is gone for the right device too.
	if out := peek("02:00:00:00:00:bb"); out["ok"] != false || out["reason"] != "other_device" {
		t.Fatalf("another device must be refused: %v", out)
	}
	if out := peek("02:00:00:00:00:aa"); out["ok"] != false || out["reason"] != "unknown" {
		t.Fatalf("a credential seen on another device must be revoked: %v", out)
	}
	// A fresh credential replaces the old one (one live per device), and sign-out forgets it.
	tok = s.issueResumeCredential(ctx, pid, dev, iamv2.MethodOTP)
	if out := peek("02:00:00:00:00:aa"); out["ok"] != true {
		t.Fatalf("a fresh credential works: %v", out)
	}
	body, _ := json.Marshal(map[string]string{"resume_token": tok})
	rec := httptest.NewRecorder()
	s.resumeRevoke(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/resume-revoke", bytes.NewReader(body)))
	if out := peek("02:00:00:00:00:aa"); out["ok"] != false {
		t.Fatalf("sign-out must forget the device: %v", out)
	}
	// The method switched off: a remembered device is not let in either.
	tok = s.issueResumeCredential(ctx, pid, dev, iamv2.MethodOTP)
	s.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{Portal: &tenantcfg.PortalConfig{RememberDeviceDays: &days}}, nil
	}
	if out := peek("02:00:00:00:00:aa"); out["ok"] != false || out["reason"] != "method_off" {
		t.Fatalf("a switched-off method must not resume: %v", out)
	}
	// Remember for 0 days: nothing is issued.
	zero := 0
	s.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{Email: &tenantcfg.AuthMethod{Enabled: true}, Portal: &tenantcfg.PortalConfig{RememberDeviceDays: &zero}}, nil
	}
	if tok := s.issueResumeCredential(ctx, pid, dev, iamv2.MethodOTP); tok != "" {
		t.Fatal("a site that does not remember devices must issue nothing")
	}
	// A voucher or account sign-in never earns one.
	if tok := s.issueResumeCredential(ctx, pid, dev, iamv2.MethodVoucher); tok != "" {
		t.Fatal("only a verified factor earns a credential")
	}
}
