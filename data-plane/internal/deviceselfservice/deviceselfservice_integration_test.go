//go:build integration && phase6

package deviceselfservice

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The Phase-6 device self-service surface, against a real PostgreSQL carrying the authoritative chain.
//
// These are written as ATTACKS wherever there is something to attack. The happy path is one test; the rest
// ask what a guest who is not being honest can reach.

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// macFromUUID derives a locally-administered MAC from a device uuid. Device identity is
// (tenant, site, appliance, MAC), so two fixtures sharing a MAC would be the SAME device -- and that
// uniqueness outlives the test process, which is why this is derived from the id rather than counted.
func macFromUUID(id, prefix string) string {
	h := strings.ReplaceAll(id, "-", "")
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s", prefix, h[0:2], h[2:4], h[4:6], h[6:8], h[8:10])
}

const (
	fixTenant    = "11111111-1111-1111-1111-111111111111"
	fixSite      = "22222222-2222-2222-2222-222222222222"
	fixAppliance = "44444444-4444-4444-4444-444444444444"
	fixOperator  = "55555555-5555-5555-5555-555555555555"
)

// seedEntitlement makes one live entitlement and returns its id.
func seedEntitlement(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("replica: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL`); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlements
		(id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, status)
		VALUES ($1,$2,$3, gen_random_uuid(), gen_random_uuid(), '{}'::jsonb, gen_random_uuid(),
		        gen_random_uuid(), 'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE')`,
		id, fixTenant, fixSite); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL`); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	t.Cleanup(func() { cleanup(t, p, id) })
	return id
}

// seedDevice binds a device to an entitlement, optionally with a live session.
func seedDevice(t *testing.T, p *pgxpool.Pool, ent, sessionState string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("replica: %v", err)
	}
	// The MAC is DERIVED FROM THE DEVICE UUID, in Go. Two earlier attempts were wrong for different reasons:
	// building it in SQL from $1 made PostgreSQL deduce two types for one parameter, and a process-local
	// counter collided with devices left behind by previous runs, because device identity is
	// (tenant, site, appliance, MAC) and that uniqueness outlives any one test process.
	mac := macFromUUID(id, "02")
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac, last_seen)
		VALUES ($1,$2,$3,$4,$5::macaddr, now())`, id, fixTenant, fixSite, fixAppliance, mac); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id, site_id, entitlement_id, device_id, status, first_authorized, last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED', now(), now())`, fixTenant, fixSite, ent, id); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
		VALUES ($1,$2,$3,$4, 1, now())`, fixTenant, fixSite, ent, id); err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
	if sessionState != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.sessions
			(id, tenant_id, site_id, entitlement_id, device_id, state, started)
			VALUES (gen_random_uuid(),$1,$2,$3,$4,$5, now())`,
			fixTenant, fixSite, ent, id, sessionState); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

// seedEntitlementWithLimit makes a live entitlement whose PINNED plan revision carries a real
// max_concurrent_devices, so authorize_entitlement_device enforces a genuine limit rather than none.
func seedEntitlementWithLimit(t *testing.T, p *pgxpool.Pool, limit int) string {
	t.Helper()
	ctx := context.Background()
	var ent, spr string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&ent, &spr); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE iam_v2.service_plan_revisions DISABLE TRIGGER ALL`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.service_plan_revisions
		(id, tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps,
		 max_concurrent_devices, device_limit_policy, idle_timeout_seconds, time_accounting_mode)
		VALUES ($1,$2,$3, gen_random_uuid(), 1, 'limit-fixture', 1000, 1000, $4, 'REJECT_NEW_DEVICE', 900,
		        'VALIDITY_WINDOW')`, spr, fixTenant, fixSite, limit); err != nil {
		t.Fatalf("seed plan revision: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE iam_v2.service_plan_revisions ENABLE TRIGGER ALL`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlements
		(id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, status)
		VALUES ($1,$2,$3, gen_random_uuid(), gen_random_uuid(), '{}'::jsonb, $4, gen_random_uuid(),
		        'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE')`, ent, fixTenant, fixSite, spr); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup(t, p, ent)
		tx, err := p.Begin(ctx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
		_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.service_plan_revisions WHERE id=$1`, spr)
		_ = tx.Commit(ctx)
	})
	return ent
}

