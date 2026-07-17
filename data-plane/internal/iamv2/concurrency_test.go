package iamv2

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- item 4: config loader (unit) -----------------------------------------

func TestLoadConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) Getenv { return func(k string) string { return m[k] } }
	// default production: everything OFF, valid, no stub
	cfg, err := LoadConfigFromEnv(env(nil), true)
	if err != nil || cfg.MasterEnabled || cfg.AllowSocialStub {
		t.Fatalf("default must be all-off/no-stub: %+v %v", cfg, err)
	}
	for _, m := range []Method{MethodVoucher, MethodAccount, MethodOTP, MethodSocial} {
		if cfg.Methods[m] {
			t.Fatalf("%s must default OFF", m)
		}
	}
	// malformed boolean => startup failure
	if _, err := LoadConfigFromEnv(env(map[string]string{EnvMaster: "yesish"}), true); err == nil {
		t.Fatal("malformed boolean must fail startup")
	}
	// method ON while master OFF => startup failure
	if _, err := LoadConfigFromEnv(env(map[string]string{EnvOTP: "true"}), true); err == nil {
		t.Fatal("method-on-master-off must fail startup")
	}
	// production profile refuses the stub even if the flag is set
	cfg, err = LoadConfigFromEnv(env(map[string]string{EnvMaster: "true", EnvSocial: "true", EnvSocialStub: "true"}), true)
	if err != nil || cfg.AllowSocialStub {
		t.Fatalf("production must refuse the stub: %+v %v", cfg, err)
	}
	if !strings.Contains(cfg.SafeFlagSummary(), "master=true") {
		t.Fatal("safe flag summary should report master state")
	}
}

// ---- item 5: argon2 bounds (unit) -----------------------------------------

func TestArgon2ParamBounds(t *testing.T) {
	// oversized m, t, p must be rejected WITHOUT running argon2 (no resource exhaustion)
	bad := []string{
		"$argon2id$v=19$m=9999999999,t=1,p=4$YWJjZGVmZ2hpamtsbW5v$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=9999,p=4$YWJjZGVmZ2hpamtsbW5v$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=1,p=9999$YWJjZGVmZ2hpamtsbW5v$" + strings.Repeat("A", 43),
		"$argon2id$v=1$m=65536,t=1,p=4$YWJjZGVmZ2hpamtsbW5v$" + strings.Repeat("A", 43),
	}
	for i, h := range bad {
		ok, err := verifyArgon2id("pw", h)
		if ok || err == nil {
			t.Fatalf("case %d: oversized/invalid params must be rejected", i)
		}
		if CodeOf(err) != ErrInvalidCred {
			t.Fatalf("case %d: must return invalid_credential code, got %v", i, CodeOf(err))
		}
	}
}

// ---- item 1: principal resolution concurrency (DB) ------------------------

