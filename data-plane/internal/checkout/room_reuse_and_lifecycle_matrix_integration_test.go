//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A ROOM IS NOT A GUEST, AND THE APPLIANCE MUST NEVER BEHAVE AS IF IT WERE.
//
// Room 412 will be occupied by a different person tonight than it was this morning, and for a stretch in the
// middle both of them have a claim on it: the departing guest is inside their checkout grace while the
// arriving guest is already checking in. Every serious failure this file guards has the same shape -- some
// piece of state keyed on the room instead of on the person, so one guest's arrival silently reaches into
// the other's entitlement.
//
// The failure would be quiet and awful. A guest whose grace was cut short by a stranger's arrival just sees
// the wifi stop; a guest who inherits the previous occupant's allowance gets somebody else's data; and a
// grace timer reset by a check-in hands out free access nobody agreed to. None of it raises an error.
//
// The conversion path itself never reads a room -- grep it and there is no such column in the query. That is
// the design. These tests exist because "the code does not currently do that" is a weaker guarantee than
// "the code cannot do that without turning a test red", and room reuse is where the temptation to key on a
// room number is strongest.

// secondStayInSameRoom creates an INDEPENDENT Stay -- its own stay id, its own reservation identity -- on the
// same tenant, site and PMS interface as f, and puts it in the same room. It is deliberately a sibling, not a
// copy: the only thing it shares with the first guest is the four walls.
func secondStayInSameRoom(t *testing.T, p *pgxpool.Pool, f fixture, room, reservation string) string {
	t.Helper()
	var stay string
	if err := p.QueryRow(context.Background(), `
		INSERT INTO iam_v2.stays
		  (id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		   status,lifecycle_version,last_applied_event_version,normalized_room_number)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$4,'IN_HOUSE',1,5,$5)
		RETURNING id::text`, f.tenant, f.site, f.iface, reservation, room).Scan(&stay); err != nil {
		t.Fatalf("seed the arriving guest's stay: %v", err)
	}
	return stay
}

// seedEventTyped seeds and applies an event of an arbitrary TYPE for this stay. seedEvent hardcodes 'GO',
// which is right for every test that needs a boundary and useless for the tests that need to prove a
// non-checkout event is refused as one.
func seedEventTyped(t *testing.T, p *pgxpool.Pool, f fixture, eventType string) string {
	t.Helper()
	ctx := context.Background()
	ts := f.boundary
	var eid string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,
		 pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,
		 processing_status,admission_kind,resync_generation)
		VALUES (gen_random_uuid(),$1,$2,$3,NULL,$4,$5,'x',$6,'UTC',7,1,false,'{}','PENDING','LIVE',0)
		RETURNING id`,
		f.tenant, f.site, f.iface, "EV-TYPED-"+eventType, eventType, ts).Scan(&eid); err != nil {
		t.Fatalf("seed a %s event: %v", eventType, err)
	}
	applyEvent(t, p, eid, f.stay)
	return eid
}

// checkoutEventFor builds an APPLIED GO for a SPECIFIC stay with its own external identity. checkoutEvent
// derives the identity from a fixed sequence number, so calling it twice in one test -- which is exactly what
// two guests sharing a room requires -- collides on the live-identity uniqueness constraint.
func checkoutEventFor(t *testing.T, p *pgxpool.Pool, f fixture, stay string, seq int) BoundarySource {
	t.Helper()
	eid := seedEvent(t, p, f, &f.boundary, false, seq, "LIVE", 0, stay)
	applyEvent(t, p, eid, stay)
	return BoundarySource{StayEventID: eid}
}

// entForStay gives an arbitrary Stay a live entitlement, the way a fresh guest signing in would.
func entForStay(t *testing.T, p *pgxpool.Pool, f fixture, stay string) string {
	t.Helper()
	g := f
	g.stay = stay
	return activeEnt(t, p, g)
}

// GUEST A IS IN GRACE. GUEST B CHECKS INTO THE SAME ROOM. NEITHER NOTICES THE OTHER.
//
// This is the scenario the product decision is written for, and the one a hotel actually runs every
// afternoon. A departs at 11:00 and keeps working in the lobby on their grace hour; B arrives at 11:20 and
// signs in upstairs. Both are entitled at the same moment, in the same room, and the two must be completely
// independent.
func TestIntegration_RoomReuse_ArrivingGuestDoesNotDisturbTheDepartingGuestsGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})

	// Put the departing guest in room 412 so the two stays genuinely collide on the room.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET normalized_room_number='412' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("guest A checkout: %+v err=%v", res, err)
	}
	graceID := res.NewEntitlementID
	var graceEndsAt time.Time
	// NULLABLE ON PURPOSE: a grace entitlement with no data cap stores NULL, not 0, and the two mean
	// different things. Scanning it as a plain int64 asserts a cap exists, which is not this test's claim.
	var graceQuota *int64
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at, data_quota_bytes FROM iam_v2.entitlements WHERE id=$1`, graceID).
		Scan(&graceEndsAt, &graceQuota); err != nil {
		t.Fatal(err)
	}

	// GUEST B ARRIVES IN THE SAME ROOM and signs in normally.
	arrivingStay := secondStayInSameRoom(t, p, f, "412", "RES-B")
	arrivingEnt := entForStay(t, p, f, arrivingStay)

	// A's grace is untouched: same deadline, same allowance, still live, still theirs.
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

	// B's entitlement is B's: a different row, a different stay, and NOT descended from A's.
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

	// AND BOTH ARE LIVE AT ONCE. The room now legitimately carries two live entitlements belonging to two
	// different people -- which is exactly what "one live entitlement per SUBJECT" permits and what a
	// room-keyed implementation could not express.
	// SCOPED TO THIS SITE. The integration database is shared and persists across runs, so an unscoped count
	// over "room 412" silently accumulates every previous execution -- which is how this assertion first
	// reported 29. A room number is only meaningful inside one site, which is the point of the whole file.
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