// seedBareDevice makes a device with no binding at all: a genuine newcomer.
func seedBareDevice(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	mac := macFromUUID(id, "06")
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac, last_seen)
		VALUES ($1,$2,$3,$4,$5::macaddr, now())`, id, fixTenant, fixSite, fixAppliance, mac); err != nil {
		t.Fatalf("seed bare device: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(ctx, `DELETE FROM iam_v2.devices WHERE id=$1`, id) })
	return id
}

func cleanup(t *testing.T, p *pgxpool.Pool, ent string) {
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
	_, _ = tx.Exec(ctx, `ALTER TABLE iam_v2.guest_device_actions DISABLE TRIGGER p6_guest_device_actions_append_only`)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.guest_device_actions WHERE entitlement_id=$1`, ent)
	_, _ = tx.Exec(ctx, `ALTER TABLE iam_v2.guest_device_actions ENABLE TRIGGER p6_guest_device_actions_append_only`)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.sessions WHERE entitlement_id=$1`, ent)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1`, ent)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.devices d WHERE d.id IN
		(SELECT device_id FROM iam_v2.entitlement_devices WHERE entitlement_id=$1)`, ent)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.entitlement_devices WHERE entitlement_id=$1`, ent)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.entitlements WHERE id=$1`, ent)
	_ = tx.Commit(ctx)
}

// ---------------------------------------------------------------------------------------------------------
// The per-appliance setting

// An appliance nobody configured has not opted in. If a missing row read as "on", every unconfigured
// appliance in a fleet would be serving a guest-facing capability the hotel never asked for.
func TestSettingDefaultsOffWhenNoRowExists(t *testing.T) {
	p := pool(t)
	s := New(p)
	var unknown string
	if err := p.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	on, err := s.EnabledForAppliance(context.Background(), fixTenant, fixSite, unknown)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if on {
		t.Fatal("an appliance with no settings row read as ENABLED")
	}
}

// The setting and its audit move together or not at all.
func TestSettingChangeIsAudited(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	t.Cleanup(func() {
		tx, err := p.Begin(ctx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, _ = tx.Exec(ctx, `ALTER TABLE iam_v2.appliance_product_setting_changes DISABLE TRIGGER p6_setting_changes_append_only`)
		_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.appliance_product_setting_changes WHERE appliance_id=$1`, fixAppliance)
		_, _ = tx.Exec(ctx, `ALTER TABLE iam_v2.appliance_product_setting_changes ENABLE TRIGGER p6_setting_changes_append_only`)
		_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.appliance_product_settings WHERE appliance_id=$1`, fixAppliance)
		_ = tx.Commit(ctx)
	})

	if err := s.SetForAppliance(ctx, fixTenant, fixSite, fixAppliance, true,
		fixOperator, "Fixture Operator", "enabling for a test"); err != nil {
		t.Fatalf("set: %v", err)
	}
	on, err := s.EnabledForAppliance(ctx, fixTenant, fixSite, fixAppliance)
	if err != nil || !on {
		t.Fatalf("setting did not take effect: on=%v err=%v", on, err)
	}
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.appliance_product_setting_changes
		WHERE appliance_id=$1 AND new_value = true AND changed_by_operator_id=$2`,
		fixAppliance, fixOperator).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one audit row naming the authenticated operator, got %d", n)
	}
}

// An appliance id that does not exist cannot acquire managed state, however it is presented.
func TestSettingRefusesUnknownAppliance(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	var fake string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&fake); err != nil {
		t.Fatal(err)
	}
	if err := s.SetForAppliance(ctx, fixTenant, fixSite, fake, true,
		fixOperator, "Fixture Operator", ""); err == nil {
		t.Fatal("managed state was created for an appliance that does not exist")
	}
}

// ---------------------------------------------------------------------------------------------------------
// The guest surface

// Only the caller's own devices are visible, and the query is what enforces it.
func TestListShowsOnlyTheCallersOwnDevices(t *testing.T) {
	p := pool(t)
	s := New(p)
	mine := seedEntitlement(t, p)
	theirs := seedEntitlement(t, p)
	d1 := seedDevice(t, p, mine, "")
	_ = seedDevice(t, p, mine, "active")
	other := seedDevice(t, p, theirs, "")

	got, err := s.ListOwnDevices(context.Background(), mine)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 devices on the caller's entitlement, got %d", len(got))
	}
	for _, d := range got {
		if d.ID == other {
			t.Fatal("another entitlement's device appeared in the caller's list")
		}
	}
	var offline, online int
	for _, d := range got {
		if d.Online {
			online++
			if d.Removable {
				t.Fatal("an ONLINE device was advertised as removable")
			}
		} else {
			offline++
			if d.ID == d1 && !d.Removable {
				t.Fatal("an offline device was not advertised as removable")
			}
		}
	}
	if online != 1 || offline != 1 {
		t.Fatalf("expected one online and one offline, got %d/%d", online, offline)
	}
}

