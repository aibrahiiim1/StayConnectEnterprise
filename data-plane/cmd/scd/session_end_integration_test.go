//go:build integration

package main

// Ending sessions as svc_scd (the sessions guard refuses a non-owner raw UPDATE out of PENDING_ENFORCEMENT, so scd ends them through
// iam_v2.end_session_enforcement) and the reconnect shortcut (a device returning at a NEW address must not keep its old session), on the full schema.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedPendingSession is seedEntitlementSession whose session is still PENDING_ENFORCEMENT (netd never promoted it).
func (f *ssFixture) seedPendingSession(t *testing.T, accountID, ip, mac string) (ent, device, session string) {
	t.Helper()
	ent, device, _ = f.seedEntitlementSession(t, accountID, ip, mac)
	// The seeded live session is replaced by a pending one: end it as the owner, then open the pending row.
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, f.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil { // seeding only
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='SEED'
		     WHERE entitlement_id=$1::uuid`, ent); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.sessions (tenant_id, site_id, entitlement_id, device_id, ip, mac, state,
		     credential_method, started) VALUES ($1,$2,$3,$4,$5::inet,$6::macaddr,'PENDING_ENFORCEMENT','EMAIL_ACCOUNT',now())
		     RETURNING id::text`, f.tenant, f.site, ent, device, ip, mac).Scan(&session)
	})
	if err != nil {
		t.Fatalf("seed pending session: %v", err)
	}
	return ent, device, session
}

func (f *ssFixture) liveSessionsOn(t *testing.T, ent string) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*)::int FROM iam_v2.sessions
	     WHERE entitlement_id=$1::uuid AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT')`, ent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegration_RevokeEndsAPendingEnforcementSession(t *testing.T) {
	f := newSSFixture(t)
	acc := f.seedAccount(t, "pend-"+randHex(3))
	ent, _, session := f.seedPendingSession(t, acc, f.ip(50), "02:19:00:00:"+randHex(1)+":01")

	// The raw UPDATE is what the guard refuses for svc_scd.
	if _, err := f.s.db.Exec(context.Background(), `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='admin'
	     WHERE id=$1::uuid`, session); err == nil {
		t.Fatal("expected the sessions guard to refuse a raw svc_scd UPDATE out of PENDING_ENFORCEMENT")
	}

	code, out := f.post(t, f.s.revoke, map[string]string{"ip": f.ip(50), "reason": "admin"})
	if code != http.StatusOK || out["status"] != "revoked" {
		t.Fatalf("revoke of a pending session: %d %v", code, out)
	}
	if st, reason := f.sessionEnd(t, session); st != "ended" || reason != "ADMIN" {
		t.Fatalf("the pending session must end: %s %s", st, reason)
	}
	if n := f.liveSessionsOn(t, ent); n != 0 {
		t.Fatalf("%d live sessions still hold capacity", n)
	}
	// Idempotent: revoking again changes nothing and is not an error.
	if code, _ := f.post(t, f.s.revoke, map[string]string{"ip": f.ip(50), "reason": "admin"}); code != http.StatusOK {
		t.Fatalf("second revoke: %d", code)
	}
}

func TestIntegration_RevokeClampsAnUnknownReason(t *testing.T) {
	f := newSSFixture(t)
	acc := f.seedAccount(t, "rsn-"+randHex(3))
	_, _, session := f.seedEntitlementSession(t, acc, f.ip(51), "02:19:00:00:"+randHex(1)+":02")
	if code, _ := f.post(t, f.s.revoke, map[string]string{"ip": f.ip(51), "reason": "made-up reason!"}); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	if st, reason := f.sessionEnd(t, session); st != "ended" || reason != "ADMIN" {
		t.Fatalf("an active session ends with the clamped reason: %s %s", st, reason)
	}
}

func TestIntegration_EndSessionNowEndsAPendingEnforcementSession(t *testing.T) {
	f := newSSFixture(t)
	acc := f.seedAccount(t, "pnow-"+randHex(3))
	ent, _, session := f.seedPendingSession(t, acc, f.ip(52), "02:19:00:00:"+randHex(1)+":03")
	if err := f.s.endSessionNow(context.Background(), session, nil, endReasonDeviceReleased); err != nil {
		t.Fatalf("endSessionNow on a pending session: %v", err)
	}
	if st, reason := f.sessionEnd(t, session); st != "ended" || reason != endReasonDeviceReleased {
		t.Fatalf("pending session: %s %s", st, reason)
	}
	if n := f.liveSessionsOn(t, ent); n != 0 {
		t.Fatalf("%d live sessions", n)
	}
	if err := f.s.endSessionNow(context.Background(), session, nil, endReasonDeviceReleased); err != nil {
		t.Fatalf("ending an ended session again: %v", err)
	}
}

