package iamv2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- item 1: full voucher PostgreSQL integration test (disposable iam_v2 DB) ----
//
// Proves the Phase 1B voucher credential path end-to-end against real DDL:
//   * the voucher is resolved by its blind-index HMAC (never by plaintext);
//   * a valid UNUSED voucher inside its window yields a VOUCHER auth_context pinned to the
//     correct tenant/site/device/guest-network, and the voucher STAYS UNUSED (Phase 1B does
//     not redeem or grant);
//   * every non-redeemable state/window is denied generically (invalid_code vs not_redeemable);
//   * a wrong tenant/site/HMAC never resolves;
//   * NO redemption, purchase, settlement, entitlement, or session row is ever created.

// testVHMAC is the scratch voucher blind-index: HMAC-SHA256(site-scoped key, code). It matches the
// shape the real scd computes; the test only needs determinism, not the production key.
func testVHMAC(_ context.Context, tenantID, siteID, code string) ([]byte, error) {
	mac := hmac.New(sha256.New, []byte("scratch-voucher-key|"+tenantID+"|"+siteID))
	mac.Write([]byte(code))
	return mac.Sum(nil), nil
}

// seedVoucherChain truncates and rebuilds the minimal plan/package/key-generation ancestry the
// vouchers FK graph requires, so the test owns a clean commerce subtree.
func seedVoucherChain(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	// Clear the whole commerce/session subtree the test asserts on (disposable DB).
	if _, err := db.Exec(ctx, `TRUNCATE
		iam_v2.sessions, iam_v2.entitlement_devices, iam_v2.entitlement_adjustments,
		iam_v2.entitlements, iam_v2.settlements, iam_v2.purchases, iam_v2.offer_quotes,
		iam_v2.vouchers, iam_v2.voucher_code_key_generations,
		iam_v2.internet_package_revisions, iam_v2.internet_packages,
		iam_v2.service_plan_revisions, iam_v2.service_plans CASCADE`); err != nil {
		t.Fatalf("truncate commerce subtree: %v", err)
	}
	stmts := []string{
		`INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code) VALUES ('bbbb0000-0000-0000-0000-000000000001',$1,$2,'PLAN1')`,
		`INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		 VALUES ('bbbb0000-0000-0000-0000-0000000000d1',$1,$2,'bbbb0000-0000-0000-0000-000000000001',1,'plan',2,'VALIDITY_WINDOW',1000000)`,
		`INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code) VALUES ('cccc0000-0000-0000-0000-000000000001',$1,$2,'PKG1')`,
		`INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
		 VALUES ('cccc0000-0000-0000-0000-0000000000d1',$1,$2,'cccc0000-0000-0000-0000-000000000001',1,'bbbb0000-0000-0000-0000-0000000000d1','GENERAL',100,'USD',2)`,
		`INSERT INTO iam_v2.voucher_code_key_generations(id,tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		 VALUES ('ffff0000-0000-0000-0000-000000000001',$1,$2,1,'\x00','{}','0a000000-0000-0000-0000-000000000001')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(ctx, s, testTenant, testSite); err != nil {
			t.Fatalf("seed chain: %v", err)
		}
	}
}

// seedVoucher inserts one voucher with a given code (blind-index HMAC), state and window.
func seedVoucher(t *testing.T, db *pgxpool.Pool, id, code, state string, vf, vu *time.Time) {
	t.Helper()
	h, _ := testVHMAC(context.Background(), testTenant, testSite, code)
	_, err := db.Exec(context.Background(),
		`INSERT INTO iam_v2.vouchers
		   (id,tenant_id,site_id,package_revision_id,code_hmac,code_ciphertext,code_nonce,
		    code_key_generation_id,code_last4,state,redemption_valid_from,redemption_valid_until)
		 VALUES ($1,$2,$3,'cccc0000-0000-0000-0000-0000000000d1',$4,'\x01','\x01',
		    'ffff0000-0000-0000-0000-000000000001','1234',$5,$6,$7)`,
		id, testTenant, testSite, h, state, vf, vu)
	if err != nil {
		t.Fatalf("seed voucher %s: %v", id, err)
	}
}

func TestVoucherScratchIntegration(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	seedVoucherChain(t, db)
	ctx := context.Background()

	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	tp := func(x time.Time) *time.Time { return &x }
	past := tp(now.Add(-time.Hour))
	future := tp(now.Add(time.Hour))

	// One voucher per state/window. Codes are distinct so each resolves by its own HMAC.
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000001", "VALID-UNUSED", "UNUSED", past, future)
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000002", "REDEEMED", "REDEEMED", past, future)
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000003", "REVOKED", "REVOKED", past, future)
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000004", "REDEXPIRED", "REDEMPTION_EXPIRED", past, future)
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000005", "BEFORE-WINDOW", "UNUSED", future, tp(now.Add(2*time.Hour)))
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000006", "AT-UNTIL", "UNUSED", past, tp(now)) // exclusive upper bound
	seedVoucher(t, db, "f0000000-0000-0000-0000-000000000007", "AFTER-WINDOW", "UNUSED", past, tp(now.Add(-time.Minute)))

	a, _ := New(
		Config{MasterEnabled: true, Methods: map[Method]bool{MethodVoucher: true}},
		NewPgRepository(db), NopObserver{},
		WithClock(func() time.Time { return now }),
		WithVoucherHMAC(testVHMAC),
	)

	// ---- valid UNUSED in-window: allow, pinned auth_context, voucher stays UNUSED ----
	res, err := a.Authenticate(ctx, Request{Method: MethodVoucher, TenantID: testTenant, SiteID: testSite,
		Secret: "VALID-UNUSED", Device: gnDevice("de:ad:be:ef:50:01")})
	if err != nil || res.Decision != DecisionAllow || res.AuthContextID == "" || res.DeviceID == "" {
		t.Fatalf("valid voucher must allow with auth_context+device: %+v err=%v", res, err)
	}
	if res.Subject.VoucherID != "f0000000-0000-0000-0000-000000000001" {
		t.Fatalf("resolved wrong voucher: %q", res.Subject.VoucherID)
	}
	// auth_context is pinned to tenant/site/method/device/guest-network.
	var acMethod, acTenant, acSite, acVoucher, acDevice, acGN string
	if err := db.QueryRow(ctx,
		`SELECT method, tenant_id::text, site_id::text, voucher_id::text, device_id::text, guest_network_id::text
		   FROM iam_v2.auth_contexts WHERE id=$1`, res.AuthContextID).
		Scan(&acMethod, &acTenant, &acSite, &acVoucher, &acDevice, &acGN); err != nil {
		t.Fatalf("read auth_context: %v", err)
	}
	if acMethod != "VOUCHER" || acTenant != testTenant || acSite != testSite ||
		acVoucher != res.Subject.VoucherID || acDevice != res.DeviceID || acGN != testGN {
		t.Fatalf("auth_context pins wrong: method=%s tenant=%s site=%s voucher=%s device=%s gn=%s",
			acMethod, acTenant, acSite, acVoucher, acDevice, acGN)
	}
	// Phase 1B does NOT redeem: the voucher is still UNUSED.
	var state string
	db.QueryRow(ctx, `SELECT state FROM iam_v2.vouchers WHERE id=$1`, res.Subject.VoucherID).Scan(&state)
	if state != "UNUSED" {
		t.Fatalf("Phase 1B must not redeem: voucher state=%s", state)
	}

	// ---- every non-redeemable case denies (not_redeemable), never allow ----
	for _, code := range []string{"REDEEMED", "REVOKED", "REDEXPIRED", "BEFORE-WINDOW", "AT-UNTIL", "AFTER-WINDOW"} {
		r, err := a.Authenticate(ctx, Request{Method: MethodVoucher, TenantID: testTenant, SiteID: testSite,
			Secret: code, Device: gnDevice("de:ad:be:ef:50:02")})
		if err != nil || r.Decision != DecisionDeny || r.Reason != "not_redeemable" {
			t.Fatalf("%s must deny not_redeemable: %+v err=%v", code, r, err)
		}
	}

	// ---- wrong code / tenant / site never resolves (invalid_code, not not_redeemable) ----
	for _, bad := range []Request{
		{Method: MethodVoucher, TenantID: testTenant, SiteID: testSite, Secret: "NO-SUCH-CODE", Device: gnDevice("de:ad:be:ef:50:03")},
		{Method: MethodVoucher, TenantID: "88888888-8888-8888-8888-888888888888", SiteID: testSite, Secret: "VALID-UNUSED", Device: gnDevice("de:ad:be:ef:50:04")},
		{Method: MethodVoucher, TenantID: testTenant, SiteID: "88888888-8888-8888-8888-888888888889", Secret: "VALID-UNUSED", Device: gnDevice("de:ad:be:ef:50:05")},
	} {
		r, err := a.Authenticate(ctx, bad)
		if err != nil || r.Decision != DecisionDeny || r.Reason != "invalid_code" {
			t.Fatalf("wrong tenant/site/code must deny invalid_code: %+v err=%v", r, err)
		}
	}

	// ---- NO commerce/session side effects anywhere ----
	for _, tbl := range []string{
		"iam_v2.purchases", "iam_v2.settlements", "iam_v2.entitlements",
		"iam_v2.entitlement_adjustments", "iam_v2.sessions",
	} {
		var n int
		db.QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n)
		if n != 0 {
			t.Fatalf("Phase 1B voucher path created %d rows in %s (must be 0)", n, tbl)
		}
	}
	// exactly one auth_context total, and it is the valid one.
	var acs int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.auth_contexts`).Scan(&acs)
	if acs != 1 {
		t.Fatalf("expected exactly 1 auth_context (only the valid voucher), got %d", acs)
	}
}