// THE ARRIVING GUEST'S CHECKOUT IS THEIRS ALONE.
//
// The mirror of the first test, and the one that would bite later: B eventually checks out too. Their
// conversion must act on B's entitlement and leave A's grace -- which may still be running -- completely
// alone, including not counting as a second grace for the room.
func TestIntegration_RoomReuse_SecondGuestsCheckoutDoesNotTouchTheFirstsGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET normalized_room_number='412' WHERE id=$1`, f.stay); err != nil {
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

	// B arrives, uses the wifi, and departs while A is still in grace.
	bStay := secondStayInSameRoom(t, p, f, "412", "RES-B")
	entForStay(t, p, f, bStay)
	resB, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, bStay, checkoutEventFor(t, p, f, bStay, 6))
	if err != nil || !resB.GraceCreated {
		t.Fatalf("guest B checkout: %+v err=%v", resB, err)
	}
	if resB.NewEntitlementID == resA.NewEntitlementID {
		t.Fatal("guest B's checkout reused guest A's grace entitlement")
	}

	// A's window is still exactly where it was.
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

	// Each stay has exactly one grace. The room has two, belonging to two people, which is correct.
	for _, s := range []string{f.stay, bStay} {
		if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, s); n != 1 {
			t.Fatalf("stay %s has %d grace entitlements, want exactly 1", s, n)
		}
	}
}

// THE SAME PERSON CHECKING BACK IN IS A DIFFERENT QUESTION ENTIRELY.
//
// Room reuse is two subjects; this is one subject twice. The invariant that must hold here is the opposite
// of the one above: a guest may not hold two live entitlements at once, so a genuine new stay for the same
// subject has to supersede the old grace rather than run beside it.
//
// The distinction matters because the two look superficially similar in the data -- a new stay, an existing
// grace -- and getting them the same way round would either split one guest across two entitlements or
// collapse two guests into one.
func TestIntegration_SameSubjectRecheckIn_DoesNotProduceTwoLiveEntitlements(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}

	// The SAME stay subject is still the owner, and it holds exactly one live entitlement: the grace. A second
	// live one for the same subject is the corruption this guards, whatever created it.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("the subject holds %d live entitlements after checkout, want exactly 1", n)
	}

	// The superseded original is terminal and superseded exactly once -- the lineage a re-check-in has to
	// extend rather than fork.
	var supersededBy int
	if err := p.QueryRow(ctx, `
		SELECT count(*) FROM iam_v2.entitlements
		 WHERE supersedes_entitlement_id = (SELECT supersedes_entitlement_id FROM iam_v2.entitlements WHERE id=$1)`,
		res.NewEntitlementID).Scan(&supersededBy); err != nil {
		t.Fatal(err)
	}
	if supersededBy != 1 {
		t.Fatalf("the original entitlement was superseded %d times, want exactly 1", supersededBy)
	}
}

// A CHECK-IN IS NOT A CHECKOUT, AND THE CONVERTER MUST REFUSE TO TREAT ONE AS THE OTHER.
//
// This is the stale/out-of-order case with teeth. After a checkout the PMS may re-send an arrival: a delayed
// GI from before the departure, a duplicate from a resync, or a genuine reinstatement. Whatever it is, it is
// not a GO, and using it as the boundary for a conversion would date the grace from the wrong moment -- or
// resurrect a closed episode.
//
// The converter derives its boundary from a typed GO for this exact stay and interface, APPLIED. Everything
// else is refused. That is asserted here rather than assumed, because the refusal is what stops an
// out-of-order message corrupting a lifecycle.
func TestIntegration_NonCheckoutEventCannotBeUsedAsABoundary(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	// A GI for this very stay, applied and perfectly valid as an arrival -- and useless as a checkout.
	gi := seedEventTyped(t, p, f, "GI")
	if _, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay,
		BoundarySource{StayEventID: gi}); err == nil {
		t.Fatal("a GI was accepted as a checkout boundary; an arrival must never date a grace period")
	}

	// Nothing was created by the refusal. A rejected boundary that still left a grace behind would be worse
	// than accepting it, because it would be invisible.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 0 {
		t.Fatalf("a refused boundary still created %d grace entitlement(s)", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay); n != 0 {
		t.Fatalf("a refused boundary wrote %d audit row(s)", n)
	}

	// And the real checkout still works afterwards: the refusal must not have poisoned the episode.
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("the genuine checkout after a refused GI: %+v err=%v", res, err)
	}
}

// ANOTHER STAY'S CHECKOUT EVENT IS NOT THIS STAY'S BOUNDARY.
//
// The room-reuse failure in its sharpest form: two stays exist at once, and the arriving guest's departure
// event must not be usable to convert the departing guest -- or the reverse. The boundary query is scoped to
// the stay, and this is what that scoping is for.
func TestIntegration_CrossStayCheckoutEventIsRefused(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	if _, err := p.Exec(ctx, `UPDATE iam_v2.stays SET normalized_room_number='412' WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	activeEnt(t, p, f)

	other := secondStayInSameRoom(t, p, f, "412", "RES-B")
	entForStay(t, p, f, other)
	othersCheckout := checkoutEventFor(t, p, f, other, 6) // a real GO, for the OTHER guest

	// Same room, same interface, a genuine applied GO -- and it belongs to somebody else.
	if _, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, othersCheckout); err == nil {
		t.Fatal("one guest's checkout event converted a different guest sharing the room")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 0 {
		t.Fatalf("a cross-stay boundary created %d grace entitlement(s)", n)
	}
}
