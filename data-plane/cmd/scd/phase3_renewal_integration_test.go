//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Package renewal (0213): a revision bought before is offered and purchasable again only when it is renewable, nothing of it is live or
// being settled, and the stay is under max_purchases_per_stay (empty = unlimited). Offers and the purchase guard use one rule.

func (f *authFixture) addRenewablePackage(t *testing.T, code string, renewable bool, max any) string {
	t.Helper()
	ctx := context.Background()
	var rev string
	if err := f.pool.QueryRow(ctx, `WITH
	  sp AS (SELECT id FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 LIMIT 1),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,$3,false,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions
	            (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,
	             price_minor,settlement_methods,duration_policy,renewable,max_purchases_per_stay)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,sp.id,'ONE_DAY',0,ARRAY['NOT_REQUIRED']::text[],
	                 '{"end_mode":"VALIDITY_WINDOW","duration_seconds":2}'::jsonb,$4,$5 FROM ip, sp RETURNING id)
	SELECT (SELECT id FROM ipr)::text`, f.tenant, f.site, code, renewable, max).Scan(&rev); err != nil {
		t.Fatalf("add package %s: %v", code, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages ip SET current_revision_id = r.id
		  FROM iam_v2.internet_package_revisions r WHERE r.id=$1 AND ip.id = r.package_id`, rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// buy resolves and grants rev through the guest handlers and returns the entitlement. Each purchase is a new session on the same
// address, which the stand-in for netd enforces only under a newer class generation (epoch).
func (f *authFixture) buy(t *testing.T, rev, reqID string, epoch int64) string {
	t.Helper()
	stop := f.startEnforcementOwnerAtEpoch(t, epoch)
	defer stop()
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
	if res.Outcome != outcomeVerified || offeredMethods(res.Offers, rev) == nil {
		t.Fatalf("the package was not offered: %+v", res)
	}
	raw, _ := json.Marshal(map[string]any{"auth_context_id": res.AuthContextID, "package_revision_id": rev,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac}})
	rec := httptest.NewRecorder()
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var g phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if g.Outcome != outcomeVerified || g.EntitlementID == "" {
		t.Fatalf("grant: %s", rec.Body.String())
	}
	return g.EntitlementID
}

// end lets the 2 s package run out and expires it through the expiry writer, which also ends its sessions.
func (f *authFixture) end(t *testing.T, ent string) {
	t.Helper()
	time.Sleep(2500 * time.Millisecond)
	var reason string
	if err := f.pool.QueryRow(context.Background(), `SELECT reason FROM iam_v2.p6_expire_entitlement($1::uuid)`, ent).Scan(&reason); err != nil || reason != "TIME" {
		t.Fatalf("the package did not expire: %q %v", reason, err)
	}
}
func (f *authFixture) offered(t *testing.T, rev string) bool {
	t.Helper()
	offers, err := f.p3.offersFor(context.Background(), f.stay, f.iface, f.network, mustDevice(t, f), time.Now())
	if err != nil {
		t.Fatalf("offers: %v", err)
	}
	for _, o := range offers {
		if o.PackageRevisionID == rev {
			return true
		}
	}
	return false
}

func TestIntegration_Renewal_ANonRenewablePackageIsNeverOfferedAgain(t *testing.T) {
	f := newAuthFixture(t)
	rev := f.addRenewablePackage(t, "ONCE", false, nil)
	ent := f.buy(t, rev, "0000e001-0000-4000-8000-000000000000", 1)
	f.end(t, ent)
	if f.offered(t, rev) {
		t.Fatal("a package that is not renewable was offered again after it ended")
	}
}

func TestIntegration_Renewal_ARenewablePackageIsBoughtAgainUpToItsMaximum(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	rev := f.addRenewablePackage(t, "DAILY", true, 2)

	first := f.buy(t, rev, "0000e101-0000-4000-8000-000000000000", 1)
	// While it is live, choosing it joins it: offered, and no second purchase is possible.
	if !f.offered(t, rev) {
		t.Fatal("the live package must stay offered so the room's other devices can join it")
	}
	if refusal := f.refusal(t, rev); refusal != "STILL_ACTIVE" {
		t.Fatalf("a second purchase while the first is live: refusal %q, want STILL_ACTIVE", refusal)
	}
	f.end(t, first)
	if !f.offered(t, rev) {
		t.Fatal("a renewable package that ended was not offered again")
	}
	second := f.buy(t, rev, "0000e102-0000-4000-8000-000000000000", 2)
	var seqs []int
	rows, err := f.pool.Query(ctx, `SELECT purchase_seq FROM iam_v2.purchases WHERE stay_id=$1 AND package_revision_id=$2 ORDER BY purchase_seq`, f.stay, rev)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s int
		_ = rows.Scan(&s)
		seqs = append(seqs, s)
	}
	rows.Close()
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("purchase sequence %v, want [1 2]", seqs)
	}
	f.end(t, second)
	if f.offered(t, rev) {
		t.Fatal("the package was offered beyond its maximum of 2 purchases per stay")
	}
	if refusal := f.refusal(t, rev); refusal != "LIMIT_REACHED" {
		t.Fatalf("refusal at the maximum %q, want LIMIT_REACHED", refusal)
	}
}

func (f *authFixture) refusal(t *testing.T, rev string) string {
	t.Helper()
	var r *string
	if err := f.pool.QueryRow(context.Background(), `SELECT iam_v2.stay_package_purchase_refusal($1::uuid,$2::uuid)`, f.stay, rev).Scan(&r); err != nil {
		t.Fatal(err)
	}
	if r == nil {
		return ""
	}
	return *r
}

// Two simultaneous purchases of the same renewal: exactly one is written, so exactly one could ever be charged.
func TestIntegration_Renewal_ConcurrentPurchasesOfOneRenewalHaveOneWinner(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	rev := f.addRenewablePackage(t, "RACE", true, nil)
	f.end(t, f.buy(t, rev, "0000e201-0000-4000-8000-000000000000", 1))
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000e202-0000-4000-8000-000000000000"))
	if res.AuthContextID == "" {
		t.Fatalf("no auth context: %+v", res)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
				errs[i] = err
				return
			}
			<-start
			var quote string
			if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.offer_quotes (tenant_id, site_id, auth_context_id, package_revision_id, pms_interface_id,
				price_minor, currency, currency_exponent, grant_snapshot, expires_at, consumed_at)
				VALUES ($1,$2,$3,$4,$5,0,'USD',2,'{}'::jsonb, now()+interval '5 minutes', now()) RETURNING id::text`,
				f.tenant, f.site, res.AuthContextID, rev, f.iface).Scan(&quote); err != nil {
				errs[i] = err
				return
			}
			if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,
				pms_interface_id,stay_id,trigger,amount_minor,currency,currency_exponent,state)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'GUEST_SELECTION',0,'USD',2,'GRANTED')`, f.tenant, f.site, rev, quote, res.AuthContextID, f.iface, f.stay); err != nil {
				errs[i] = err
				return
			}
			time.Sleep(200 * time.Millisecond)
			errs[i] = tx.Commit(ctx)
		}(i)
	}
	close(start)
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "purchase_once_per_stay"):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("%d purchases committed and %d refused, want exactly one of each", ok, refused)
	}
}

// An empty maximum is unlimited: the package is bought, ends and is bought again, three times and still on offer.
func TestIntegration_Renewal_AnEmptyMaximumRenewsWithoutLimit(t *testing.T) {
	f := newAuthFixture(t)
	rev := f.addRenewablePackage(t, "UNLIMITED", true, nil)
	for i, req := range []string{"0000e301-0000-4000-8000-000000000000", "0000e302-0000-4000-8000-000000000000", "0000e303-0000-4000-8000-000000000000"} {
		f.end(t, f.buy(t, rev, req, int64(i+1)))
	}
	if !f.offered(t, rev) || f.refusal(t, rev) != "" {
		t.Fatalf("an unlimited renewable package stopped being offered after 3 purchases (refusal %q)", f.refusal(t, rev))
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND package_revision_id=$2`, f.stay, rev).Scan(&n); err != nil || n != 3 {
		t.Fatalf("purchases %d (%v), want 3", n, err)
	}
}