// An ACTIVE package of the account acquired by a device seen on the fixture guest network and holding its place, as activation requires.
func (f *ssFixture) seedAcquiredEntitlement(t *testing.T, accountID, mac string) (ent, device string) {
	t.Helper()
	ctx := context.Background()
	p := f.owner
	device = f.seedDevice(t, mac)
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.device_network_appearances (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
	     VALUES ($1,$2,$3::uuid,$4::uuid,now(),now())`, f.tenant, f.site, device, f.network); err != nil {
		t.Fatalf("seed appearance: %v", err)
	}
	ac := f.seedSignIn(t, accountID, device, time.Hour)
	tag := randHex(4)
	var svcPlan, svcRev, pkg, pkgRev, purchase string
	must := func(err error, what string) {
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.service_plans (tenant_id, site_id, code) VALUES ($1,$2,'plan-'||$3) RETURNING id::text`,
		f.tenant, f.site, tag).Scan(&svcPlan), "plan")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps,
		max_concurrent_devices, data_quota_bytes, time_quota_seconds) VALUES ($1,$2,$3,1,10000,5000,2,5000000000,86400) RETURNING id::text`,
		f.tenant, f.site, svcPlan).Scan(&svcRev), "plan revision")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code) VALUES ($1,$2,'pkg-'||$3) RETURNING id::text`,
		f.tenant, f.site, tag).Scan(&pkg), "package")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.internet_package_revisions (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type)
		VALUES ($1,$2,$3,1,$4,'FREE_STAY') RETURNING id::text`, f.tenant, f.site, pkg, svcRev).Scan(&pkgRev), "package revision")
	must(pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, trigger, auth_context_id)
		     VALUES ($1,$2,$3,'ADMIN_GRANT',$4::uuid) RETURNING id::text`, f.tenant, f.site, pkgRev, ac).Scan(&purchase); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements (tenant_id, site_id, purchase_id, guest_account_id, policy_snapshot,
		     service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, window_ends_at)
		     VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW', now() + interval '1 day') RETURNING id::text`,
			f.tenant, f.site, purchase, accountID, svcRev, pkgRev).Scan(&ent); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '1 minute','ADMIN_GRANT')`, ent)
		return err
	}), "entitlement")
	return ent, device
}

// activate posts /v1/sessions/activate with a short deadline: no netd runs here, so the session stays
// PENDING_ENFORCEMENT and the handler answers when its wait budget ends.
func (f *ssFixture) activate(t *testing.T, ent, device, ip, mac string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"entitlement_id": ent, "device_id": device, "ip": ip, "mac": mac})
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	f.s.activateIAMv2Session(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)).WithContext(ctx))
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f *ssFixture) sessionIP(t *testing.T, id string) string {
	t.Helper()
	var ip string
	if err := f.owner.QueryRow(context.Background(), `SELECT host(ip) FROM iam_v2.sessions WHERE id=$1::uuid`, id).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	return ip
}

func TestIntegration_ReconnectAtANewAddressGetsASessionAtThatAddress(t *testing.T) {
	f := newSSFixture(t)
	acc := f.seedAccount(t, "repin-"+randHex(3))
	mac := "02:19:00:01:" + randHex(1) + ":01"
	ent, device := f.seedAcquiredEntitlement(t, acc, mac)

	code, out := f.activate(t, ent, device, f.ip(60), mac)
	if code != http.StatusOK || out["reused"] != false {
		t.Fatalf("first activation: %d %v", code, out)
	}
	first := out["session_id"].(string)

	// Same address again: the idempotent shortcut still answers with the same session.
	code, out = f.activate(t, ent, device, f.ip(60), mac)
	if code != http.StatusOK || out["reused"] != true || out["session_id"] != first {
		t.Fatalf("same-address reconnect: %d %v", code, out)
	}

	// A new address (DHCP gave the device another lease): a session pinned to the NEW address.
	code, out = f.activate(t, ent, device, f.ip(61), mac)
	if code != http.StatusOK || out["reused"] != false || out["session_id"] == first {
		t.Fatalf("new-address reconnect must not reuse the old session: %d %v", code, out)
	}
	second := out["session_id"].(string)
	if ip := f.sessionIP(t, second); ip != f.ip(61) {
		t.Fatalf("the new session is pinned to %s, want %s", ip, f.ip(61))
	}
	if st, reason := f.sessionEnd(t, first); st != "ended" || reason != endReasonAddressChanged {
		t.Fatalf("the old-address session must end: %s %s", st, reason)
	}
	if n := f.liveSessionsOn(t, ent); n != 1 {
		t.Fatalf("one device, %d live sessions", n)
	}
	// The device kept its one place on the package: no second authorization interval was opened.
	if status, open := f.binding(t, ent, device); status != "AUTHORIZED" || open != 1 {
		t.Fatalf("binding after the re-pin: %s open=%d", status, open)
	}
	// And at the new address it is idempotent again.
	if code, out := f.activate(t, ent, device, f.ip(61), mac); code != http.StatusOK || out["session_id"] != second || out["reused"] != true {
		t.Fatalf("reconnect at the new address: %d %v", code, out)
	}
}
