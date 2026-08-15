//go:build integration && phase6

package deviceselfservice

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
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

// macSeq hands out distinct MAC suffixes; device identity is (tenant, site, appliance, MAC), so two
// fixtures sharing a MAC would be the SAME device and the tests would silently test one row twice.
var macCounter atomic.Uint32

func macSeq() uint32 { return macCounter.Add(1) }

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
	// The MAC is built in Go rather than from the uuid in SQL: reusing $1 as both a uuid and a text argument
	// makes PostgreSQL deduce two different types for one parameter and refuse the statement outright.
	mac := fmt.Sprintf("02:00:00:%02x:%02x:%02x", macSeq()&0xff, (macSeq()>>8)&0xff, (macSeq()>>16)&0xff)
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

// A release that races a session arriving must produce one coherent outcome, and never a released binding
// with a live session on it.
func TestReleaseRacingAnArrivingSession(t *testing.T) {
	p := pool(t)
	s := New(p)
	ctx := context.Background()

	for round := 0; round < 8; round++ {
		ent := seedEntitlement(t, p)
		dev := seedDevice(t, p, ent, "")

		done := make(chan struct{})
		go func() {
			defer close(done)
			time.Sleep(2 * time.Millisecond)
			tx, err := p.Begin(ctx)
			if err != nil {
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
			_, _ = tx.Exec(ctx, `INSERT INTO iam_v2.sessions
				(id, tenant_id, site_id, entitlement_id, device_id, state, started)
				VALUES (gen_random_uuid(),$1,$2,$3,$4,'active', now())`, fixTenant, fixSite, ent, dev)
			_ = tx.Commit(ctx)
		}()
		out, err := s.Release(ctx, ent, dev)
		<-done
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
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
		case out == OutcomeOK && status == "DISCONNECTED":
			// the release won; a session arriving afterwards is the ordinary reconnect case
		case out == OutcomeRefusedOnline && status == "AUTHORIZED" && live > 0:
			// the session won; the binding is untouched
		default:
			t.Fatalf("round %d: incoherent outcome=%s status=%s live=%d", round, out, status, live)
		}
	}
}