func TestPrincipalResolutionConcurrent(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	repo := NewPgRepository(db)
	const workers = 24
	ids := make([]string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = repo.WithTx(ctx, func(tx Tx) error {
				pid, err := tx.ResolvePrincipalByIdentity(ctx, testTenant, "EMAIL", "", "race@example.com", time.Now())
				ids[i] = pid
				return err
			})
		}(i)
	}
	wg.Wait()
	first := ids[0]
	if first == "" {
		t.Fatal("no principal resolved")
	}
	for i, id := range ids {
		if id != first {
			t.Fatalf("worker %d got different principal %q != %q", i, id, first)
		}
	}
	var principals, identities int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_principals`).Scan(&principals)
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_principal_identities WHERE factor_value_norm='race@example.com'`).Scan(&identities)
	if identities != 1 {
		t.Fatalf("expected exactly 1 identity, got %d", identities)
	}
	if principals != 1 {
		t.Fatalf("expected exactly 1 principal (no orphan), got %d", principals)
	}
	// issuer namespaces independent: SOCIAL with same value is a different identity
	_ = repo.WithTx(ctx, func(tx Tx) error {
		_, err := tx.ResolvePrincipalByIdentity(ctx, testTenant, "SOCIAL_SUBJECT", "google", "race@example.com", time.Now())
		return err
	})
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_principals`).Scan(&principals)
	if principals != 2 {
		t.Fatalf("issuer/type namespaces must be independent (expected 2 principals), got %d", principals)
	}
}

// ---- item 2/3: transaction rollback on downstream failure (DB) ------------

func TestRollbackOnDownstreamFailure(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	repo := NewPgRepository(db)
	// UpsertDevice succeeds, but CreateAuthContext fails (guest_network_id absent => FK violation).
	// The whole tx must roll back: NO device row must persist.
	err := repo.WithTx(ctx, func(tx Tx) error {
		if _, e := tx.UpsertDevice(ctx, testTenant, testSite, "33333333-3333-3333-3333-333333333333", "de:ad:be:ef:00:01", "", "", time.Now()); e != nil {
			return e
		}
		_, e := tx.CreateAuthContext(ctx, AuthContextSpec{
			TenantID: testTenant, SiteID: testSite, Method: MethodOTP, Subject: Subject{PrincipalID: "55555555-5555-5555-5555-555555555555"},
			DeviceID: "66666666-6666-6666-6666-666666666666", GuestNetworkID: "77777777-7777-7777-7777-777777777777", TTL: time.Minute, Now: time.Now(),
		})
		return e
	})
	if err == nil {
		t.Fatal("expected downstream failure")
	}
	var devices int
	db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.devices WHERE mac='de:ad:be:ef:00:01'`).Scan(&devices)
	if devices != 0 {
		t.Fatalf("transaction must roll back: found %d partial device rows", devices)
	}
}

// ---- item 3: auth_context atomic consume (DB) -----------------------------

func TestAuthContextConsumeAndDoubleConsume(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	repo := NewPgRepository(db)
	// seed an account + create an auth_context via the account adapter
	db.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, enabled) VALUES ($1,$2,'carol',$3,true)`, testTenant, testSite, argon2idHash("pw"))
	a, _ := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}, repo, NopObserver{})
	res, err := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: testSite, Username: "carol", Secret: "pw", Device: gnDevice("de:ad:be:ef:11:22")})
	if err != nil || res.AuthContextID == "" {
		t.Fatalf("seed auth_context: %+v %v", res, err)
	}
	acID := res.AuthContextID

	// double-consume: many workers, exactly one wins
	const workers = 24
	var wins int64
	var consumedErr int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = repo.WithTx(ctx, func(tx Tx) error {
				subj, err := tx.ConsumeAuthContext(ctx, acID, MethodAccount, time.Now())
				if err == nil && subj.GuestAccountID != "" {
					atomic.AddInt64(&wins, 1)
					return nil
				}
				if CodeOf(err) == ErrACConsumed {
					atomic.AddInt64(&consumedErr, 1)
				}
				return err
			})
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one consumer must win, got %d", wins)
	}

	// mismatch: consuming with the wrong method on a fresh context
	res2, _ := a.Authenticate(ctx, Request{Method: MethodAccount, TenantID: testTenant, SiteID: testSite, Username: "carol", Secret: "pw", Device: gnDevice("de:ad:be:ef:11:23")})
	_ = repo.WithTx(ctx, func(tx Tx) error {
		_, err := tx.ConsumeAuthContext(ctx, res2.AuthContextID, MethodOTP, time.Now())
		if CodeOf(err) != ErrACMismatch {
			t.Fatalf("wrong method must return mismatch, got %v", CodeOf(err))
		}
		return err
	})

	// expired: force expiry, then consume -> expired
	db.Exec(ctx, `UPDATE iam_v2.auth_contexts SET expires_at = now() - interval '1 hour' WHERE id=$1`, res2.AuthContextID)
	_ = repo.WithTx(ctx, func(tx Tx) error {
		_, err := tx.ConsumeAuthContext(ctx, res2.AuthContextID, MethodAccount, time.Now())
		if CodeOf(err) != ErrACExpired {
			t.Fatalf("expired context must return expired, got %v", CodeOf(err))
		}
		return err
	})
}
