package iamv2

import (
	"context"
	"testing"
	"time"
)

// ---- item 1: voucher state/validity rule (pure unit) ----------------------

func TestVoucherRedeemableRule(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tp := func(x time.Time) *time.Time { return &x }
	cases := []struct {
		name   string
		state  string
		vf, vu *time.Time
		want   bool
	}{
		{"unused in window", "UNUSED", tp(past), tp(future), true},
		{"unused open window", "UNUSED", nil, nil, true},
		{"redeemed", "REDEEMED", tp(past), tp(future), false},
		{"revoked", "REVOKED", tp(past), tp(future), false},
		{"redemption_expired state", "REDEMPTION_EXPIRED", tp(past), tp(future), false},
		{"before valid_from", "UNUSED", tp(future), tp(future.Add(time.Hour)), false},
		{"at valid_until (exclusive)", "UNUSED", tp(past), tp(now), false},
		{"after valid_until", "UNUSED", tp(past), tp(past.Add(time.Minute)), false},
	}
	for _, c := range cases {
		if got := voucherRedeemable(c.state, c.vf, c.vu, now); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// ---- item 2: account is scoped to the site (DB) ---------------------------

func TestAccountCrossSiteRejected(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	// account belongs to testSite
	db.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, enabled) VALUES ($1,$2,'dave',$3,true)`, testTenant, testSite, argon2idHash("pw"))
	a, _ := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}, NewPgRepository(db), NopObserver{})

	otherSite := "99999999-9999-9999-9999-999999999999"
	// correct tenant+site -> allow
	if res, err := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: testSite, Username: "dave", Secret: "pw", Device: gnDevice("de:ad:be:ef:20:01")}); err != nil || res.Decision != DecisionAllow {
		t.Fatalf("same-site must allow: %+v %v", res, err)
	}
	// same tenant, different site -> generic deny (same as nonexistent)
	if res, _ := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: otherSite, Username: "dave", Secret: "pw", Device: gnDevice("de:ad:be:ef:20:02")}); res.Decision != DecisionDeny {
		t.Fatalf("cross-site must deny generically: %+v", res)
	}
	// different tenant -> deny
	if res, _ := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: otherSite, SiteID: testSite, Username: "dave", Secret: "pw", Device: gnDevice("de:ad:be:ef:20:03")}); res.Decision != DecisionDeny {
		t.Fatalf("cross-tenant must deny: %+v", res)
	}
}

// ---- item 4: social identity scratch (DB) ---------------------------------

func TestSocialScratchIntegration(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	a, _ := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodSocial: true}}, NewPgRepository(db), NopObserver{})

	base := Request{Method: MethodSocial, TenantID: testTenant, SiteID: testSite, Provider: "google", FactorIssuer: "google", FactorValue: "subject-123", Device: gnDevice("de:ad:be:ef:30:01")}
	res, err := a.Authenticate(ctx, base)
	if err != nil || res.Decision != DecisionAllow || res.Subject.PrincipalID == "" {
		t.Fatalf("social allow: %+v %v", res, err)
	}
	// same issuer+subject -> same principal (idempotent)
	res2, _ := a.Authenticate(ctx, base)
	if res2.Subject.PrincipalID != res.Subject.PrincipalID {
		t.Fatal("same issuer+subject must resolve the same principal")
	}
	// different issuer, same subject -> independent identity/principal
	diff := base
	diff.FactorIssuer = "apple"
	diff.Device = gnDevice("de:ad:be:ef:30:02")
	res3, _ := a.Authenticate(ctx, diff)
	if res3.Subject.PrincipalID == res.Subject.PrincipalID {
		t.Fatal("different issuer must produce an independent principal")
	}
	var principals int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_principals`).Scan(&principals)
	if principals != 2 {
		t.Fatalf("expected 2 principals (google + apple), got %d", principals)
	}
	// a SOCIAL auth_context was created
	var n int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.auth_contexts WHERE method='SOCIAL'`).Scan(&n)
	if n < 1 {
		t.Fatal("expected a SOCIAL auth_context")
	}
}

// ---- item 3: pinned consume rejects wrong device/tenant/site (DB) ---------

func TestConsumePinnedRejectsWrongPins(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	repo := NewPgRepository(db)
	db.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, enabled) VALUES ($1,$2,'erin',$3,true)`, testTenant, testSite, argon2idHash("pw"))
	a, _ := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}, repo, NopObserver{})
	res, _ := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: testSite, Username: "erin", Secret: "pw", Device: gnDevice("de:ad:be:ef:40:01")})
	if res.AuthContextID == "" {
		t.Fatal("seed auth_context failed")
	}
	base := ConsumeAuthContextRequest{AuthContextID: res.AuthContextID, TenantID: testTenant, SiteID: testSite, ExpectedMethod: MethodAccount, ExpectedDeviceID: res.DeviceID, ExpectedGuestNetworkID: testGN, Now: time.Now()}

	check := func(name string, mutate func(*ConsumeAuthContextRequest), wantCode Code) {
		r := base
		mutate(&r)
		_ = repo.WithTx(ctx, func(tx Tx) error {
			_, err := tx.ConsumeAuthContext(ctx, r)
			if CodeOf(err) != wantCode {
				t.Fatalf("%s: got %v want %v", name, CodeOf(err), wantCode)
			}
			return err
		})
	}
	// wrong device / guest-network (same tenant+site) -> mismatch
	check("wrong device", func(r *ConsumeAuthContextRequest) { r.ExpectedDeviceID = "88888888-8888-8888-8888-888888888888" }, ErrACMismatch)
	check("wrong network", func(r *ConsumeAuthContextRequest) { r.ExpectedGuestNetworkID = "88888888-8888-8888-8888-888888888889" }, ErrACMismatch)
	// wrong tenant / site -> not found (must not reveal existence)
	check("wrong tenant", func(r *ConsumeAuthContextRequest) { r.TenantID = "88888888-8888-8888-8888-888888888890" }, ErrACNotFound)
	check("wrong site", func(r *ConsumeAuthContextRequest) { r.SiteID = "88888888-8888-8888-8888-888888888891" }, ErrACNotFound)
	// correct pins still consume successfully afterwards
	_ = repo.WithTx(ctx, func(tx Tx) error {
		cc, err := tx.ConsumeAuthContext(ctx, base)
		if err != nil || cc.Subject.GuestAccountID == "" {
			t.Fatalf("correct pins must consume: %+v %v", cc, err)
		}
		return err
	})
}
