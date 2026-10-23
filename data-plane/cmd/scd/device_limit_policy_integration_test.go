//go:build integration

package main

// DISCONNECT_OLDEST through the real activation handler as svc_scd: scd must not refuse at the limit before the database is asked.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func (f *ssFixture) newDeviceAcquiring(t *testing.T, ent, accountID, mac string) string {
	t.Helper()
	ctx := context.Background()
	var dev, ac string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`, f.tenant, f.site, mac).Scan(&dev); err != nil {
		t.Fatalf("device: %v", err)
	}
	f.ownerExec(t, `INSERT INTO iam_v2.device_network_appearances (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
		VALUES ($1,$2,$3,$4,now(),now())`, f.tenant, f.site, dev, f.network)
	tx, err := f.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, fam := range []string{"auth_context", "commerce_intent"} {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, fam); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts (tenant_id, site_id, method, guest_account_id, device_id,
		guest_network_id, expires_at, consumed_at) VALUES ($1,$2,'ACCOUNT',$3,$4,$5, now() + interval '10 minutes', now())
		RETURNING id::text`, f.tenant, f.site, accountID, dev, f.network).Scan(&ac); err != nil {
		t.Fatalf("auth context: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.purchases SET auth_context_id=$2
		 WHERE id = (SELECT purchase_id FROM iam_v2.entitlements WHERE id=$1)`, ent, ac); err != nil {
		t.Fatalf("acquisition chain: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return dev
}

func (f *ssFixture) limitPlan(t *testing.T, ent, policy string) {
	t.Helper()
	f.ownerExec(t, `WITH r AS (
	    INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps,
	      max_concurrent_devices, device_limit_policy)
	    SELECT spr.tenant_id, spr.site_id, spr.service_plan_id, spr.revision_no + 1, spr.down_kbps, spr.up_kbps, 1, $2
	      FROM iam_v2.service_plan_revisions spr JOIN iam_v2.entitlements e ON e.service_plan_revision_id = spr.id
	     WHERE e.id = $1::uuid RETURNING id)
	  UPDATE iam_v2.entitlements SET service_plan_revision_id = (SELECT id FROM r) WHERE id = $1::uuid`, ent, policy)
}

// activateQuick uses a short deadline: nothing promotes the session in this harness, so the answer carries the state reached.
func (f *ssFixture) activateQuick(t *testing.T, body map[string]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), iamv2EnforcementReserve+300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	f.s.activateIAMv2Session(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)).WithContext(ctx))
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestIntegration_DisconnectOldestAdmitsANewDeviceThroughActivation(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	var accountID string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash)
		VALUES ($1,$2,'oldest.'||$3||'@example.com','x') RETURNING id::text`, f.tenant, f.site, randHex(3)).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	ent, oldDev, oldSess := f.seedEntitlementSession(t, accountID, f.ip(40), "02:22:00:00:"+randHex(1)+":01")
	f.limitPlan(t, ent, "DISCONNECT_OLDEST")
	mac := "02:22:00:00:" + randHex(1) + ":02"
	newDev := f.newDeviceAcquiring(t, ent, accountID, mac)

	code, out := f.activateQuick(t, map[string]string{"entitlement_id": ent, "device_id": newDev, "ip": f.ip(41), "mac": mac})
	if code != http.StatusOK || out["session_id"] == nil || out["session_id"] == "" {
		t.Fatalf("DISCONNECT_OLDEST still refused the new device at scd: %d %v", code, out)
	}
	if st, reason := f.sessionEnd(t, oldSess); st != "ended" || reason != "DEVICE_LIMIT_OLDEST_DISCONNECTED" {
		t.Fatalf("the oldest device's session is %s/%s", st, reason)
	}
	var recorded int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM iam_v2.device_limit_disconnections
		 WHERE entitlement_id=$1 AND disconnected_device_id=$2 AND admitted_device_id=$3`, ent, oldDev, newDev).Scan(&recorded)
	if recorded != 1 {
		t.Fatal("the disconnection was not recorded")
	}
}

func TestIntegration_RejectNewDeviceStillRefusesThroughActivation(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	var accountID string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash)
		VALUES ($1,$2,'reject.'||$3||'@example.com','x') RETURNING id::text`, f.tenant, f.site, randHex(3)).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	ent, _, oldSess := f.seedEntitlementSession(t, accountID, f.ip(50), "02:23:00:00:"+randHex(1)+":01")
	f.limitPlan(t, ent, "REJECT_NEW_DEVICE")
	mac := "02:23:00:00:" + randHex(1) + ":02"
	newDev := f.newDeviceAcquiring(t, ent, accountID, mac)
	code, out := f.activateQuick(t, map[string]string{"entitlement_id": ent, "device_id": newDev, "ip": f.ip(51), "mac": mac})
	if code != http.StatusForbidden || out["error"] != "MAX_DEVICES_REACHED" {
		t.Fatalf("REJECT_NEW_DEVICE over the limit: %d %v", code, out)
	}
	if st, _ := f.sessionEnd(t, oldSess); st != "active" {
		t.Fatal("a refused admission disconnected the existing device")
	}
}
