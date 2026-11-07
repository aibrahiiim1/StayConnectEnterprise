//go:build integration

package stayengine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onegate/onegate/data-plane/internal/checkout"
)

// Protel announces a room move as a check-out from the old room and a check-in to the new one, same reservation (FIAS 2.20 p. 46,
// either order). Contract F1: the move keeps the package, its devices and its data; a same-room pair stays a reinstatement.

func moveFixture(t *testing.T) (*pgxpool.Pool, scope, *Processor, string) {
	t.Helper()
	p := pool(t)
	t.Cleanup(p.Close)
	s := seed(t, p)
	return p, s, NewProcessorWithCheckout(p, checkout.NewConverter(p)), commerce(t, p, s, true)
}

// pmsdPool connects as the stay engine's production role, so the move is applied with exactly its privileges.
func pmsdPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("PHASE3_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE svc_pmsd`)
		return err
	}
	rp, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.Close)
	return rp
}

// authorizeDevice gives the entitlement one device, authorized from `from`.
func authorizeDevice(t *testing.T, p *pgxpool.Pool, s scope, ent string, from time.Time) string {
	t.Helper()
	ctx := context.Background()
	var dev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),('02:00:00:'||substr(md5(random()::text),1,2)||':'||substr(md5(random()::text),1,2)||':'||substr(md5(random()::text),1,2))::macaddr)
		RETURNING id::text`, s.tenant, s.site).Scan(&dev); err != nil {
		t.Fatalf("device: %v", err)
	}
	if err := guardedExec(p, []string{"device_auth"}, `WITH d AS (INSERT INTO iam_v2.entitlement_devices
		(tenant_id, site_id, entitlement_id, device_id, status, grandfathered, first_authorized, last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED',false,$5,$5) RETURNING 1)
		INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
		SELECT $1,$2,$3,$4,1,$5 FROM d`, s.tenant, s.site, ent, dev, from); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	return dev
}

func stayOf(t *testing.T, p *pgxpool.Pool, s scope, res string) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`,
		s.iface, res).Scan(&id); err != nil {
		t.Fatalf("stay %s: %v", res, err)
	}
	return id
}

func TestIntegration_RoomMoveCheckoutFirstContinuesThePackage(t *testing.T) {
	p, s, _, pkgRev := moveFixture(t)
	rp := pmsdPool(t)
	pr := NewProcessorWithCheckout(rp, checkout.NewConverter(rp))
	ctx := context.Background()
	insertLive(t, p, s, "M-GI", "GI", pay("RM1", "900", "Mover", "Ana", "F1", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM1")
	boundary := time.Now().Add(-30 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-2*time.Hour))
	dev := authorizeDevice(t, p, s, ent, boundary.Add(-time.Hour))
	var window time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&window); err != nil {
		t.Fatal(err)
	}

	insertLiveAt(t, p, s, "M-GO", "GO", pay("RM1", "900", "Mover", "Ana", "F1", "260101", "260110"), boundary)
	process(t, pr, s)
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND terminal_reason='CONVERTED'`, ent); n != 1 {
		t.Fatal("the check-out half of the move did not convert as a checkout does")
	}

	insertLive(t, p, s, "M-GI2", "GI", pay("RM1", "901", "Mover", "Ana", "F1", "260101", "260110"))
	process(t, pr, s)

	status, lv, room := stayState(t, p, s, "RM1")
	if status != "IN_HOUSE" || lv != 2 || room != "901" {
		t.Fatalf("stay %s/%d/%s, want IN_HOUSE/2/901", status, lv, room)
	}
	var cont, contPkg, purchaseTrigger string
	var contWindow time.Time
	var quota *int64
	var amount int64
	if err := p.QueryRow(ctx, `SELECT e.id::text, e.package_revision_id::text, e.window_ends_at, e.data_quota_bytes, pu.trigger, pu.amount_minor
		FROM iam_v2.entitlements e JOIN iam_v2.purchases pu ON pu.id = e.purchase_id
		WHERE e.stay_id=$1 AND e.status='ACTIVE' AND pu.trigger='ROOM_MOVE_RESTORE'`, stayID).Scan(&cont, &contPkg, &contWindow, &quota, &purchaseTrigger, &amount); err != nil {
		t.Fatalf("no live package after the move: %v", err)
	}
	if contPkg != pkgRev || !contWindow.Equal(window) {
		t.Fatalf("continuation package %s window %v, want %s and the original window %v (never extended)", contPkg, contWindow, pkgRev, window)
	}
	if quota == nil || *quota != 524288000 {
		t.Fatalf("continuation allowance %v, want the 500 MB the original had left", quota)
	}
	if purchaseTrigger != "ROOM_MOVE_RESTORE" || amount != 0 {
		t.Fatalf("continuation purchase %s/%d, want a zero-price ROOM_MOVE_RESTORE", purchaseTrigger, amount)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND policy_snapshot->>'restores'=$2`, cont, ent); n != 1 {
		t.Fatal("the continuation does not point at the package it continues")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'
		AND status='TERMINATED' AND terminal_reason='SUPERSEDED'`, stayID); n != 1 {
		t.Fatal("the After Check-out Access of the move was not superseded")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND device_id::text=$2 AND status='AUTHORIZED'`, cont, dev); n != 1 {
		t.Fatal("the device the guest had before the move lost its place")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.pms_postings WHERE stay_id=$1`, stayID); n != 0 {
		t.Fatal("a room move produced a room charge")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.room_move_restorations WHERE stay_id=$1 AND lifecycle_version=1 AND outcome='RESTORED' AND source='ROOM_MOVE'`, stayID); n != 1 {
		t.Fatal("the restoration was not recorded")
	}

	// A replayed check-in, or a second run for the episode, changes nothing.
	if !redeliver(t, p, s, "M-GI2", "GI", pay("RM1", "901", "Mover", "Ana", "F1", "260101", "260110")) {
		t.Fatal("the same check-in identity was accepted twice")
	}
	var again string
	if err := p.QueryRow(ctx, `SELECT outcome FROM iam_v2.restore_room_move_access($1,$2,$3::uuid,1,
		(SELECT id FROM iam_v2.stay_events WHERE pms_interface_id=$4 AND external_event_identity='M-GI2'),'ROOM_MOVE',false)`,
		s.tenant, s.site, stayID, s.iface).Scan(&again); err != nil || again != "RESTORED" {
		t.Fatalf("a repeated restoration answered %q, %v", again, err)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, stayID); n != 1 {
		t.Fatalf("%d live packages after a repeat, want 1", n)
	}
}

