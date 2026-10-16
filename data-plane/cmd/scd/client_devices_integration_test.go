//go:build integration

package main

// A CLIENT'S DEVICES (client_devices.go, migration 0118) against the full appliance schema, the server running
// as svc_scd: replace on a 1-device package, the package untouched, the daily limit, release from an online
// device, and the proofs a replacement needs (a fresh sign-in on THIS device for the package's subject).

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedLimitedEntitlement is seedEntitlementSession with a device limit of its own: an ACTIVE entitlement of the
// account whose first device (mac) is online.
func (f *ssFixture) seedLimitedEntitlement(t *testing.T, accountID, ip, mac string, limit int) (ent, device, session string) {
	t.Helper()
	ent, device, session = f.seedEntitlementSession(t, accountID, ip, mac)
	// The seeded plan allows 4; a fresh revision with the limit under test replaces it on this entitlement.
	ctx := context.Background()
	var rev string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no,
	     down_kbps, up_kbps, max_concurrent_devices, data_quota_bytes, time_quota_seconds)
	     SELECT tenant_id, site_id, service_plan_id, revision_no + 1, down_kbps, up_kbps, $2, data_quota_bytes, time_quota_seconds
	       FROM iam_v2.service_plan_revisions WHERE id = (SELECT service_plan_revision_id FROM iam_v2.entitlements WHERE id=$1::uuid)
	     RETURNING id::text`, ent, limit).Scan(&rev); err != nil {
		t.Fatalf("seed limited revision: %v", err)
	}
	tx, err := f.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`) // seeding only: the revision pin is immutable
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET service_plan_revision_id=$2::uuid WHERE id=$1::uuid`, ent, rev); err != nil {
		t.Fatalf("pin limited revision: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ent, device, session
}

func (f *ssFixture) seedDevice(t *testing.T, mac string) string {
	t.Helper()
	var id string
	if err := f.owner.QueryRow(context.Background(), `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
	     VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`, f.tenant, f.site, mac).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedSignIn is the auth context a sign-in on device leaves: the account, this device, an expiry.
func (f *ssFixture) seedSignIn(t *testing.T, accountID, device string, expiresIn time.Duration) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pgx.BeginFunc(ctx, f.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('auth_context')`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts (tenant_id, site_id, method, guest_account_id, device_id,
		     guest_network_id, expires_at) VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid, now() + make_interval(secs => $6))
		     RETURNING id::text`, f.tenant, f.site, accountID, device, f.network, expiresIn.Seconds()).Scan(&id)
	})
	if err != nil {
		t.Fatalf("seed sign-in: %v", err)
	}
	return id
}

func (f *ssFixture) seedAccount(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := f.owner.QueryRow(context.Background(), `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash)
	     VALUES ($1,$2,$3,'x') RETURNING id::text`, f.tenant, f.site, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type entSnapshot struct {
	status, window string
	data, online   int64
	account        string
}

func (f *ssFixture) entState(t *testing.T, ent string) entSnapshot {
	t.Helper()
	var e entSnapshot
	if err := f.owner.QueryRow(context.Background(), `SELECT status, window_ends_at::text, consumed_data_bytes,
	     consumed_online_seconds, guest_account_id::text FROM iam_v2.entitlements WHERE id=$1::uuid`, ent).
		Scan(&e.status, &e.window, &e.data, &e.online, &e.account); err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *ssFixture) binding(t *testing.T, ent, device string) (status string, open int) {
	t.Helper()
	ctx := context.Background()
	_ = f.owner.QueryRow(ctx, `SELECT COALESCE((SELECT status FROM iam_v2.entitlement_devices WHERE entitlement_id=$1::uuid AND device_id=$2::uuid),'')`,
		ent, device).Scan(&status)
	_ = f.owner.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.entitlement_device_authorizations
	     WHERE entitlement_id=$1::uuid AND device_id=$2::uuid AND deauthorized_at IS NULL`, ent, device).Scan(&open)
	return status, open
}