// The listing carries no MAC address. A guest does not need one, and it is a stable network identifier for a
// device on a shared network.
func TestListCarriesNoMACAddress(t *testing.T) {
	p := pool(t)
	s := New(p)
	ent := seedEntitlement(t, p)
	seedDevice(t, p, ent, "")
	got, err := s.ListOwnDevices(context.Background(), ent)
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v (%d devices)", err, len(got))
	}
	// The struct has no MAC field at all; this asserts the shape stays that way if somebody adds one.
	var d any = got[0]
	if _, ok := d.(interface{ MAC() string }); ok {
		t.Fatal("Device grew a MAC accessor")
	}
}

// Online and converging devices are not removable, and the two are refused identically.
func TestOnlineAndPendingEnforcementAreNotRemovable(t *testing.T) {
	p := pool(t)
	s := New(p)
	ent := seedEntitlement(t, p)
	online := seedDevice(t, p, ent, "active")
	pending := seedDevice(t, p, ent, "PENDING_ENFORCEMENT")

	for name, dev := range map[string]string{"active": online, "PENDING_ENFORCEMENT": pending} {
		out, err := s.Release(context.Background(), ent, dev)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != OutcomeRefusedOnline {
			t.Fatalf("%s device was released or mis-refused: %s", name, out)
		}
	}
}

// A device belonging to somebody else is refused exactly as one that never existed: there is no oracle.
func TestAnotherGuestsDeviceIsIndistinguishableFromNothing(t *testing.T) {
	p := pool(t)
	s := New(p)
	mine := seedEntitlement(t, p)
	theirs := seedEntitlement(t, p)
	other := seedDevice(t, p, theirs, "")
	ctx := context.Background()

	var nowhere string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&nowhere); err != nil {
		t.Fatal(err)
	}
	outOther, err := s.Release(ctx, mine, other)
	if err != nil {
		t.Fatal(err)
	}
	outNowhere, err := s.Release(ctx, mine, nowhere)
	if err != nil {
		t.Fatal(err)
	}
	if outOther != OutcomeRefusedNotFound || outNowhere != OutcomeRefusedNotFound {
		t.Fatalf("the two refusals differ: other=%s nowhere=%s", outOther, outNowhere)
	}

	var status string
	if err := p.QueryRow(ctx, `SELECT status FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND device_id=$2`, theirs, other).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "AUTHORIZED" {
		t.Fatalf("the other guest's binding was modified: %s", status)
	}
}

// The whole point: an offline device releases, exactly once, and nothing durable is destroyed.
func TestOfflineReleaseFreesTheSlotExactlyOnceAndKeepsHistory(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	ent := seedEntitlement(t, p)
	dev := seedDevice(t, p, ent, "")

	out, err := s.Release(ctx, ent, dev)
	if err != nil || out != OutcomeOK {
		t.Fatalf("release: out=%s err=%v", out, err)
	}
	again, err := s.Release(ctx, ent, dev)
	if err != nil {
		t.Fatal(err)
	}
	if again != OutcomeRefusedAlready {
		t.Fatalf("a second release was not refused: %s", again)
	}

	var devices, intervals, openIntervals, audits int
	if err := p.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM iam_v2.devices WHERE id=$2),
		(SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND device_id=$2),
		(SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NULL),
		(SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id=$1 AND device_id=$2)`,
		ent, dev).Scan(&devices, &intervals, &openIntervals, &audits); err != nil {
		t.Fatal(err)
	}
	if devices != 1 {
		t.Fatal("the device identity was destroyed by a slot release")
	}
	if intervals != 1 {
		t.Fatal("the authorization interval was deleted rather than closed")
	}
	if openIntervals != 0 {
		t.Fatal("the authorization interval was left OPEN against a released binding")
	}
	if audits != 2 {
		t.Fatalf("expected the release and its refused repeat to be audited, got %d rows", audits)
	}
}

// The slot really is available again: a replacement device can take it.
func TestReleasedSlotIsAvailableForAReplacement(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	ent := seedEntitlement(t, p)
	old := seedDevice(t, p, ent, "")

	var authorized int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND status='AUTHORIZED'`, ent).Scan(&authorized); err != nil {
		t.Fatal(err)
	}
	if authorized != 1 {
		t.Fatalf("setup: expected 1 authorized slot, got %d", authorized)
	}
	if out, err := s.Release(ctx, ent, old); err != nil || out != OutcomeOK {
		t.Fatalf("release: %s %v", out, err)
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND status='AUTHORIZED'`, ent).Scan(&authorized); err != nil {
		t.Fatal(err)
	}
	if authorized != 0 {
		t.Fatalf("the slot was not freed: %d still authorized", authorized)
	}
	seedDevice(t, p, ent, "")
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND status='AUTHORIZED'`, ent).Scan(&authorized); err != nil {
		t.Fatal(err)
	}
	if authorized != 1 {
		t.Fatalf("a replacement could not take the freed slot: %d authorized", authorized)
	}
}

