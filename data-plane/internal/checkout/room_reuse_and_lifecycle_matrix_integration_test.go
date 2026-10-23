//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A room is not a guest: while the departing guest is in grace the arriving guest checks in, and state keyed on
// the room instead of the person would let one reach into the other's entitlement (cut-short grace, inherited
// allowance, reset grace timer) without any error. The conversion path never reads a room; these tests keep
// it that way.

// secondStayInSameRoom creates an independent Stay (own id and reservation identity) in the same room.
func secondStayInSameRoom(t *testing.T, p *pgxpool.Pool, f fixture, room, reservation string) string {
	t.Helper()
	var stay string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
		INSERT INTO iam_v2.stays
		  (id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		   status,lifecycle_version,last_applied_event_version,normalized_room_number)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$4,'IN_HOUSE',1,5,$5)
		RETURNING id::text`, f.tenant, f.site, f.iface, reservation, room).Scan(&stay)
	}); err != nil {
		t.Fatalf("seed the arriving guest's stay: %v", err)
	}
	return stay
}

// seedEventTyped seeds an event of any type; seedEvent hardcodes 'GO'.
func seedEventTyped(t *testing.T, p *pgxpool.Pool, f fixture, eventType string) string {
	t.Helper()
	ts := f.boundary
	var eid string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,
		 pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,
		 processing_status,admission_kind,resync_generation)
		VALUES (gen_random_uuid(),$1,$2,$3,NULL,$4,$5,'x',$6,'UTC',7,1,false,'{}','PENDING','LIVE',0)
		RETURNING id`,
			f.tenant, f.site, f.iface, "EV-TYPED-"+eventType, eventType, ts).Scan(&eid)
	}); err != nil {
		t.Fatalf("seed a %s event: %v", eventType, err)
	}
	applyEvent(t, p, eid, f.stay)
	return eid
}

// checkoutEventFor builds an APPLIED GO for a specific stay; checkoutEvent's fixed sequence collides on the
// live-identity uniqueness constraint when called twice in one test.
func checkoutEventFor(t *testing.T, p *pgxpool.Pool, f fixture, stay string, seq int) BoundarySource {
	t.Helper()
	eid := seedEvent(t, p, f, &f.boundary, false, seq, "LIVE", 0, stay)
	applyEvent(t, p, eid, stay)
	return BoundarySource{StayEventID: eid}
}

func entForStay(t *testing.T, p *pgxpool.Pool, f fixture, stay string) string {
	t.Helper()
	g := f
	g.stay = stay
	return activeEnt(t, p, g)
}