// Two moves in a row, the second check-in arriving only with the next roster resync (seen on the test appliance, 2026-11-06): the
// package survives both, with its original end time.
func TestIntegration_TwoRoomMovesInARowKeepThePackage(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	ctx := context.Background()
	insertLive(t, p, s, "T-GI", "GI", pay("RM5", "500", "Twice", "Ed", "F5", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM5")
	ent := grantEntitlement(t, p, s, stayID, pkgRev, time.Now().Add(-3*time.Hour))
	var window time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&window); err != nil {
		t.Fatal(err)
	}
	insertLiveAt(t, p, s, "T-GO1", "GO", pay("RM5", "500", "Twice", "Ed", "F5", "260101", "260110"), time.Now().Add(-2*time.Hour))
	process(t, pr, s)
	insertLive(t, p, s, "T-GI1", "GI", pay("RM5", "501", "Twice", "Ed", "F5", "260101", "260110"))
	process(t, pr, s)
	insertLiveAt(t, p, s, "T-GO2", "GO", pay("RM5", "501", "Twice", "Ed", "F5", "260101", "260110"), time.Now())
	process(t, pr, s)
	insertLive(t, p, s, "T-GI2", "GI", pay("RM5", "500", "Twice", "Ed", "F5", "260101", "260110"))
	process(t, pr, s)

	if status, lv, room := stayState(t, p, s, "RM5"); status != "IN_HOUSE" || lv != 3 || room != "500" {
		t.Fatalf("stay %s/%d/%s, want IN_HOUSE/3/500", status, lv, room)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.room_move_restorations WHERE stay_id=$1 AND outcome='RESTORED'`, stayID); n != 2 {
		t.Fatalf("%d restorations, want one per move", n)
	}
	var got time.Time
	if err := p.QueryRow(ctx, `SELECT e.window_ends_at FROM iam_v2.entitlements e JOIN iam_v2.purchases pu ON pu.id=e.purchase_id
		WHERE e.stay_id=$1 AND e.status='ACTIVE' AND pu.trigger='ROOM_MOVE_RESTORE'`, stayID).Scan(&got); err != nil {
		t.Fatalf("no live continuation after the second move: %v", err)
	}
	if !got.Equal(window) {
		t.Fatalf("after two moves the package ends %v, want the original %v", got, window)
	}
}

// With After Check-out Access off there is no grace to carry the devices; the continuation still gets the ones the checkout closed.
func TestIntegration_RoomMoveWithoutAfterCheckoutAccessKeepsTheDevices(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	ctx := context.Background()
	var op string
	if err := p.QueryRow(ctx, `INSERT INTO public.operators(tenant_id,email,display_name,password_hash,status)
		VALUES ($1,$2,'Move Test','$argon2id$fixture','active') RETURNING id::text`, s.tenant, "mv-"+s.tenant[:8]+"@example.invalid").Scan(&op); err != nil {
		t.Fatalf("operator: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.set_after_checkout_enabled($1,$2,false,
		(SELECT config_version FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2)::int,$3::uuid,'TEST_REASON')`,
		s.tenant, s.site, op); err != nil {
		t.Fatalf("switch After Check-out Access off: %v", err)
	}
	insertLive(t, p, s, "D-GI", "GI", pay("RM6", "400", "Nograce", "Fe", "F6", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM6")
	boundary := time.Now().Add(-10 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-time.Hour))
	dev := authorizeDevice(t, p, s, ent, boundary.Add(-30*time.Minute))
	insertLiveAt(t, p, s, "D-GO", "GO", pay("RM6", "400", "Nograce", "Fe", "F6", "260101", "260110"), boundary)
	process(t, pr, s)
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID); n != 0 {
		t.Fatalf("After Check-out Access is off, yet %d grace packages exist", n)
	}
	insertLive(t, p, s, "D-GI2", "GI", pay("RM6", "401", "Nograce", "Fe", "F6", "260101", "260110"))
	process(t, pr, s)
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices ed JOIN iam_v2.entitlements e ON e.id=ed.entitlement_id
		JOIN iam_v2.purchases pu ON pu.id=e.purchase_id
		WHERE e.stay_id=$1 AND e.status='ACTIVE' AND pu.trigger='ROOM_MOVE_RESTORE' AND ed.device_id::text=$2 AND ed.status='AUTHORIZED'`, stayID, dev); n != 1 {
		t.Fatal("the device the checkout disconnected did not get its place back on the continued package")
	}
}

// A live session moves to the continuation just after its last recorded sample, so a sample still in flight at the move is
// counted against the continuation, not against the allowance already frozen into it.
func TestIntegration_RoomMoveSwitchesALiveSessionAfterItsLastSample(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	ctx := context.Background()
	insertLive(t, p, s, "S-GI", "GI", pay("RM8", "500", "Online", "Jo", "F8", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM8")
	boundary := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-time.Hour))
	dev := authorizeDevice(t, p, s, ent, boundary.Add(-30*time.Minute))
	insertLiveAt(t, p, s, "S-GO", "GO", pay("RM8", "500", "Online", "Jo", "F8", "260101", "260110"), boundary)
	process(t, pr, s)

	var grace, sess string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT' AND status='ACTIVE'`,
		stayID).Scan(&grace); err != nil {
		t.Fatalf("After Check-out Access: %v", err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions (tenant_id, site_id, entitlement_id, device_id, ip, mac, state, credential_method,
		started, ingress_interface)
		SELECT $1,$2,$3::uuid,dv.id,'10.77.0.8'::inet,dv.mac,'active','room',$5,'br-lan' FROM iam_v2.devices dv WHERE dv.id=$4::uuid RETURNING id::text`,
		s.tenant, s.site, grace, dev, boundary.Add(time.Minute)).Scan(&sess); err != nil {
		t.Fatalf("session: %v", err)
	}
	last := time.Now().Add(-2 * time.Minute).Truncate(time.Microsecond)
	for i, at := range []time.Time{last.Add(-time.Minute), last} {
		if _, err := p.Exec(ctx, `SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,'br-lan',iam_v2.p3_expected_class_minor('10.77.0.8'::inet),
			1,$5,$5,$6)`, s.tenant, s.site, sess, dev, int64(i)*1000, at); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}

	insertLive(t, p, s, "S-GI2", "GI", pay("RM8", "501", "Online", "Jo", "F8", "260101", "260110"))
	process(t, pr, s)
	var cont, before, after string
	if err := p.QueryRow(ctx, `SELECT e.id::text, iam_v2.p3_entitlement_at($2::uuid, $3)::text, iam_v2.p3_entitlement_at($2::uuid, $3 + interval '1 second')::text
		FROM iam_v2.entitlements e JOIN iam_v2.purchases pu ON pu.id=e.purchase_id
		WHERE e.stay_id=$1 AND e.status='ACTIVE' AND pu.trigger='ROOM_MOVE_RESTORE'`, stayID, sess, last).Scan(&cont, &before, &after); err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if before != grace || after != cont {
		t.Fatalf("the sample at the last reading belongs to %s and one a second later to %s; want the After Check-out Access, then the continuation", before, after)
	}
}

// A checkout closed by a same-room reinstatement is not a move, even if the stay moves rooms later.
func TestIntegration_RecoveryRefusesACheckoutClosedByAReinstatement(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	ctx := context.Background()
	insertLive(t, p, s, "R-GI", "GI", pay("RM7", "300", "Back", "Gu", "F7", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM7")
	boundary := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-time.Hour))
	insertLiveAt(t, p, s, "R-GO", "GO", pay("RM7", "300", "Back", "Gu", "F7", "260101", "260110"), boundary)
	process(t, pr, s)
	insertLive(t, p, s, "R-GI2", "GI", pay("RM7", "300", "Back", "Gu", "F7", "260101", "260110"))
	process(t, pr, s)
	insertLive(t, p, s, "R-GI3", "GI", pay("RM7", "301", "Back", "Gu", "F7", "260101", "260110"))
	process(t, pr, s)
	if _, err := p.Exec(ctx, `SELECT * FROM iam_v2.restore_room_move_access($1,$2,$3::uuid,1,NULL,'RECOVERY',false)`, s.tenant, s.site, stayID); err == nil {
		t.Fatal("a checkout closed by a same-room reinstatement was restored because the stay moved later")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.room_move_restorations WHERE stay_id=$1`, stayID); n != 0 {
		t.Fatal("a refused recovery left a record")
	}
}

