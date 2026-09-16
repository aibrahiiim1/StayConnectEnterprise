//go:build integration

package stayengine

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
)

// THE WHOLE LIFECYCLE, DRIVEN BY THE CODE THAT ACTUALLY RUNS IT.
//
// Everything here enters through the inbox and is applied by Processor.ProcessNext with the real Checkout
// Converter wired in -- the same two objects pmsd constructs in production. Nothing in this file writes to
// iam_v2.stays. That restriction is the entire point.
//
// WHY IT MATTERS, stated plainly because I got this wrong once. An earlier version of these proofs performed
// the reinstatement with a direct UPDATE that set status, bumped lifecycle_version and cleared the boundary.
// It asserted the right things about the resulting rows and told me nothing about whether the engine would
// ever produce that state from a GI -- and it quietly skipped the part the engine also does, which is to
// re-pin the stay's application lineage to the applied event. That omission produced a false defect report:
// a replayed checkout looked as though it could drag a reinstated stay back to CHECKED_OUT, when in truth
// the lineage pin makes it impossible. A helper that imitates the production path can only ever test the
// helper, and it will be wrong in exactly the places the real path is subtle.
//
// So: real events, real resolver, real transaction boundary, real converter.

// currentStay reads the row the engine maintains. Reading is allowed; writing is not.
func currentStay(t *testing.T, p *pgxpool.Pool, stayID string) (status string, lifecycle int, effco *time.Time, lastEvent *string) {
	t.Helper()
	if err := p.QueryRow(context.Background(), `
		SELECT status, lifecycle_version, effective_checkout_at, last_applied_event_id::text
		  FROM iam_v2.stays WHERE id=$1`, stayID).Scan(&status, &lifecycle, &effco, &lastEvent); err != nil {
		t.Fatalf("read stay: %v", err)
	}
	return
}

// redeliver attempts to ingest an event identity the interface has ALREADY accepted, and returns whether the
// database refused it. Redelivery is stopped at ingestion, not at resolution: se_live_identity is unique on
// (tenant, site, pms_interface_id, external_event_identity) for LIVE events, so a reconnect or resync that
// resends a departure cannot even create a second row to be processed. Asserting the refusal is the honest
// way to describe that boundary -- the earlier draft of this test tried to insert and process a duplicate,
// which is not a sequence production can reach.
func redeliver(t *testing.T, p *pgxpool.Pool, s scope, identity, eventType, payloadJSON string) bool {
	t.Helper()
	_, err := p.Exec(context.Background(), `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,external_event_identity,event_type,pms_timestamp_raw,
		 pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,
		 processing_status,admission_kind,resync_generation)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,'x',now(),'UTC',1,1,false,$6::jsonb,'PENDING','LIVE',0)`,
		s.tenant, s.site, s.iface, identity, eventType, payloadJSON)
	return err != nil
}