// Two concurrent releases of the same offline device: exactly one may free the slot. Two would mean one slot
// freed twice, which is how a device limit quietly stops being a limit.
func TestConcurrentReleasesFreeTheSlotOnce(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	ent := seedEntitlement(t, p)
	dev := seedDevice(t, p, ent, "")

	type res struct {
		out Outcome
		err error
	}
	ch := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			o, err := s.Release(ctx, ent, dev)
			ch <- res{o, err}
		}()
	}
	var oks int
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			t.Fatalf("concurrent release errored: %v", r.err)
		}
		if r.out == OutcomeOK {
			oks++
		}
	}
	if oks != 1 {
		t.Fatalf("expected exactly one release to succeed, got %d", oks)
	}
	var disconnected int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND device_id=$2 AND status='DISCONNECTED'`, ent, dev).Scan(&disconnected); err != nil {
		t.Fatal(err)
	}
	if disconnected != 1 {
		t.Fatalf("the binding is not in exactly one released state: %d", disconnected)
	}
}

// The throttle is counted from durable rows, so reconnecting or restarting does not reset it.
func TestThrottleIsDurable(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	ent := seedEntitlement(t, p)
	dev := seedDevice(t, p, ent, "")

	// Three attempts against a budget of three, then the fourth must be refused. Called directly so the
	// budget can be made small; the service uses the default.
	for i := 0; i < 3; i++ {
		var out string
		if err := p.QueryRow(ctx, `SELECT iam_v2.p6_guest_release_device($1,$2,3)`, ent, dev).Scan(&out); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var out string
	if err := p.QueryRow(ctx, `SELECT iam_v2.p6_guest_release_device($1,$2,3)`, ent, dev).Scan(&out); err != nil {
		t.Fatal(err)
	}
	if Outcome(out) != OutcomeRefusedThrottled {
		t.Fatalf("the throttle did not engage: %s", out)
	}
	// A fresh pool is a fresh process as far as any in-memory counter is concerned.
	p2, err := pgxpool.New(ctx, os.Getenv("PHASE3_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	if err := p2.QueryRow(ctx, `SELECT iam_v2.p6_guest_release_device($1,$2,3)`, ent, dev).Scan(&out); err != nil {
		t.Fatal(err)
	}
	if Outcome(out) != OutcomeRefusedThrottled {
		t.Fatalf("a new connection reset the throttle: %s", out)
	}
}

// ---------------------------------------------------------------------------------------------------------
// THE RACE, against the REAL admission path.
//
// The first version of this test created the competing session with a raw INSERT under
// session_replication_role=replica, which disables the very triggers the invariant depends on -- so it proved
// that a release and a fake admission do not collide, which is not the claim. Worse, it ACCEPTED the outcome
// "release OK, binding DISCONNECTED, live session arriving after", and that final state is exactly what the
// feature must never produce.
//
// admitDevice below goes through iam_v2.authorize_entitlement_device -- the primitive migration 0010 declares
// to be one of only two approved ways to open an authorization interval, the one the real grant path in
// staygrant.go calls, the one that takes the L3 entitlement lock in the global lock order and enforces the
// device limit under it -- and then inserts the session with every trigger armed.

// admitDevice performs a REAL device admission: authorize through the approved primitive, then open a
// session, in one transaction, with no trigger disabled.
func admitDevice(ctx context.Context, p *pgxpool.Pool, ent, dev string) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, dev); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.sessions
		(id, tenant_id, site_id, entitlement_id, device_id, state, started)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'PENDING_ENFORCEMENT', now())`,
		fixTenant, fixSite, ent, dev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// assertNoReleasedBindingIsOnline is THE assertion. It fails if the database anywhere contains a
