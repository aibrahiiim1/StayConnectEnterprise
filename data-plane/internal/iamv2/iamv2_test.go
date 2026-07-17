package iamv2

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

// ---- unit (no DB) ----------------------------------------------------------

// spyRepo records whether the repository was ever entered.
type spyRepo struct{ calls int }

func (s *spyRepo) WithTx(ctx context.Context, fn func(Tx) error) error {
	s.calls++
	return fn(&spyTx{})
}

type spyTx struct{}

func (spyTx) ResolveVoucherByHMAC(context.Context, string, string, []byte, time.Time) (string, bool, error) {
	return "", false, nil
}
func (spyTx) LookupAccount(context.Context, string, string) (string, string, bool, *time.Time, *time.Time, *time.Time, error) {
	return "", "", false, nil, nil, nil, nil
}
func (spyTx) ResolvePrincipalByIdentity(context.Context, string, string, string, string, time.Time) (string, error) {
	return "", nil
}
func (spyTx) UpsertDevice(context.Context, string, string, string, string, string, string, time.Time) (string, error) {
	return "", nil
}
func (spyTx) CreateAuthContext(context.Context, AuthContextSpec) (string, error) { return "", nil }

func TestConfigValidateFailClosed(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default (all off) must be valid: %v", err)
	}
	// per-method on while master off => misconfiguration
	bad := Config{MasterEnabled: false, Methods: map[Method]bool{MethodOTP: true}}
	if err := bad.Validate(); err == nil {
		t.Fatal("method-on-while-master-off must be rejected")
	}
	// unknown method flag
	if err := (Config{MasterEnabled: true, Methods: map[Method]bool{"BOGUS": true}}).Validate(); err == nil {
		t.Fatal("unknown method flag must be rejected")
	}
}

func TestDisabledNeverInvokesRepository(t *testing.T) {
	repo := &spyRepo{}
	a, err := New(DefaultConfig(), repo, NopObserver{}) // all flags OFF
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Method{MethodVoucher, MethodAccount, MethodOTP, MethodSocial} {
		res, err := a.Authenticate(context.Background(), Request{Method: m, TenantID: "t", SiteID: "s", Username: "u", Secret: "p", FactorType: "EMAIL", FactorValue: "a@x.com"})
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if res.Decision != DecisionDisabled {
			t.Fatalf("%s: expected disabled, got %s", m, res.Decision)
		}
	}
	if repo.calls != 0 {
		t.Fatalf("PRODUCTION-SAFETY VIOLATION: repository invoked %d times while flags OFF", repo.calls)
	}
}

