//go:build integration

package main

// Room-guest commerce follows the same rules as other clients: card is offered only in a currency the default
// account accepts (iamv2.SiteMethods.CardApplicable), and PRIOR_PURCHASE limits apply per stay and device.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func (f *authFixture) addCardPackage(t *testing.T, code, currency string) string {
	t.Helper()
	var rev string
	if err := f.pool.QueryRow(context.Background(), `WITH
	  sp AS (SELECT id FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 LIMIT 1),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,$3,false,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions
	            (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,
	             price_minor,currency,currency_exponent,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,sp.id,'GENERAL',500,$4,2,ARRAY['ONLINE_PAYMENT']::text[],
	                 '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM ip, sp RETURNING id)
	SELECT (SELECT id FROM ipr)::text`, f.tenant, f.site, code, currency).Scan(&rev); err != nil {
		t.Fatalf("add card package: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.internet_packages ip SET current_revision_id = r.id
		  FROM iam_v2.internet_package_revisions r WHERE r.id=$1 AND ip.id = r.package_id`, rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

func offeredMethods(offers []phase3Offer, rev string) []string {
	for _, o := range offers {
		if o.PackageRevisionID == rev {
			return o.Methods
		}
	}
	return nil
}

func TestIntegration_RoomGuestCardFollowsTheDefaultAccountCurrency(t *testing.T) {
	f := newAuthFixture(t)
	usd := f.addCardPackage(t, "CARD_USD", "USD")
	prev := stayMethodGate
	t.Cleanup(func() { stayMethodGate = prev })
	resolve := func(reqID string, site iamv2.SiteMethods) []string {
		t.Helper()
		stayMethodGate = func(context.Context, *server) iamv2.SiteMethods { return site }
		_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
		if res.Outcome != outcomeVerified {
			t.Fatalf("the stay did not verify: %+v", res)
		}
		return offeredMethods(res.Offers, usd)
	}
	gbp := iamv2.SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"GBP"}}
	if m := resolve("0000c001-0000-4000-8000-000000000000", gbp); len(m) != 0 {
		t.Fatalf("a USD package was offered by card on a GBP account that does not convert: %v", m)
	}
	converts := gbp
	converts.CardAccepts = func(cur string) bool { return cur == "USD" }
	if m := resolve("0000c002-0000-4000-8000-000000000000", converts); len(m) != 1 || m[0] != iamv2.AcquireCard {
		t.Fatalf("a provider-converts account must offer card for USD, got %v", m)
	}
	same := iamv2.SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"USD"}}
	if m := resolve("0000c003-0000-4000-8000-000000000000", same); len(m) != 1 {
		t.Fatalf("a USD account must offer card for USD, got %v", m)
	}
}

func TestIntegration_RoomGuestOffersApplyPurchaseHistoryPerStayAndDevice(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()
	limited := f.addPackage(t, "ONCE_PER_CLIENT", []map[string]any{
		{"type": "PRIOR_PURCHASE", "value": map[string]any{"forbids_prior": true}}})

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000d001-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || offeredMethods(res.Offers, limited) == nil {
		t.Fatalf("a package the stay has never had must be offered: %+v", res)
	}
	raw, _ := json.Marshal(map[string]any{"auth_context_id": res.AuthContextID, "package_revision_id": limited,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac}})
	rec := httptest.NewRecorder()
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var granted phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &granted)
	if granted.Outcome != outcomeVerified || granted.EntitlementID == "" {
		t.Fatalf("grant: %s", rec.Body.String())
	}
	var dev string
	if err := f.pool.QueryRow(ctx, `SELECT device_id::text FROM iam_v2.sessions WHERE id=$1`, granted.SessionID).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	has := func(stay, device string, rev string) bool {
		t.Helper()
		offers, err := f.p3.offersFor(ctx, stay, f.iface, f.network, device, time.Now())
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
	// The stay's live access: choosing it again joins it (a second device of the room) -- still offered.
	other := mustDevice(t, f)
	if !has(f.stay, other, limited) {
		t.Fatal("the package behind the stay's live access must stay offered so the room's other devices can join")
	}
	// PER DEVICE: the same phone on ANOTHER stay has had this package.
	if has(f.otherStay, dev, limited) {
		t.Fatal("a device that already had the free package was offered it again on another stay")
	}
	// A new device on that other stay: never had it.
	if !has(f.otherStay, other, limited) {
		t.Fatal("a new device on a new stay must be offered the package")
	}
	// PER STAY, KEYED ON THE PACKAGE: the stay's access ends, the package is republished as revision 2 (which
	// the once-per-revision index cannot see), and the stay is still not offered a second free allowance.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'TERMINATED',now(),'ADMIN')`, granted.EntitlementID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("end the access: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var rev2 string
	if err := f.pool.QueryRow(ctx, `WITH r AS (
		  INSERT INTO iam_v2.internet_package_revisions
		    (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
		  SELECT gen_random_uuid(),tenant_id,site_id,package_id,2,service_plan_revision_id,package_type,0,settlement_methods,duration_policy
		    FROM iam_v2.internet_package_revisions WHERE id=$1 RETURNING id, package_id)
		SELECT id::text FROM r`, limited).Scan(&rev2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value)
		VALUES ($1,$2,$3,'PRIOR_PURCHASE','{"forbids_prior":true}'::jsonb)`, f.tenant, f.site, rev2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages ip SET current_revision_id=$1
		  FROM iam_v2.internet_package_revisions r WHERE r.id=$1 AND ip.id=r.package_id`, rev2); err != nil {
		t.Fatal(err)
	}
	if has(f.stay, other, rev2) {
		t.Fatal("a republish handed the stay a second free allowance")
	}
}

func TestIntegration_VoucherIssueRefusesPackagesAVoucherCanNeverRedeem(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	groupOnly := f.addPackage(t, "STAFF_ONLY", []map[string]any{
		{"type": "CLIENT_GROUP", "value": map[string]any{"group_ids": []string{"00000000-0000-4000-8000-00000000a001"}}}})
	var perNight string
	if err := f.pool.QueryRow(ctx, `WITH
	  sp AS (SELECT id FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 LIMIT 1),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,'PER_NIGHT',false,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions
	            (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,
	             price_minor,settlement_methods,duration_policy,data_allocation_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,sp.id,'GENERAL',0,ARRAY['NOT_REQUIRED','PREPAID']::text[],
	                 '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb,
	                 '{"mode":"PER_STAY_NIGHT","gb_per_night":1}'::jsonb FROM ip, sp RETURNING id)
	SELECT (SELECT id FROM ipr)::text`, f.tenant, f.site).Scan(&perNight); err != nil {
		t.Fatalf("add a per-night package: %v", err)
	}
	plain := f.addPackage(t, "PLAIN_FREE", nil)
	issue := func(rev string) (int, string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"package_revision_id": rev, "count": 1, "issued_by": "op-1"})
		rec := httptest.NewRecorder()
		f.srv.issueVouchersIAMv2(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out["error"]
	}
	if code, msg := issue(groupOnly); code != http.StatusConflict || msg == "" {
		t.Fatalf("a group-only package must be refused at issue: %d %q", code, msg)
	}
	if code, msg := issue(perNight); code != http.StatusConflict || msg == "" {
		t.Fatalf("a per-night package must be refused at issue: %d %q", code, msg)
	}
	if code, _ := issue(plain); code == http.StatusConflict {
		t.Fatal("an ordinary package was refused by the redeemability check")
	}
	if code, _ := issue("not-a-uuid"); code != http.StatusConflict {
		t.Fatalf("a malformed revision id is refused before any SQL: %d", code)
	}
}

func mustDevice(t *testing.T, f *authFixture) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,$3, ('02:aa:' || substr(md5(random()::text),1,2) || ':' || substr(md5(random()::text),1,2) || ':' ||
		        substr(md5(random()::text),1,2) || ':' || substr(md5(random()::text),1,2))::macaddr) RETURNING id::text`,
		f.tenant, f.site, f.appliance).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