func countIn(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// GI → entitlement → GO → Grace → GI → reinstated → re-authenticate → GO → second Grace.
//
// Each arrow is an event the processor applies. The assertions between them are about state the ENGINE
// produced, never state this test placed.
func TestIntegration_RealPath_ReinstatementLifecycleEndToEnd(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))
	guest := pay("R-RE1", "512", "Nakamura", "Aiko", "F-RE1", "260201", "260205")

	// ---- 1. ARRIVAL. The engine creates the Stay from a GI; the test does not. ----
	insertLive(t, p, s, "E1-GI", "GI", guest)
	process(t, pr, s)

	var stayID string
	if err := p.QueryRow(ctx,
		`SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R-RE1'`,
		s.iface).Scan(&stayID); err != nil {
		t.Fatalf("the engine did not create a Stay from the arrival: %v", err)
	}
	status, episode1, effco, _ := currentStay(t, p, stayID)
	if status != "IN_HOUSE" || effco != nil {
		t.Fatalf("after arrival: status=%s effective_checkout_at=%v, want IN_HOUSE/nil", status, effco)
	}

	// ---- 2. THE GUEST SIGNS IN. A normal, non-grace entitlement. ----
	boundary1 := time.Now().Add(-4 * time.Hour).Truncate(time.Microsecond)
	firstEnt := grantEntitlement(t, p, s, stayID, pkgRev, boundary1.Add(-time.Hour))

	// ---- 3. DEPARTURE. The engine flips the Stay and the converter runs inside the SAME transaction. ----
	insertLiveAt(t, p, s, "E2-GO", "GO", guest, boundary1)
	process(t, pr, s)

	status, _, effco, lastEvent := currentStay(t, p, stayID)
	if status != "CHECKED_OUT" {
		t.Fatalf("after departure the engine left the stay %s, want CHECKED_OUT", status)
	}
	if effco == nil || !effco.Equal(boundary1) {
		t.Fatalf("the boundary is %v, want the event's trusted timestamp %v", effco, boundary1)
	}

	var grace1 string
	var grace1Ends time.Time
	if err := p.QueryRow(ctx, `
		SELECT id::text, window_ends_at FROM iam_v2.entitlements
		 WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID).Scan(&grace1, &grace1Ends); err != nil {
		t.Fatalf("the real checkout path produced no grace entitlement: %v", err)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='TERMINATED' AND terminal_reason='CONVERTED'`, firstEnt); n != 1 {
		t.Fatal("the original entitlement was not converted by the real checkout path")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, stayID); n != 1 {
		t.Fatalf("after checkout the stay holds %d live entitlements, want exactly 1 (the grace)", n)
	}

	// ---- 4. THE OLD DEPARTURE CANNOT BE INGESTED TWICE. ----
	// A reconnect or resync that resends this departure is refused at the door by se_live_identity, so there
	// is never a second row for the processor to apply. This is where replay protection actually lives.
	if !redeliver(t, p, s, "E2-GO", "GO", guest) {
		t.Fatal("the interface accepted the SAME departure identity twice; replay protection is not at ingestion")
	}
	// No process() call follows, deliberately: the refusal means no row was created, so there is nothing
	// pending. Calling the processor here would assert against an empty queue, not against a replay.
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID); n != 1 {
		t.Fatalf("after the refused redelivery the stay has %d grace entitlements, want 1", n)
	}

	// ---- 5. GENUINE REINSTATEMENT. A real GI on a CHECKED_OUT stay, applied by the engine. ----
	insertLive(t, p, s, "E3-GI", "GI", guest)
	process(t, pr, s)

	status, episode2, effco, lastEventAfterGI := currentStay(t, p, stayID)
	if status != "IN_HOUSE" {
		t.Fatalf("genuine reinstatement left the stay %s, want IN_HOUSE", status)
	}
	if episode2 != episode1+1 {
		t.Fatalf("lifecycle_version went %d -> %d, want exactly one advance", episode1, episode2)
	}
	if effco != nil {
		t.Fatalf("reinstatement left the previous episode's boundary at %v", effco)
	}
	// THE LINEAGE MOVED TO THE ARRIVAL. This is what makes the old departure unusable as a boundary, and it
	// is produced by the engine -- which is precisely what the earlier helper-driven version never did.
	if lastEventAfterGI == nil || (lastEvent != nil && *lastEventAfterGI == *lastEvent) {
		t.Fatal("reinstatement did not re-pin the stay's application lineage to the arrival event")
	}
	// The grace from the closed episode is untouched by the reinstatement itself.
	var graceStatusAfterGI string
	var graceEndsAfterGI time.Time
	if err := p.QueryRow(ctx, `SELECT status, window_ends_at FROM iam_v2.entitlements WHERE id=$1`, grace1).
		Scan(&graceStatusAfterGI, &graceEndsAfterGI); err != nil {
		t.Fatal(err)
	}
	if !graceEndsAfterGI.Equal(grace1Ends) {
		t.Fatalf("reinstatement rewrote the old grace deadline: %s -> %s", grace1Ends, graceEndsAfterGI)
	}

	// ---- 6. THE GUEST AUTHENTICATES AGAIN. ----
	// A reinstated guest is in house with a package belonging to a closed episode. Signing in supersedes it;
	// ent_live_stay would refuse a second live row, so this also demonstrates the supersession is real.
	boundary2 := time.Now().Add(-30 * time.Minute).Truncate(time.Microsecond)
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),'SUPERSEDED')`, grace1); err != nil {
		t.Fatalf("supersede the closed episode's grace: %v", err)
	}
	secondEnt := grantEntitlement(t, p, s, stayID, pkgRev, boundary2.Add(-time.Hour))
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, stayID); n != 1 {
		t.Fatalf("after re-authentication the stay holds %d live entitlements, want exactly 1", n)
	}

	// ---- 7. A LATER GENUINE DEPARTURE. New episode, so a new conversion is allowed. ----
	insertLiveAt(t, p, s, "E4-GO", "GO", guest, boundary2)
	process(t, pr, s)

	status, episode3, effco, _ := currentStay(t, p, stayID)
	if status != "CHECKED_OUT" {
		t.Fatalf("the second departure left the stay %s, want CHECKED_OUT", status)
	}
	if episode3 != episode2 {
		t.Fatalf("a checkout changed lifecycle_version %d -> %d; only reinstatement may advance it", episode2, episode3)
	}
	if effco == nil || !effco.Equal(boundary2) {
		t.Fatalf("the second boundary is %v, want %v", effco, boundary2)
	}

	// EXACTLY ONE NEW GRACE, for the new episode.
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID); n != 2 {
		t.Fatalf("the stay has %d grace entitlements across two episodes, want exactly 2", n)
	}
	if n := countIn(t, p, `
		SELECT count(DISTINCT checkout_episode) FROM iam_v2.purchases
		 WHERE stay_id=$1 AND trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE')`, stayID); n != 2 {
		t.Fatalf("the two conversions carry %d distinct episodes, want 2", n)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='TERMINATED' AND terminal_reason='CONVERTED'`, secondEnt); n != 1 {
		t.Fatal("the second departure did not convert the re-authenticated entitlement")
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, stayID); n != 1 {
		t.Fatalf("the stay holds %d live entitlements at the end, want exactly 1", n)
	}

	// AND THE FIRST GRACE'S RECORD IS UNCHANGED THROUGHOUT. A superseded grace keeps the window it was
	// given; rewriting it would falsify what that guest was actually entitled to.
	var grace1EndsFinal time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, grace1).Scan(&grace1EndsFinal); err != nil {
		t.Fatal(err)
	}
	if !grace1EndsFinal.Equal(grace1Ends) {
		t.Fatalf("the first grace's deadline was rewritten: %s -> %s", grace1Ends, grace1EndsFinal)
	}
}

// A STALE ARRIVAL AFTER A DEPARTURE IS NOT A REINSTATEMENT, AND THE ENGINE DECIDES THAT -- NOT THIS TEST.
//
// The difference between a genuine reinstatement and a late duplicate is not visible in the event body: both
// are GIs for a checked-out stay. It is the inbox identity that separates them. A redelivered event the stay
// has already applied is a deterministic no-op; a NEW arrival is a new episode. Both paths are driven here
// through the processor.
func TestIntegration_RealPath_RedeliveredArrivalIsNotASecondReinstatement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))
	guest := pay("R-RE2", "514", "Okonkwo", "Ada", "F-RE2", "260301", "260305")

	insertLive(t, p, s, "F1-GI", "GI", guest)
	process(t, pr, s)
	var stayID string
	if err := p.QueryRow(ctx,
		`SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R-RE2'`,
		s.iface).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().Add(-3 * time.Hour).Truncate(time.Microsecond)
	grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-time.Hour))
	insertLiveAt(t, p, s, "F2-GO", "GO", guest, boundary)
	process(t, pr, s)

	// One genuine reinstatement.
	insertLive(t, p, s, "F3-GI", "GI", guest)
	process(t, pr, s)
	_, episodeAfterFirst, _, _ := currentStay(t, p, stayID)

	// THE SAME ARRIVAL, REDELIVERED. Refused at ingestion, so it can never become a second reinstatement.
	if !redeliver(t, p, s, "F3-GI", "GI", guest) {
		t.Fatal("the interface accepted the SAME arrival identity twice")
	}

	status, episodeAfterReplay, _, _ := currentStay(t, p, stayID)
	if status != "IN_HOUSE" {
		t.Fatalf("a redelivered arrival moved the stay to %s", status)
	}
	if episodeAfterReplay != episodeAfterFirst {
		t.Fatalf("a redelivered arrival advanced lifecycle_version %d -> %d; only a genuine new arrival may",
			episodeAfterFirst, episodeAfterReplay)
	}
	if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID); n != 1 {
		t.Fatalf("the replay changed the grace count to %d, want 1", n)
	}
}