func TestSocialStubRefusedInProduction(t *testing.T) {
	cfg := Config{MasterEnabled: true, Methods: map[Method]bool{MethodSocial: true}, AllowSocialStub: false}
	repo := &spyRepo{}
	a, err := New(cfg, repo, NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Authenticate(context.Background(), Request{Method: MethodSocial, TenantID: "t", Provider: "Stub", FactorIssuer: "iss", FactorValue: "sub"})
	if err != ErrSocialStubRefusedErr {
		t.Fatalf("expected stub refusal error, got %v", err)
	}
	if res.Decision != DecisionDeny || res.Reason != "social_stub_refused" {
		t.Fatalf("expected deny/social_stub_refused, got %+v", res)
	}
	if repo.calls != 0 {
		t.Fatal("stub refusal must occur before any repository call")
	}
}

func TestMasterEnabledRequiresRepo(t *testing.T) {
	cfg := Config{MasterEnabled: true, Methods: map[Method]bool{MethodOTP: true}}
	if _, err := New(cfg, nil, nil); err == nil {
		t.Fatal("master enabled with nil repo must error")
	}
}

func TestRedactScrubsSecrets(t *testing.T) {
	in := "login alice@example.com from aa:bb:cc:dd:ee:ff code 123456"
	out := Redact(in)
	for _, leak := range []string{"alice@example.com", "aa:bb:cc:dd:ee:ff", "123456"} {
		if strings.Contains(out, leak) {
			t.Fatalf("Redact leaked %q: %s", leak, out)
		}
	}
}

// ---- scratch integration (disposable iam_v2 DB) ----------------------------

func argon2idHash(pw string) string {
	salt := []byte("abcdef0123456789")
	key := argon2.IDKey([]byte(pw), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", 64*1024, 1, 4,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func scratchDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("IAMV2_TEST_DSN")
	if dsn == "" {
		t.Skip("IAMV2_TEST_DSN not set; skipping scratch iam_v2 integration")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// clean the tables this test writes
	_, _ = db.Exec(context.Background(), `TRUNCATE iam_v2.auth_contexts, iam_v2.device_network_appearances, iam_v2.devices, iam_v2.guest_access_accounts, iam_v2.guest_principal_identities, iam_v2.guest_principals CASCADE`)
	return db
}

const testTenant = "11111111-1111-1111-1111-111111111111"
const testSite = "22222222-2222-2222-2222-222222222222"
const testGN = "44444444-4444-4444-4444-444444444444"

func seedGuestNetwork(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	_, err := db.Exec(context.Background(),
		`INSERT INTO public.guest_networks
		   (id, tenant_id, site_id, name, parent_interface, bridge_name, gateway_cidr, gateway_ip, subnet_cidr)
		 VALUES ($1,$2,$3,'test-net','ens192','br-guest','10.50.0.1/24','10.50.0.1','10.50.0.0/24')
		 ON CONFLICT (id) DO NOTHING`, testGN, testTenant, testSite)
	if err != nil {
		t.Fatalf("seed guest_network: %v", err)
	}
}

func gnDevice(mac string) DeviceContext {
	return DeviceContext{MAC: mac, ApplianceID: "33333333-3333-3333-3333-333333333333", GuestNetworkID: testGN}
}

func TestAccountScratchIntegration(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	if _, err := db.Exec(ctx,
		`INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, enabled)
		 VALUES ($1,$2,'alice',$3,true)`, testTenant, testSite, argon2idHash("correcthorse")); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	cfg := Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}
	a, _ := New(cfg, NewPgRepository(db), NopObserver{})

	// wrong password -> deny, no auth_context
	res, err := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: testSite, Username: "alice", Secret: "nope", Device: gnDevice("aa:bb:cc:00:11:22")})
	if err != nil || res.Decision != DecisionDeny {
		t.Fatalf("wrong pw: %+v err=%v", res, err)
	}
	// correct password -> allow + auth_context + device
	res, err = a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: testSite, Username: "alice", Secret: "correcthorse", Device: gnDevice("aa:bb:cc:00:11:22")})
	if err != nil {
		t.Fatalf("correct pw err: %v", err)
	}
	if res.Decision != DecisionAllow || res.AuthContextID == "" || res.Subject.GuestAccountID == "" || res.DeviceID == "" {
		t.Fatalf("expected allow with auth_context+device: %+v", res)
	}
	var n int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.auth_contexts WHERE method='ACCOUNT' AND guest_account_id=$1`, res.Subject.GuestAccountID).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 ACCOUNT auth_context, got %d", n)
	}
}

func TestOTPIdentityScratchIntegration(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	cfg := Config{MasterEnabled: true, Methods: map[Method]bool{MethodOTP: true}}
	a, _ := New(cfg, NewPgRepository(db), NopObserver{})
	req := Request{Method: MethodOTP, TenantID: testTenant, SiteID: testSite, FactorType: "EMAIL", FactorValue: "bob@example.com", Device: gnDevice("aa:bb:cc:33:44:55")}
	// first call creates principal+identity
	res, err := a.Authenticate(ctx, req)
	if err != nil || res.Decision != DecisionAllow || res.Subject.PrincipalID == "" {
		t.Fatalf("otp allow: %+v err=%v", res, err)
	}
	first := res.Subject.PrincipalID
	// second call for same identity resolves the SAME principal (idempotent identity)
	res2, err := a.Authenticate(ctx, req)
	if err != nil || res2.Subject.PrincipalID != first {
		t.Fatalf("otp identity not stable: %v %v", res2.Subject.PrincipalID, err)
	}
	var principals int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_principals`).Scan(&principals)
	if principals != 1 {
		t.Fatalf("expected exactly 1 principal for repeat identity, got %d", principals)
	}
}
