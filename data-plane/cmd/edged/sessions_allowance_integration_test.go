//go:build integration

package main

// The allowance on the screen must be the one being enforced: sessionCols reads COALESCE(e.data_quota_bytes, spr.data_quota_bytes),
// matching enforce.go and checkout.go. This runs the real shared projection against a real database; a string assertion would pass against SQL that no longer parses.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// seedAllowanceSession creates a plan revision with planQuota, an entitlement with its own frozen quota entQuota (nil = none),
// consumed bytes and one active session; it returns the session id.
func seedAllowanceSession(t *testing.T, f *apiFixture, planQuota *int64, entQuota *int64, consumed int64, mac string) string {
	t.Helper()
	ctx := context.Background()

	var svcPlan, svcRev, pkg, pkgRev, purchase string
	suffix := "alw-" + mac[len(mac)-2:]
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.service_plans (tenant_id, site_id, code)
		VALUES ($1,$2,'plan-'||$3) RETURNING id::text`,
		f.tenant, f.site, suffix).Scan(&svcPlan); err != nil {
		t.Fatalf("seed service plan: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices, data_quota_bytes)
		VALUES ($1,$2,$3,1,10000,5000,4,$4) RETURNING id::text`,
		f.tenant, f.site, svcPlan, planQuota).Scan(&svcRev); err != nil {
		t.Fatalf("seed plan revision: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code)
		VALUES ($1,$2,'pkg-'||$3) RETURNING id::text`,
		f.tenant, f.site, suffix).Scan(&pkg); err != nil {
		t.Fatalf("seed package: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type)
		VALUES ($1,$2,$3,1,$4,'FREE_STAY') RETURNING id::text`,
		f.tenant, f.site, pkg, svcRev).Scan(&pkgRev); err != nil {
		t.Fatalf("seed package revision: %v", err)
	}
	// purchases are commerce_intent-guarded on the full appliance schema.
	if err := controlled(ctx, f.pool, []string{"commerce_intent"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, trigger)
		VALUES ($1,$2,$3,'ADMIN_GRANT') RETURNING id::text`,
			f.tenant, f.site, pkgRev).Scan(&purchase)
	}); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}

	// A guest ACCOUNT is the subject: the projection is subject-agnostic and needs no PMS interface, stay or roster.
	var account string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
		(tenant_id, site_id, username, password_hash)
		VALUES ($1,$2,'guest-'||$3,'$argon2id$fixture$not-a-real-hash') RETURNING id::text`,
		f.tenant, f.site, suffix).Scan(&account); err != nil {
		t.Fatalf("seed guest account: %v", err)
	}

	// The insert and its first transition are one transaction: a deferred constraint checks the append-only history at COMMIT.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, purchase_id, guest_account_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at,
		 data_quota_bytes, consumed_data_bytes)
		VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		        now() + interval '1 day', $7, $8)
		RETURNING id::text`, f.tenant, f.site, purchase, account, svcRev, pkgRev, entQuota, consumed).
		Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	// The status column refuses direct sets; go through the sanctioned writer.
	if _, err := tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '1 minute','ADMIN_GRANT')`,
		ent); err != nil {
		t.Fatalf("activate entitlement: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit entitlement: %v", err)
	}

	var device, sess string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`,
		f.tenant, f.site, mac).Scan(&device); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	// On the full schema a session needs a device authorization binding on its entitlement (device_auth family).
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED',now(),now())`, f.tenant, f.site, ent, device); err != nil {
		t.Fatalf("seed entitlement device: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"device_auth"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
		VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, ent, device)
		return err
	}); err != nil {
		t.Fatalf("seed device authorization: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id, site_id, entitlement_id, device_id, ip, mac, state, started, expires_at)
		VALUES ($1,$2,$3,$4,('10.66.0.'||(1 + (hashtext($5) % 200 + 200) % 200))::inet,$5::macaddr,'active',
		        now(), now() + interval '1 day')
		RETURNING id::text`, f.tenant, f.site, ent, device, mac).Scan(&sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sess
}

func readProjectedAllowance(t *testing.T, f *apiFixture, sessionID string) (quota *int64, used *int64) {
	t.Helper()
	var row edgeSessionRow
	r := f.pool.QueryRow(context.Background(),
		`SELECT `+sessionCols+` `+sessionFrom+` WHERE s.id = $1`, sessionID)
	if err := scanEdgeSession(r, &row); err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	return row.DataQuotaBytes, row.DataUsedBytes
}

func TestSessionAllowance_ShowsTheEntitlementsOwnFrozenQuota(t *testing.T) {
	f := newAPI(t)

	// A seven-night stay at 1 GB a night on a plan whose own figure is 100 MB.
	sevenGB := int64(7_000_000_000)
	hundredMB := int64(100_000_000)
	sess := seedAllowanceSession(t, f, &hundredMB, &sevenGB, 1_200_000_000, "02:00:00:00:00:01")

	quota, used := readProjectedAllowance(t, f, sess)
	if quota == nil {
		t.Fatal("no allowance projected at all")
	}
	if *quota != sevenGB {
		t.Errorf("allowance = %d, want %d (the entitlement's frozen grant, not the plan's 100 MB)", *quota, sevenGB)
	}
	if used == nil || *used != 1_200_000_000 {
		t.Errorf("usage = %v, want the entitlement's consumed bytes", used)
	}
	if *used > *quota {
		t.Error("usage still reads as over the limit — the two halves are describing different allowances")
	}
}

func TestSessionAllowance_FallsBackToThePlanWhenTheEntitlementHasNoneOfItsOwn(t *testing.T) {
	f := newAPI(t)

	// Entitlements granted before PER_STAY_NIGHT carry NULL and must report the pinned plan revision's figure.
	fiveHundredMB := int64(500_000_000)
	sess := seedAllowanceSession(t, f, &fiveHundredMB, nil, 10_000_000, "02:00:00:00:00:02")

	quota, _ := readProjectedAllowance(t, f, sess)
	if quota == nil || *quota != 500_000_000 {
		t.Fatalf("allowance = %v, want the plan revision's 500 MB", quota)
	}
}

// Unlimited must stay unlimited: COALESCE of two NULLs is NULL; a substituted zero would render as a guest with no allowance.
func TestSessionAllowance_UnlimitedStaysUnlimited(t *testing.T) {
	f := newAPI(t)

	sess := seedAllowanceSession(t, f, nil, nil, 42, "02:00:00:00:00:03")

	quota, used := readProjectedAllowance(t, f, sess)
	if quota != nil {
		t.Errorf("allowance = %d, want no limit at all (NULL)", *quota)
	}
	if used == nil || *used != 42 {
		t.Errorf("usage = %v, want 42 — an unlimited allowance still meters usage", used)
	}
}

// The projection and the enforcement predicate, written in different files, must agree on a guest's limit for every entitlement shape.
func TestSessionAllowance_AgreesWithWhatIsEnforced(t *testing.T) {
	f := newAPI(t)

	frozen := int64(4_000_000_000)
	planA := int64(100_000_000)
	withOwn := seedAllowanceSession(t, f, &planA, &frozen, 0, "02:00:00:00:00:04")
	planB := int64(250_000_000)
	withoutOwn := seedAllowanceSession(t, f, &planB, nil, 0, "02:00:00:00:00:05")

	for _, sess := range []string{withOwn, withoutOwn} {
		projected, _ := readProjectedAllowance(t, f, sess)

		var enforced *int64
		if err := f.pool.QueryRow(context.Background(), `
			SELECT COALESCE(e.data_quota_bytes, spr.data_quota_bytes)
			  FROM iam_v2.sessions s
			  JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
			  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
			 WHERE s.id = $1`, sess).Scan(&enforced); err != nil {
			t.Fatalf("enforcement expression: %v", err)
		}
		switch {
		case projected == nil && enforced == nil:
		case projected == nil || enforced == nil:
			t.Errorf("session %s: shown %v, enforced %v", sess, projected, enforced)
		case *projected != *enforced:
			t.Errorf("session %s: shown %d, enforced %d", sess, *projected, *enforced)
		}
	}
}