func TestIntegration_ClientReplacesADeviceOnAOneDevicePackage(t *testing.T) {
	f := newSSFixture(t)
	s := f.s
	ctx := context.Background()
	acc := f.seedAccount(t, "rep-"+randHex(3))
	mac1, mac2 := "02:18:00:01:"+randHex(1)+":01", "02:18:00:01:"+randHex(1)+":02"
	ent, d1, s1 := f.seedLimitedEntitlement(t, acc, f.ip(40), mac1, 1)
	d2 := f.seedDevice(t, mac2)
	before := f.entState(t, ent)

	// The new device has signed in; its sign-in names the package's subject.
	ac := f.seedSignIn(t, acc, d2, 10*time.Minute)
	code, out := f.post(t, s.clientDevicesList, map[string]string{"ip": f.ip(41), "mac": mac2, "auth_context_id": ac})
	devs, _ := out["devices"].([]any)
	if code != 200 || out["outcome"] != "LISTED" || out["limit"] != float64(1) || len(devs) != 1 {
		t.Fatalf("list for the new device = %d %v", code, out)
	}
	if dv := devs[0].(map[string]any); dv["id"] != d1 || dv["online"] != true || dv["this_device"] != false {
		t.Fatalf("the package's device = %v", dv)
	}

	// Another device's sign-in, or an expired one, proves nothing.
	other := f.seedSignIn(t, acc, f.seedDevice(t, "02:18:00:01:"+randHex(1)+":03"), 10*time.Minute)
	if _, o := f.post(t, s.clientDevicesReplace, map[string]string{"ip": f.ip(41), "mac": mac2, "auth_context_id": other, "device_id": d1}); o["outcome"] != "UNAVAILABLE" {
		t.Fatalf("a sign-in of another device replaced a device: %v", o)
	}
	expired := f.seedSignIn(t, acc, d2, -time.Minute)
	if _, o := f.post(t, s.clientDevicesReplace, map[string]string{"ip": f.ip(41), "mac": mac2, "auth_context_id": expired, "device_id": d1}); o["outcome"] != "UNAVAILABLE" {
		t.Fatalf("an expired sign-in replaced a device: %v", o)
	}
	if st, _ := f.sessionEnd(t, s1); st != "active" {
		t.Fatal("a refused replacement must not disconnect anything")
	}

	// REPLACE: device 1 goes offline and loses its place; device 2 holds it; the package is unchanged.
	code, out = f.post(t, s.clientDevicesReplace, map[string]string{"ip": f.ip(41), "mac": mac2, "auth_context_id": ac, "device_id": d1})
	if code != 200 || out["outcome"] != "REPLACED" || out["entitlement_id"] != ent || out["device_id"] != d2 {
		t.Fatalf("replace = %d %v", code, out)
	}
	if st, reason := f.sessionEnd(t, s1); st != "ended" || reason != endReasonDeviceReplaced {
		t.Fatalf("the replaced device must be disconnected: %s %s", st, reason)
	}
	if st, open := f.binding(t, ent, d1); st != "DISCONNECTED" || open != 0 {
		t.Fatalf("the replaced device keeps its place: %s open=%d", st, open)
	}
	if st, open := f.binding(t, ent, d2); st != "AUTHORIZED" || open != 1 {
		t.Fatalf("the new device must hold the place: %s open=%d", st, open)
	}
	if after := f.entState(t, ent); after != before {
		t.Fatalf("replacement changed the package: %+v -> %+v", before, after)
	}
	var audited int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM iam_v2.device_slot_actions WHERE entitlement_id=$1::uuid
	     AND actor='CLIENT' AND action='REPLACE' AND outcome='OK' AND device_id=$2::uuid AND replacement_device_id=$3::uuid`,
		ent, d1, d2).Scan(&audited)
	if audited != 1 {
		t.Fatal("the replacement is recorded")
	}
	var refusals int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM iam_v2.device_slot_actions WHERE entitlement_id IS NULL OR outcome <> 'OK'`).Scan(&refusals)
	if refusals < 2 {
		t.Fatal("refused attempts are recorded too")
	}

	// THE DAILY LIMIT: with one change a day, device 1 cannot take the place straight back.
	if _, err := f.owner.Exec(ctx, `SELECT iam_v2.device_settings_set($1,$2,1,'test','one change a day')`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	back := f.seedSignIn(t, acc, d1, 10*time.Minute)
	if _, o := f.post(t, s.clientDevicesList, map[string]string{"ip": f.ip(40), "mac": mac1, "auth_context_id": back}); o["changes_left"] != float64(0) {
		t.Fatalf("changes left after today's one change = %v", o["changes_left"])
	}
	// Device 2 is online on the package; a refused change must not disconnect it.
	var s2 string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.sessions (tenant_id, site_id, entitlement_id, device_id, ip, mac, state,
	     credential_method, started, expires_at) VALUES ($1,$2,$3::uuid,$4::uuid,$5::inet,$6::macaddr,'active','ACCOUNT',now(), now()+interval '1 day')
	     RETURNING id::text`, f.tenant, f.site, ent, d2, f.ip(41), mac2).Scan(&s2); err != nil {
		t.Fatal(err)
	}
	if _, o := f.post(t, s.clientDevicesReplace, map[string]string{"ip": f.ip(40), "mac": mac1, "auth_context_id": back, "device_id": d2}); o["outcome"] != "LIMIT_TODAY" {
		t.Fatalf("a second change today = %v", o)
	}
	if st, _ := f.sessionEnd(t, s2); st != "active" {
		t.Fatal("a change refused for today's limit disconnected the target")
	}
	if st, open := f.binding(t, ent, d2); st != "AUTHORIZED" || open != 1 {
		t.Fatal("a refused change must not move the place")
	}
	// The setting's own bounds hold in the database.
	if _, err := f.owner.Exec(ctx, `SELECT iam_v2.device_settings_set($1,$2,11,'test','above the hard maximum')`, f.tenant, f.site); err == nil {
		t.Fatal("the daily limit accepted 11, above the hard maximum of 10")
	}
}

func TestIntegration_ClientReleasesAnotherDeviceFromTheOnlinePage(t *testing.T) {
	f := newSSFixture(t)
	s := f.s
	acc := f.seedAccount(t, "rel-"+randHex(3))
	mac1, mac2 := "02:18:00:02:"+randHex(1)+":01", "02:18:00:02:"+randHex(1)+":02"
	ent, _, _ := f.seedLimitedEntitlement(t, acc, f.ip(50), mac1, 2)
	d2 := f.seedDevice(t, mac2)
	// Device 2 used the package earlier and signed out: it holds a place, offline.
	ac := f.seedSignIn(t, acc, d2, 10*time.Minute)
	if err := pgx.BeginFunc(context.Background(), f.owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `SELECT iam_v2.authorize_entitlement_device($1::uuid,$2::uuid,now())`, ent, d2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = ac
	before := f.entState(t, ent)

	_, out := f.post(t, s.clientDevicesList, map[string]string{"ip": f.ip(50), "mac": mac1})
	devs, _ := out["devices"].([]any)
	if out["outcome"] != "LISTED" || len(devs) != 2 || out["in_use"] != float64(2) {
		t.Fatalf("list from the online device = %v", out)
	}
	// A device that is not online on a package has nothing to list without a sign-in.
	if _, o := f.post(t, s.clientDevicesList, map[string]string{"ip": f.ip(51), "mac": mac2}); o["outcome"] != "UNAVAILABLE" {
		t.Fatalf("an offline device listed a package without proof: %v", o)
	}
	_, out = f.post(t, s.clientDevicesRelease, map[string]string{"ip": f.ip(50), "mac": mac1, "device_id": d2})
	if out["outcome"] != "RELEASED" {
		t.Fatalf("release = %v", out)
	}
	if st, open := f.binding(t, ent, d2); st != "DISCONNECTED" || open != 0 {
		t.Fatalf("the released device keeps its place: %s open=%d", st, open)
	}
	if after := f.entState(t, ent); after != before {
		t.Fatalf("release changed the package: %+v -> %+v", before, after)
	}
}