// A renewal still being settled (a room charge not yet confirmed) blocks the next purchase: no second purchase, so no second charge.
func TestIntegration_Renewal_APurchaseAwaitingSettlementBlocksTheNext(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	rev := f.addRenewablePackage(t, "PENDING", true, nil)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000e401-0000-4000-8000-000000000000"))
	if res.AuthContextID == "" {
		t.Fatalf("no auth context: %+v", res)
	}
	insert := func(state string) error {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
			return err
		}
		var quote string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.offer_quotes (tenant_id, site_id, auth_context_id, package_revision_id, pms_interface_id,
			price_minor, currency, currency_exponent, grant_snapshot, expires_at, consumed_at)
			VALUES ($1,$2,$3,$4,$5,0,'USD',2,'{}'::jsonb, now()+interval '5 minutes', now()) RETURNING id::text`,
			f.tenant, f.site, res.AuthContextID, rev, f.iface).Scan(&quote); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,
			pms_interface_id,stay_id,trigger,amount_minor,currency,currency_exponent,state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'GUEST_SELECTION',0,'USD',2,$8)`, f.tenant, f.site, rev, quote, res.AuthContextID, f.iface, f.stay, state); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := insert("AWAITING_SETTLEMENT"); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	if r := f.refusal(t, rev); r != "PURCHASE_IN_PROGRESS" {
		t.Fatalf("refusal while the first purchase awaits settlement %q, want PURCHASE_IN_PROGRESS", r)
	}
	if f.offered(t, rev) {
		t.Fatal("a package whose purchase is still being settled was offered for another purchase")
	}
	if err := insert("AWAITING_SETTLEMENT"); err == nil || !strings.Contains(err.Error(), "purchase_once_per_stay") {
		t.Fatalf("a second purchase while the first awaits settlement: %v, want purchase_once_per_stay", err)
	}
}