// Guest A is in grace while guest B checks into the same room; the two must be completely independent.
func TestIntegration_RoomReuse_ArrivingGuestDoesNotDisturbTheDepartingGuestsGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})

	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET normalized_room_number='412' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("guest A checkout: %+v err=%v", res, err)
	}
	graceID := res.NewEntitlementID
	var graceEndsAt time.Time
	// Nullable: a grace with no data cap stores NULL, not 0.
	var graceQuota *int64
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at, data_quota_bytes FROM iam_v2.entitlements WHERE id=$1`, graceID).
		Scan(&graceEndsAt, &graceQuota); err != nil {
		t.Fatal(err)
	}

	arrivingStay := secondStayInSameRoom(t, p, f, "412", "RES-B")
	arrivingEnt := entForStay(t, p, f, arrivingStay)

	var afterEnds time.Time
	var afterQuota *int64
	var afterStatus, afterStay string
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at, data_quota_bytes, status, stay_id::text FROM iam_v2.entitlements WHERE id=$1`, graceID).
		Scan(&afterEnds, &afterQuota, &afterStatus, &afterStay); err != nil {
		t.Fatal(err)
	}
	if !afterEnds.Equal(graceEndsAt) {
		t.Fatalf("an arriving guest moved the departing guest's grace deadline: %s -> %s", graceEndsAt, afterEnds)
	}
	if (afterQuota == nil) != (graceQuota == nil) ||
		(afterQuota != nil && graceQuota != nil && *afterQuota != *graceQuota) {
		t.Fatalf("an arriving guest changed the departing guest's allowance: %v -> %v", graceQuota, afterQuota)
	}
	if afterStatus != "ACTIVE" {
		t.Fatalf("the departing guest's grace is %s -- an arrival must not end it", afterStatus)
	}
	if afterStay != f.stay {
		t.Fatalf("the grace changed owner: %s -> %s", f.stay, afterStay)
	}

	if arrivingEnt == graceID {
		t.Fatal("the arriving guest was handed the departing guest's entitlement")
	}
	var bStay string
	var bSupersedes *string
	var bIsGrace bool
	if err := p.QueryRow(ctx,
		`SELECT stay_id::text, supersedes_entitlement_id::text, is_emergency_grace
		   FROM iam_v2.entitlements WHERE id=$1`, arrivingEnt).Scan(&bStay, &bSupersedes, &bIsGrace); err != nil {
		t.Fatal(err)
	}
	if bStay != arrivingStay {
		t.Fatalf("the arriving guest's entitlement belongs to stay %s, want %s", bStay, arrivingStay)
	}
	if bSupersedes != nil {
		t.Fatalf("a fresh arrival must not supersede anything; it superseded %s", *bSupersedes)
	}
	if bIsGrace {
		t.Fatal("a fresh arrival must start a normal package, not inherit a grace entitlement")
	}

	// Both are live at once: one live entitlement per subject, two people. The count is scoped to this site
	// because the integration database persists across runs.
	if n := count(t, p, `
		SELECT count(*) FROM iam_v2.entitlements e
		  JOIN iam_v2.stays s ON s.id = e.stay_id
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.normalized_room_number='412' AND e.status='ACTIVE'`,
		f.tenant, f.site); n != 2 {
		t.Fatalf("room 412 at this site carries %d live entitlements, want 2 (one per guest)", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("the departing guest has %d live entitlements, want exactly 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, arrivingStay); n != 1 {
		t.Fatalf("the arriving guest has %d live entitlements, want exactly 1", n)
	}
}

// B's checkout must act only on B's entitlement and leave A's running grace alone.
func TestIntegration_RoomReuse_SecondGuestsCheckoutDoesNotTouchTheFirstsGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET normalized_room_number='412' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	activeEnt(t, p, f)

	resA, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 5))
	if err != nil || !resA.GraceCreated {
		t.Fatalf("guest A checkout: %+v err=%v", resA, err)
	}
	var aEnds time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, resA.NewEntitlementID).Scan(&aEnds); err != nil {
		t.Fatal(err)
	}

	bStay := secondStayInSameRoom(t, p, f, "412", "RES-B")
	entForStay(t, p, f, bStay)
	resB, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, bStay, checkoutEventFor(t, p, f, bStay, 6))
	if err != nil || !resB.GraceCreated {
		t.Fatalf("guest B checkout: %+v err=%v", resB, err)
	}
	if resB.NewEntitlementID == resA.NewEntitlementID {
		t.Fatal("guest B's checkout reused guest A's grace entitlement")
	}

	var aEndsAfter time.Time
	var aStatus string
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at, status FROM iam_v2.entitlements WHERE id=$1`, resA.NewEntitlementID).
		Scan(&aEndsAfter, &aStatus); err != nil {
		t.Fatal(err)
	}
	if !aEndsAfter.Equal(aEnds) {
		t.Fatalf("guest B's checkout moved guest A's grace deadline: %s -> %s", aEnds, aEndsAfter)
	}
	if aStatus != "ACTIVE" {
		t.Fatalf("guest B's checkout ended guest A's grace (%s)", aStatus)
	}

	for _, s := range []string{f.stay, bStay} {
		if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, s); n != 1 {
			t.Fatalf("stay %s has %d grace entitlements, want exactly 1", s, n)
		}
	}
}

// After a checkout the PMS may re-send an arrival (delayed GI, resync duplicate, reinstatement). Only an
// APPLIED GO for this exact stay and interface may be the boundary, or the grace would be dated wrongly or a
// closed episode resurrected.
func TestIntegration_NonCheckoutEventCannotBeUsedAsABoundary(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	gi := seedEventTyped(t, p, f, "GI")
	if _, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay,
		BoundarySource{StayEventID: gi}); err == nil {
		t.Fatal("a GI was accepted as a checkout boundary; an arrival must never date a grace period")
	}

	// A rejected boundary must leave no grace behind.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 0 {
		t.Fatalf("a refused boundary still created %d grace entitlement(s)", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay); n != 0 {
		t.Fatalf("a refused boundary wrote %d audit row(s)", n)
	}

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("the genuine checkout after a refused GI: %+v err=%v", res, err)
	}
}

// Another stay's checkout event must not be usable as this stay's boundary.
func TestIntegration_CrossStayCheckoutEventIsRefused(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET normalized_room_number='412' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	activeEnt(t, p, f)

	other := secondStayInSameRoom(t, p, f, "412", "RES-B")
	entForStay(t, p, f, other)
	othersCheckout := checkoutEventFor(t, p, f, other, 6)
	if _, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, othersCheckout); err == nil {
		t.Fatal("one guest's checkout event converted a different guest sharing the room")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 0 {
		t.Fatalf("a cross-stay boundary created %d grace entitlement(s)", n)
	}
}
