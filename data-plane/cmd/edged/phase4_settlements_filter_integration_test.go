//go:build integration && phase4

package main

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// One card settlement on the fixture's site, with no recorded "When" (no quote, no entitlement); returns its package code.
func seedListedSettlement(t *testing.T, f *apiFixture) string {
	t.Helper()
	ctx := context.Background()
	var planID, planRev, pkgID, pkgRev, code, purchaseID string
	q := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	q(&planID, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code)
		VALUES ($1,$2,'flt-'||substr(md5(random()::text),1,8)) RETURNING id::text`, f.tenant, f.site)
	q(&planRev, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'flt',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, f.tenant, f.site, planID)
	q(&pkgID, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code)
		VALUES ($1,$2,'payfilter-'||substr(md5(random()::text),1,8)) RETURNING id::text`, f.tenant, f.site)
	q(&code, `SELECT code FROM iam_v2.internet_packages WHERE id=$1`, pkgID)
	q(&pkgRev, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,
		 settlement_methods)
		VALUES ($1,$2,$3,1,$4,'GENERAL',100,'USD',2,'{ONLINE_PAYMENT}') RETURNING id::text`, f.tenant, f.site, pkgID, planRev)
	if err := controlled(ctx, f.pool, []string{"commerce_intent"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,trigger,amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,'ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
			f.tenant, f.site, pkgRev).Scan(&purchaseID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'ONLINE_PAYMENT','REQUIRED')`, f.tenant, f.site, purchaseID)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return code
}

func listed(t *testing.T, f *apiFixture, query, search string) (int, map[string]any) {
	t.Helper()
	h := map[string]string{}
	if search != "" {
		h[settlementSearchHeader] = url.PathEscape(search)
	}
	return f.getWithHeader(t, "/financial-ops/settlements"+query, h)
}

func summaryAll(t *testing.T, body map[string]any) float64 {
	t.Helper()
	sum, _ := body["summary"].(map[string]any)
	n, ok := sum["all"].(float64)
	if !ok {
		t.Fatalf("no summary.all in %v", body)
	}
	return n
}

// The search and the period select the rows, the total and the tiles alike.
func TestIntegrationFinOpsAPI_SettlementsSearchAndPeriodSelectRowsAndSummary(t *testing.T) {
	f := newAPI(t, "payments_operator")
	code := seedListedSettlement(t, f)

	c, body := listed(t, f, "", code)
	if c != 200 || summaryAll(t, body) != 1 || body["total"].(float64) != 1 || len(body["settlements"].([]any)) != 1 {
		t.Fatalf("searching the package code: %d %v", c, body)
	}
	if c, body = listed(t, f, "?view=paid", "card payment"); c != 200 || body["total"].(float64) < 1 {
		t.Fatalf("searching the paid-by words: %d %v", c, body)
	}
	if c, body = listed(t, f, "", code+"-nothing-like-it"); c != 200 || summaryAll(t, body) != 0 || len(body["settlements"].([]any)) != 0 {
		t.Fatalf("a search that matches nothing still selected rows: %d %v", c, body)
	}

	// The seeded payment has no "When", so any period leaves it out, and the tiles with it.
	now := time.Now().UTC()
	period := "?from=" + url.QueryEscape(now.Add(-24*time.Hour).Format(time.RFC3339)) + "&to=" + url.QueryEscape(now.Add(time.Hour).Format(time.RFC3339))
	if c, body = listed(t, f, period, code); c != 200 || summaryAll(t, body) != 0 || len(body["settlements"].([]any)) != 0 {
		t.Fatalf("a payment with no time fell inside a period: %d %v", c, body)
	}
}

// Every table, column and grant the filtered list reads must hold for svc_edged; the fixture connects as a superuser.
func TestIntegrationFinOpsAPI_SettlementsListRunsAsTheProductionRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const tenant, site = "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	now := time.Now()
	if _, err := pool.Exec(ctx, settlementListed+` SELECT count(*), sum(v.amount_minor) FROM v`,
		tenant, site, nil, now.Add(-24*time.Hour), now, likePattern("room")); err != nil {
		t.Fatalf("the filtered settlement list does not run as svc_edged: %v", err)
	}
}

func TestIntegrationFinOpsAPI_SettlementsRefuseABadPeriod(t *testing.T) {
	f := newAPI(t, "payments_operator")
	now := time.Now().UTC().Format(time.RFC3339)
	for _, q := range []string{"?from=yesterday", "?to=2026-13-01", "?from=" + url.QueryEscape(now) + "&to=" + url.QueryEscape(now)} {
		if c, _ := listed(t, f, q, ""); c != 400 {
			t.Fatalf("%s was accepted: %d", q, c)
		}
	}
}
