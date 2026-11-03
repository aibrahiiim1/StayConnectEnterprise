//go:build integration

package main

// The one-time Full Stay grant (scripts/sql/onetime-fullstay-grant.sql): a stay on Free moves to Full Stay with its session kept,
// a stay without access gets Full Stay that its first room sign-in joins, and nothing anywhere is charged.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// moneyCensus counts every row a charge, quote, settlement, posting or payment would leave.
func moneyCensus(t *testing.T, p *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM iam_v2.offer_quotes) + (SELECT count(*) FROM iam_v2.settlements) +
		(SELECT count(*) FROM iam_v2.pms_postings) + (SELECT count(*) FROM iam_v2.posting_outbox) +
		(SELECT count(*) FROM iam_v2.posting_attempts) + (SELECT count(*) FROM iam_v2.payment_transactions) +
		(SELECT count(*) FROM iam_v2.payment_checkouts) +
		(SELECT count(*) FROM iam_v2.purchases WHERE amount_minor <> 0)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegration_OnetimeFullStayGrant(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	src, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "sql", "onetime-fullstay-grant.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// pg_temp lives for one session, so the function is loaded and called on one connection, as psql does on the appliance.
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, string(src)); err != nil {
		t.Fatalf("load the script: %v", err)
	}

	// Full Stay as it is on the appliance: priced, room-charge only, switched off for sale, lasting until the stay ends.
	var target, freePkg, operator string
	if err := f.pool.QueryRow(ctx, `WITH
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled) VALUES (gen_random_uuid(),$1,$2,'fs-plan',true) RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,
	                                                    max_concurrent_devices,device_limit_policy,time_accounting_mode)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,10000,10000,10,'REJECT_NEW_DEVICE','VALIDITY_WINDOW' FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,'fullstay',false,false) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
	                                                        package_type,price_minor,currency,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'GENERAL',1000,'USD',ARRAY['PMS_POSTING']::text[],
	                 '{"end_mode":"MANUAL_END"}'::jsonb FROM ip, spr RETURNING id, package_id)
	SELECT (SELECT id FROM ipr)::text`, f.tenant, f.site).Scan(&target); err != nil {
		t.Fatalf("seed Full Stay: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages ip SET current_revision_id=r.id
		FROM iam_v2.internet_package_revisions r WHERE r.id=$1 AND ip.id=r.package_id`, target); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT package_id::text FROM iam_v2.internet_package_revisions WHERE id=$1`, f.pkgRev).Scan(&freePkg); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO public.operators(tenant_id,email,display_name,password_hash,status)
		VALUES ($1,'onetime-'||substr(md5(random()::text),1,8),'Migration','$argon2id$fixture','active') RETURNING id::text`,
		f.tenant).Scan(&operator); err != nil {
		t.Fatal(err)
	}
	grant := func(stay string) (outcome, oldEnt, newEnt string, devices, sessions int, err error) {
		var o, n *string
		err = conn.QueryRow(ctx, `SELECT outcome, old_entitlement::text, new_entitlement::text, devices, sessions
			FROM pg_temp.onetime_fullstay_grant($1,$2,$3,$4,$5,$6,$7,'paid Full Stay on the previous system')`,
			f.tenant, f.site, stay, f.iface, freePkg, target, operator).Scan(&outcome, &o, &n, &devices, &sessions)
		if o != nil {
			oldEnt = *o
		}
		if n != nil {
			newEnt = *n
		}
		return
	}

	// The room is on Free with a device online.
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000f501-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	_, g := f.grantVia(t, res.AuthContextID, f.net.guestIP, f.net.mac)
	if g.Outcome != outcomeVerified || g.SessionID == "" {
		t.Fatalf("setup: Free was not granted: %+v", g)
	}
	money := moneyCensus(t, f.pool)

	// A refusal writes nothing: the wrong PMS interface.
	if _, err := conn.Exec(ctx, `SELECT * FROM pg_temp.onetime_fullstay_grant($1,$2,$3,gen_random_uuid(),$4,$5,$6,'wrong interface')`,
		f.tenant, f.site, f.stay, freePkg, target, operator); err == nil || !strings.Contains(err.Error(), "ONETIME_REFUSED") {
		t.Fatalf("a stay on another interface was not refused: %v", err)
	}

	outcome, oldEnt, newEnt, devices, sessions, err := grant(f.stay)
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if outcome != "SWITCHED" || oldEnt != g.EntitlementID || devices != 1 || sessions != 1 {
		t.Fatalf("switch: outcome %s old %s devices %d sessions %d", outcome, oldEnt, devices, sessions)
	}
	var oldStatus, oldReason, newRev, newStatus, sessEnt, purTrigger, purState string
	var purAmount int64
	var quota *int64
	var sessEnded bool
	var live int
	if err := f.pool.QueryRow(ctx, `SELECT o.status, o.terminal_reason, n.package_revision_id::text, n.status, n.data_quota_bytes,
		       (SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$3 AND status IN ('PENDING','ACTIVE','SUSPENDED')),
		       s.entitlement_id::text, s.ended IS NOT NULL, pu.trigger, pu.state, pu.amount_minor
		  FROM iam_v2.entitlements o, iam_v2.entitlements n, iam_v2.sessions s, iam_v2.purchases pu
		 WHERE o.id=$1 AND n.id=$2 AND s.id=$4 AND pu.id=n.purchase_id`, oldEnt, newEnt, f.stay, g.SessionID).
		Scan(&oldStatus, &oldReason, &newRev, &newStatus, &quota, &live, &sessEnt, &sessEnded, &purTrigger, &purState, &purAmount); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "TERMINATED" || oldReason != "SUPERSEDED" || newRev != target || newStatus != "ACTIVE" || quota != nil || live != 1 {
		t.Fatalf("entitlements: old %s/%s new %s/%s quota %v live %d", oldStatus, oldReason, newRev, newStatus, quota, live)
	}
	if sessEnded || sessEnt != newEnt {
		t.Fatalf("the online device was disconnected or left on Free: ended=%v entitlement=%s", sessEnded, sessEnt)
	}
	if purTrigger != "ADMIN_GRANT" || purState != "GRANTED" || purAmount != 0 {
		t.Fatalf("purchase %s %s %d, want a zero-price ADMIN_GRANT", purTrigger, purState, purAmount)
	}

	// Again: nothing changes.
	again, _, sameEnt, _, _, err := grant(f.stay)
	if err != nil || again != "ALREADY_GRANTED" || sameEnt != newEnt {
		t.Fatalf("a repeat changed something: %s %s %v", again, sameEnt, err)
	}

	// A stay that has not signed in yet: granted with no device, and its first room sign-in joins it with no choice.
	var fresh string
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `WITH st AS (
		INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,normalized_room_number,
		       status,lifecycle_version,last_applied_event_version,occupancy_evidence_at,occupancy_ingested_at,occupancy_revision_id,
		       occupancy_normalization_version,occupancy_clock_suspect,occupancy_evidence_version,arrival,departure)
		SELECT gen_random_uuid(),tenant_id,site_id,pms_interface_id,'RES-4013','STAY-4013','413','IN_HOUSE',1,0,occupancy_evidence_at,
		       occupancy_ingested_at,occupancy_revision_id,occupancy_normalization_version,occupancy_clock_suspect,occupancy_evidence_version,
		       arrival,departure FROM iam_v2.stays WHERE id=$1 RETURNING id,tenant_id,site_id,pms_interface_id),
	  sg AS (INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	         SELECT tenant_id,site_id,pms_interface_id,id,'ADEYEMI',true FROM st RETURNING stay_id)
	SELECT stay_id::text FROM sg`, f.stay).Scan(&fresh); err != nil {
		t.Fatalf("seed a stay without access: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The first call on a fresh connection is a no-access grant: nothing earlier has given the function's record its shape.
	conn2, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Release()
	if _, err := conn2.Exec(ctx, string(src)); err != nil {
		t.Fatal(err)
	}
	var freshEnt string
	var oldPtr *string
	if err := conn2.QueryRow(ctx, `SELECT outcome, old_entitlement::text, new_entitlement::text, devices
		FROM pg_temp.onetime_fullstay_grant($1,$2,$3,$4,$5,$6,$7,'paid Full Stay on the previous system')`,
		f.tenant, f.site, fresh, f.iface, freePkg, target, operator).Scan(&outcome, &oldPtr, &freshEnt, &devices); err != nil {
		t.Fatalf("a no-access grant as the connection's first call: %v", err)
	}
	if outcome != "GRANTED" || oldPtr != nil || devices != 0 {
		t.Fatalf("grant without access: %s %v %d", outcome, oldPtr, devices)
	}
	body := f.resolveBody("413", "Adeyemi", "", "0000f502-0000-4000-8000-000000000000")
	body["device"] = map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:f5:00:02"}
	_, res2 := post(t, f.p3.resolveHandler, body)
	if res2.Outcome != outcomeVerified || !res2.LiveAccess || len(res2.Offers) != 0 {
		t.Fatalf("the granted stay was offered packages instead of joining: %+v", res2)
	}
	raw, _ := json.Marshal(map[string]any{"auth_context_id": res2.AuthContextID, "join_live": true, "device": body["device"]})
	rec := httptest.NewRecorder()
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var joined phase3GrantResp
	if err := json.Unmarshal(rec.Body.Bytes(), &joined); err != nil || joined.EntitlementID != freshEnt || joined.SessionID == "" {
		t.Fatalf("the first sign-in did not join Full Stay: %s", rec.Body.String())
	}

	if after := moneyCensus(t, f.pool); after != money {
		t.Fatalf("money rows went from %d to %d: the grant must charge, quote, settle and post nothing", money, after)
	}
	var audits int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_log WHERE action='package.onetime_migration_granted'
		AND target_id IN ($1,$2) AND payload->>'amount_minor' = '0'`, f.stay, fresh).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit rows %d (%v), want one per granted stay", audits, err)
	}
}