// DISCONNECTED binding carrying an active or PENDING_ENFORCEMENT session.
func assertNoReleasedBindingIsOnline(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), `
		SELECT count(*) FROM iam_v2.entitlement_devices ed
		  JOIN iam_v2.sessions se ON se.entitlement_id = ed.entitlement_id AND se.device_id = ed.device_id
		 WHERE ed.status = 'DISCONNECTED' AND se.state IN ('active','PENDING_ENFORCEMENT')`).Scan(&n); err != nil {
		t.Fatalf("invariant query: %v", err)
	}
	if n != 0 {
		t.Fatalf("FORBIDDEN STATE: %d released binding(s) carry a live session", n)
	}
}

// The structural guard, stated on its own: a live session cannot be created on a released binding by ANY
// writer, however it orders its statements.
func TestLiveSessionCannotExistOnAReleasedBinding(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	ent := seedEntitlement(t, p)
	dev := seedDevice(t, p, ent, "")

	if out, err := s.Release(ctx, ent, dev); err != nil || out != OutcomeOK {
		t.Fatalf("release: %s %v", out, err)
	}
	// A raw admission attempt with every trigger armed must be refused outright.
	_, err := p.Exec(ctx, `INSERT INTO iam_v2.sessions
		(id, tenant_id, site_id, entitlement_id, device_id, state, started)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'active', now())`, fixTenant, fixSite, ent, dev)
	if err == nil {
		t.Fatal("a live session was created on a RELEASED binding")
	}
	assertNoReleasedBindingIsOnline(t, p)

	// ...and a legitimate reconnect works, but only through the normal authorization path, which opens a NEW
	// interval rather than reviving the closed one.
	if err := admitDevice(ctx, p, ent, dev); err != nil {
		t.Fatalf("legitimate re-admission was refused: %v", err)
	}
	var intervals, open int
	if err := p.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER (WHERE deauthorized_at IS NULL)
		FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND device_id=$2`,
		ent, dev).Scan(&intervals, &open); err != nil {
		t.Fatal(err)
	}
	if intervals != 2 {
		t.Fatalf("expected the closed interval PLUS a new one, got %d", intervals)
	}
	if open != 1 {
		t.Fatalf("expected exactly one open interval after re-admission, got %d", open)
	}
	var status string
	if err := p.QueryRow(ctx, `SELECT status FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND device_id=$2`, ent, dev).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "AUTHORIZED" {
		t.Fatalf("re-admission did not restore the binding: %s", status)
	}
}

// Release versus a REAL concurrent admission of the same device. Exactly one coherent outcome, and the
// forbidden state must never appear.
func TestReleaseRacesRealAdmission(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()

	var releaseWon, admissionWon int
	for round := 0; round < 12; round++ {
		ent := seedEntitlement(t, p)
		dev := seedDevice(t, p, ent, "")

		admitErr := make(chan error, 1)
		go func() {
			time.Sleep(time.Duration(round%3) * time.Millisecond)
			admitErr <- admitDevice(ctx, p, ent, dev)
		}()
		out, err := s.Release(ctx, ent, dev)
		aerr := <-admitErr
		if err != nil {
			t.Fatalf("round %d release: %v", round, err)
		}

		var status string
		var live int
		if err := p.QueryRow(ctx, `SELECT ed.status,
			(SELECT count(*) FROM iam_v2.sessions se WHERE se.entitlement_id=$1 AND se.device_id=$2
			   AND se.state IN ('active','PENDING_ENFORCEMENT'))
			FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id=$1 AND ed.device_id=$2`,
			ent, dev).Scan(&status, &live); err != nil {
			t.Fatalf("round %d observe: %v", round, err)
		}

		switch {
		case out == OutcomeOK && aerr != nil && status == "DISCONNECTED" && live == 0:
			// (A) release won: the binding is released and the admission was refused outright.
			releaseWon++
		case out == OutcomeOK && aerr == nil && status == "AUTHORIZED" && live > 0:
			// (A') release won, then the admission RE-AUTHORIZED through the approved primitive -- a new
			// interval, a fresh device-limit check. That is a legitimate reconnect, not a survival of the
			// released authorization, and the interval history proves which it was.
			releaseWon++
		case out == OutcomeRefusedOnline && aerr == nil && status == "AUTHORIZED" && live > 0:
			// (B) admission won: the binding is untouched and the release observed the live session.
			admissionWon++
		default:
			t.Fatalf("round %d incoherent: release=%s admitErr=%v status=%s live=%d",
				round, out, aerr, status, live)
		}
		assertNoReleasedBindingIsOnline(t, p)
	}
	if releaseWon+admissionWon != 12 {
		t.Fatalf("rounds did not classify: release=%d admission=%d", releaseWon, admissionWon)
	}
	t.Logf("release won %d, admission won %d, forbidden state never observed", releaseWon, admissionWon)
}

// BRANCH (B), DETERMINISTICALLY. The unbiased race above resolved to "release won" in every round, which is
// exactly the situation Phase 5 hit with F9-i: an outcome that never occurs is an outcome never tested, and
// tuning a sleep until it sometimes happens is not a proof. So the admission is committed FIRST, through the
// real primitive, and the release must then observe it and refuse.
func TestAdmissionWinsIsRefusedByRelease(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	ent := seedEntitlement(t, p)
	dev := seedDevice(t, p, ent, "")

	if err := admitDevice(ctx, p, ent, dev); err != nil {
		t.Fatalf("real admission failed: %v", err)
	}
	out, err := s.Release(ctx, ent, dev)
	if err != nil {
		t.Fatal(err)
	}
	if out != OutcomeRefusedOnline {
		t.Fatalf("branch B: release did not observe the admitted session: %s", out)
	}
	var status string
	if err := p.QueryRow(ctx, `SELECT status FROM iam_v2.entitlement_devices
		WHERE entitlement_id=$1 AND device_id=$2`, ent, dev).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "AUTHORIZED" {
		t.Fatalf("branch B: the binding was modified by a refused release: %s", status)
	}
	assertNoReleasedBindingIsOnline(t, p)
}

// Release versus a NEW device admission at the max-device boundary. The limit must never be exceeded, even
// transiently, and exactly one slot may be freed.
func TestReleaseRacesNewDeviceAtTheLimit(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()

	for round := 0; round < 8; round++ {
		ent := seedEntitlementWithLimit(t, p, 1)
		held := seedDevice(t, p, ent, "")
		newcomer := seedBareDevice(t, p)

		admitErr := make(chan error, 1)
		go func() { admitErr <- admitDevice(ctx, p, ent, newcomer) }()
		out, err := s.Release(ctx, ent, held)
		aerr := <-admitErr
		if err != nil {
			t.Fatalf("round %d release: %v", round, err)
		}

		var openIntervals int
		if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
			WHERE entitlement_id=$1 AND deauthorized_at IS NULL`, ent).Scan(&openIntervals); err != nil {
			t.Fatal(err)
		}
		if openIntervals > 1 {
			t.Fatalf("round %d: %d devices authorized against a limit of 1 (release=%s admit=%v)",
				round, openIntervals, out, aerr)
		}
		assertNoReleasedBindingIsOnline(t, p)

		// Exactly one slot freed: the released binding is DISCONNECTED exactly once, never twice.
		var released int
		if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_device_actions
			WHERE entitlement_id=$1 AND device_id=$2 AND outcome='OK'`, ent, held).Scan(&released); err != nil {
			t.Fatal(err)
		}
		if released > 1 {
			t.Fatalf("round %d: the slot was freed %d times", round, released)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// THE FIRST SETTING WRITE, CONCURRENTLY.
//
// p6_set_guest_device_self_service used to take the settings row FOR UPDATE, which orders nothing on an
// appliance that has never been configured: there is no row to lock. Two concurrent first writes would each
// read old_value as NULL and each record a transition from "unset", so the setting converged (the upsert is
// idempotent) while the HISTORY claimed two independent first changes -- and the history is the only reason
// the audit exists.
//
// The fix anchors the lock on the APPLIANCE identity, which always exists. This proves the audit's old/new
// chain reflects the actual committed sequence.
func TestConcurrentFirstSettingWriteProducesACoherentAuditChain(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	t.Cleanup(func() { clearSetting(ctx, p) })
	clearSetting(ctx, p)

	// TWO GOROUTINES WERE NOT ENOUGH. Fired together they passed with and without the fix, because the
	// window between "read the absent row" and "insert it" is a few microseconds and the Go scheduler plus
	// connection acquisition almost always serialized them. A race that only sometimes overlaps is a test
	// that only sometimes tests anything -- the Phase-5 F9-i lesson again.
	//
	// So the overlap is FORCED. Writer A opens a transaction, calls the function and does NOT commit. Writer
	// B then calls the function in its own transaction. Without the appliance anchor, B reads the absent row
	// (READ COMMITTED sees nothing of A's uncommitted insert), records old_value = NULL, and only then blocks
	// on the row -- so both rows claim to be the first change. With the anchor, B blocks BEFORE it reads, and
	// therefore sees what A committed.
	txA, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if _, err := txA.Exec(ctx, `SELECT iam_v2.p6_set_guest_device_self_service($1,$2,$3,$4,$5,$6,$7)`,
		fixTenant, fixSite, fixAppliance, true, fixOperator, "writer-A", "first"); err != nil {
		t.Fatalf("writer A: %v", err)
	}

	bDone := make(chan error, 1)
	go func() {
		txB, err := p.Begin(ctx)
		if err != nil {
			bDone <- err
			return
		}
		defer func() { _ = txB.Rollback(ctx) }()
		if _, err := txB.Exec(ctx, `SELECT iam_v2.p6_set_guest_device_self_service($1,$2,$3,$4,$5,$6,$7)`,
			fixTenant, fixSite, fixAppliance, false, fixOperator, "writer-B", "second"); err != nil {
			bDone <- err
			return
		}
		bDone <- txB.Commit(ctx)
	}()

	// Give B time to reach the function and block (or, without the fix, to read past it).
	time.Sleep(150 * time.Millisecond)
	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("writer A commit: %v", err)
	}
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("writer B: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer B never completed")
	}

	rows, err := p.Query(ctx, `SELECT old_value, new_value, changed_by
		FROM iam_v2.appliance_product_setting_changes WHERE appliance_id=$1 ORDER BY changed_at, id`, fixAppliance)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		old *bool
		new bool
		by  string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.old, &r.new, &r.by); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("expected exactly two audit rows, got %d", len(got))
	}

	firsts := 0
	for _, r := range got {
		if r.old == nil {
			firsts++
		}
	}
	if firsts != 1 {
		t.Fatalf("%d of 2 audit rows claim to be the FIRST change: the writers were not ordered", firsts)
	}

	// The chain must be continuous AND in the committed order: A committed first, so A is the first row and
	// B must have observed A's value.
	if got[0].by != "writer-A" {
		t.Fatalf("the audit order does not reflect the committed order: first row is %q", got[0].by)
	}
	if got[1].old == nil || *got[1].old != got[0].new {
		t.Fatalf("broken chain: A wrote %v, B recorded finding %v", got[0].new, got[1].old)
	}

	var on bool
	if err := p.QueryRow(ctx, `SELECT guest_device_self_service FROM iam_v2.appliance_product_settings
		WHERE appliance_id=$1`, fixAppliance).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != got[1].new {
		t.Fatalf("the setting (%v) disagrees with the last audited change (%v)", on, got[1].new)
	}
}

// clearSetting removes the fixture appliance's setting and audit so a first-write test really is first.
func clearSetting(ctx context.Context, p *pgxpool.Pool) {
	tx, err := p.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `ALTER TABLE iam_v2.appliance_product_setting_changes DISABLE TRIGGER p6_setting_changes_append_only`)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.appliance_product_setting_changes WHERE appliance_id=$1`, fixAppliance)
	_, _ = tx.Exec(ctx, `ALTER TABLE iam_v2.appliance_product_setting_changes ENABLE TRIGGER p6_setting_changes_append_only`)
	_, _ = tx.Exec(ctx, `DELETE FROM iam_v2.appliance_product_settings WHERE appliance_id=$1`, fixAppliance)
	_ = tx.Commit(ctx)
}

// The setting write must refuse an operator label the server did not resolve. "Somebody changed it" is not an
// audit record, and an empty label is how that happens in practice.
func TestSettingRefusesAnEmptyOperatorLabel(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()
	t.Cleanup(func() { clearSetting(ctx, p) })
	if err := s.SetForAppliance(ctx, fixTenant, fixSite, fixAppliance, true,
		fixOperator, "   ", "no label"); err == nil {
		t.Fatal("a setting change was accepted with a blank operator label")
	}
}