func TestIntegration_RoomMoveCheckinFirstNeverChecksOut(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	insertLive(t, p, s, "N-GI", "GI", pay("RM2", "700", "Early", "Bo", "F2", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM2")
	ent := grantEntitlement(t, p, s, stayID, pkgRev, time.Now().Add(-time.Hour))

	insertLive(t, p, s, "N-GI2", "GI", pay("RM2", "701", "Early", "Bo", "F2", "260101", "260110"))
	process(t, pr, s)
	insertLiveAt(t, p, s, "N-GO", "GO", pay("RM2", "700", "Early", "Bo", "F2", "260101", "260110"), time.Now().Add(-time.Minute))
	process(t, pr, s)

	status, lv, room := stayState(t, p, s, "RM2")
	if status != "IN_HOUSE" || lv != 1 || room != "701" {
		t.Fatalf("stay %s/%d/%s, want IN_HOUSE/1/701: the old room's check-out must end nothing", status, lv, room)
	}
	if st, _ := eventOutcome(t, p, s, "N-GO"); st != "APPLIED" {
		t.Fatalf("the old room's check-out is %s, want APPLIED", st)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='ACTIVE'`, ent); n != 1 {
		t.Fatal("the package ended on the old room's check-out")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, stayID); n != 0 {
		t.Fatal("a checkout was recorded for a room the guest had already left")
	}
	// The real departure, from the room the guest is in, still checks out.
	insertLiveAt(t, p, s, "N-GO2", "GO", pay("RM2", "701", "Early", "Bo", "F2", "260101", "260110"), time.Now())
	process(t, pr, s)
	if status, _, _ := stayState(t, p, s, "RM2"); status != "CHECKED_OUT" {
		t.Fatalf("the departure from the guest's own room left the stay %s", status)
	}
}

func TestIntegration_SameRoomCheckoutCheckinStaysAReinstatement(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	insertLive(t, p, s, "S-GI", "GI", pay("RM3", "800", "Same", "Cy", "F3", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM3")
	boundary := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-time.Hour))
	insertLiveAt(t, p, s, "S-GO", "GO", pay("RM3", "800", "Same", "Cy", "F3", "260101", "260110"), boundary)
	process(t, pr, s)
	insertLive(t, p, s, "S-GI2", "GI", pay("RM3", "800", "Same", "Cy", "F3", "260101", "260110"))
	process(t, pr, s)

	if status, lv, _ := stayState(t, p, s, "RM3"); status != "IN_HOUSE" || lv != 2 {
		t.Fatalf("stay %s/%d, want a reinstated IN_HOUSE/2", status, lv)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.room_move_restorations WHERE stay_id=$1`, stayID); n != 0 {
		t.Fatal("a same-room check-out/check-in was treated as a room move")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND terminal_reason='CONVERTED'`, ent); n != 1 {
		t.Fatal("the reinstatement changed the converted package")
	}
}

func TestIntegration_RoomMoveNeverExtendsAnEndedWindow(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	ctx := context.Background()
	insertLive(t, p, s, "X-GI", "GI", pay("RM4", "600", "Late", "Di", "F4", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM4")
	// Granted 80 h ago: the 72 h window ended 8 h ago, after the checkout 10 h ago.
	boundary := time.Now().Add(-10 * time.Hour).Truncate(time.Microsecond)
	grantEntitlement(t, p, s, stayID, pkgRev, time.Now().Add(-80*time.Hour))
	insertLiveAt(t, p, s, "X-GO", "GO", pay("RM4", "600", "Late", "Di", "F4", "260101", "260110"), boundary)
	process(t, pr, s)
	insertLive(t, p, s, "X-GI2", "GI", pay("RM4", "601", "Late", "Di", "F4", "260101", "260110"))
	process(t, pr, s)

	var outcome string
	if err := p.QueryRow(ctx, `SELECT outcome FROM iam_v2.room_move_restorations WHERE stay_id=$1`, stayID).Scan(&outcome); err != nil || outcome != "EXPIRED" {
		t.Fatalf("restoration outcome %q (%v), want EXPIRED", outcome, err)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE' AND end_mode<>'GRACE_AFTER_CHECKOUT'`, stayID); n != 0 {
		t.Fatal("a package whose window had ended was restored")
	}
}

// After a check-out-first move, the old room's check-out re-sent late changes nothing, and the guest's real departure from the new room
// checks out the continued package exactly as any checkout does.
func TestIntegration_ALateRepeatOfTheMovesCheckoutChangesNothingAndTheRealDepartureStillChecksOut(t *testing.T) {
	p, s, pr, pkgRev := moveFixture(t)
	insertLive(t, p, s, "L-GI", "GI", pay("RM9", "200", "Late", "Al", "F9", "260101", "260110"))
	process(t, pr, s)
	stayID := stayOf(t, p, s, "RM9")
	boundary := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-time.Hour))
	authorizeDevice(t, p, s, ent, boundary.Add(-30*time.Minute))
	insertLiveAt(t, p, s, "L-GO", "GO", pay("RM9", "200", "Late", "Al", "F9", "260101", "260110"), boundary)
	process(t, pr, s)
	insertLive(t, p, s, "L-GI2", "GI", pay("RM9", "201", "Late", "Al", "F9", "260101", "260110"))
	process(t, pr, s)
	var cont string
	if err := p.QueryRow(context.Background(), `SELECT e.id::text FROM iam_v2.entitlements e JOIN iam_v2.purchases pu ON pu.id=e.purchase_id
		WHERE e.stay_id=$1 AND e.status='ACTIVE' AND pu.trigger='ROOM_MOVE_RESTORE'`, stayID).Scan(&cont); err != nil {
		t.Fatalf("no continuation after the move: %v", err)
	}

	insertLive(t, p, s, "L-GO-again", "GO", pay("RM9", "200", "Late", "Al", "F9", "260101", "260110"))
	process(t, pr, s)
	if status, lv, room := stayState(t, p, s, "RM9"); status != "IN_HOUSE" || lv != 2 || room != "201" {
		t.Fatalf("a late repeat of the old room's check-out left the stay %s/%d/%s, want IN_HOUSE/2/201", status, lv, room)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='ACTIVE'`, cont); n != 1 {
		t.Fatal("a late repeat of the old room's check-out ended the continued package")
	}

	insertLive(t, p, s, "L-GO2", "GO", pay("RM9", "201", "Late", "Al", "F9", "260101", "260110"))
	process(t, pr, s)
	if status, _, _ := stayState(t, p, s, "RM9"); status != "CHECKED_OUT" {
		t.Fatalf("the departure from the new room left the stay %s", status)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='TERMINATED' AND terminal_reason='CONVERTED'`, cont); n != 1 {
		t.Fatal("the real departure did not check out the continued package")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.room_move_restorations WHERE stay_id=$1`, stayID); n != 1 {
		t.Fatalf("restorations %d after a real departure, want only the move's 1", n)
	}
}