// TWO GUESTS, ONE ROOM, BOTH THROUGH THE ENGINE.
//
// The room-reuse independence was previously proven by constructing the second Stay with an INSERT. Here the
// engine creates both Stays from their own arrivals, so the isolation being asserted is the isolation the
// production resolver actually provides -- including that it does not match the arriving guest onto the
// departing guest's reservation just because the room agrees.
func TestIntegration_RealPath_RoomReuseCreatesAnIndependentStay(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	leaving := pay("R-OUT", "777", "Haddad", "Samir", "F-OUT", "260401", "260403")
	arriving := pay("R-IN", "777", "Lindqvist", "Elsa", "F-IN", "260403", "260407")

	insertLive(t, p, s, "G1-GI", "GI", leaving)
	process(t, pr, s)
	var stayA string
	if err := p.QueryRow(ctx,
		`SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R-OUT'`, s.iface).Scan(&stayA); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	grantEntitlement(t, p, s, stayA, pkgRev, boundary.Add(-2*time.Hour))
	insertLiveAt(t, p, s, "G2-GO", "GO", leaving, boundary)
	process(t, pr, s)

	var graceA string
	var graceAEnds time.Time
	if err := p.QueryRow(ctx, `SELECT id::text, window_ends_at FROM iam_v2.entitlements
		 WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayA).Scan(&graceA, &graceAEnds); err != nil {
		t.Fatalf("guest A received no grace: %v", err)
	}

	// THE NEXT GUEST ARRIVES IN THE SAME ROOM, under their own reservation. The engine resolves them.
	insertLive(t, p, s, "G3-GI", "GI", arriving)
	process(t, pr, s)

	var stayB string
	if err := p.QueryRow(ctx,
		`SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R-IN'`, s.iface).Scan(&stayB); err != nil {
		t.Fatalf("the engine did not create a Stay for the arriving guest: %v", err)
	}
	if stayB == stayA {
		t.Fatal("the arriving guest was resolved onto the departing guest's Stay -- the room was treated as identity")
	}

	// A's grace is exactly as it was, and still theirs.
	var endsAfter time.Time
	var statusAfter, ownerAfter string
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at, status, stay_id::text FROM iam_v2.entitlements WHERE id=$1`, graceA).
		Scan(&endsAfter, &statusAfter, &ownerAfter); err != nil {
		t.Fatal(err)
	}
	if !endsAfter.Equal(graceAEnds) || statusAfter != "ACTIVE" || ownerAfter != stayA {
		t.Fatalf("the arrival disturbed A's grace: ends=%s status=%s owner=%s", endsAfter, statusAfter, ownerAfter)
	}

	// B signs in and holds their own entitlement; both are live, one per Stay.
	entB := grantEntitlement(t, p, s, stayB, pkgRev, time.Now().Add(-time.Minute))
	if entB == graceA {
		t.Fatal("the arriving guest was handed the departing guest's entitlement")
	}
	for _, c := range []struct {
		stay, who string
	}{{stayA, "the departing guest"}, {stayB, "the arriving guest"}} {
		if n := countIn(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, c.stay); n != 1 {
			t.Fatalf("%s holds %d live entitlements, want exactly 1", c.who, n)
		}
	}
	if n := countIn(t, p, `
		SELECT count(*) FROM iam_v2.entitlements e JOIN iam_v2.stays st ON st.id=e.stay_id
		 WHERE st.pms_interface_id=$1 AND st.normalized_room_number='777' AND e.status='ACTIVE'`, s.iface); n != 2 {
		t.Fatalf("room 777 carries %d live entitlements, want 2 -- one per guest", n)
	}
}
