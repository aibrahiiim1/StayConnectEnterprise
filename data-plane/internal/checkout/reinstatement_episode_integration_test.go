//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// REINSTATEMENT, PERFORMED RATHER THAN MENTIONED.
//
// The Phase-0 contract §7.2 and acceptance item F7 are specific: reinstatement is a NEW EPISODE, and "a
// post-reinstatement checkout is a new episode with exactly one new grace conversion". Until this file the
// second half of that sentence had no test. The existing episode-idempotency test proves the same episode
// cannot convert twice, which is the safety half; nothing proved that a legitimately reinstated guest CAN
// convert again, which is the service half.
//
// The failure it guards is quiet and bad. Grace conversions are keyed unique on
// (stay_id, checkout_episode). If the episode did not advance on reinstatement -- or if the converter read
// it from the wrong place -- a guest whose checkout was reversed and who then genuinely departed again would
// be refused as a duplicate, silently receive no grace at all, and be cut off at the door with a
// NO_GRACE audit row that looks entirely normal.
//
// A NOTE ON WHAT THIS FILE REPLACES. An earlier test of mine was called
// TestIntegration_SameSubjectRecheckIn_DoesNotProduceTwoLiveEntitlements and did not perform a re-check-in
// at all: it asserted post-checkout state and took its own name as the claim. That is the exact failure mode
// of trusting a test name, and it is corrected here by tests that construct every transition they assert.

// reinstate performs what the Stay engine's OpReinstate performs when a GI lands on a CHECKED_OUT stay:
// status back to IN_HOUSE, exactly one lifecycle_version bump, the previous episode's boundary cleared --
// AND the arrival event applied, which re-pins the stay's application lineage to that GI.
//
// THE LINEAGE PIN IS NOT A DETAIL, and leaving it out is how the first draft of this file produced a false
// alarm. The converter refuses any boundary event that is not exactly stays.last_applied_event_id. A
// reinstatement driven by a real GI moves that pin to the GI, which is precisely what makes a replayed
// pre-reinstatement GO unusable afterwards. A helper that skipped the pin left the old GO still pinned, so
// the replay was accepted and the test reported a lifecycle corruption that cannot occur in production.
// Modelling the reinstatement faithfully is the difference between testing the system and testing the
// helper.
func reinstate(t *testing.T, p *pgxpool.Pool, f fixture, stay string) int {
	t.Helper()
	var episode int
	if err := p.QueryRow(context.Background(), `
		UPDATE iam_v2.stays
		   SET status='IN_HOUSE', lifecycle_version = lifecycle_version + 1, effective_checkout_at = NULL
		 WHERE id=$1
		RETURNING lifecycle_version`, stay).Scan(&episode); err != nil {
		t.Fatalf("reinstate the stay: %v", err)
	}
	// The arrival that caused it, applied and pinned -- the engine does this for every event it applies.
	seedEventTyped(t, p, f, "GI")
	return episode
}

// reauthenticate models the guest signing in again after their checkout was reversed: the stale grace is
// superseded and a NORMAL entitlement takes its place.
//
// It has to supersede rather than simply insert, because ent_live_stay permits exactly one live entitlement
// per stay -- which is the invariant under test elsewhere in this file. Returning to the portal is what a
// reinstated guest actually does, and it is the step that gives their next departure something to convert.
func reauthenticate(t *testing.T, p *pgxpool.Pool, f fixture, stay, staleEnt string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),'SUPERSEDED')`, staleEnt); err != nil {
		t.Fatalf("supersede the stale grace: %v", err)
	}
	g := f
	g.stay = stay
	return activeEnt(t, p, g)
}

func stayState(t *testing.T, p *pgxpool.Pool, stay string) (status string, episode int, checkoutAt *time.Time) {
	t.Helper()
	if err := p.QueryRow(context.Background(),
		`SELECT status, lifecycle_version, effective_checkout_at FROM iam_v2.stays WHERE id=$1`, stay).
		Scan(&status, &episode, &checkoutAt); err != nil {
		t.Fatalf("read the stay: %v", err)
	}
	return
}

// A REINSTATED GUEST WHO LEAVES AGAIN GETS A SECOND GRACE -- AND EXACTLY ONE.
//
// The whole sequence is performed here: depart, be reinstated, depart again. Each assertion is about state
// that this test itself created.
func TestIntegration_ReinstatementStartsANewEpisodeAndAllowsExactlyOneNewGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	// FIRST DEPARTURE.
	first, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 5))
	if err != nil || !first.GraceCreated {
		t.Fatalf("first checkout: %+v err=%v", first, err)
	}
	var firstEnds time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, first.NewEntitlementID).Scan(&firstEnds); err != nil {
		t.Fatal(err)
	}
	_, episodeBefore, _ := stayState(t, p, f.stay)

	// THE PMS REVERSES THE CHECKOUT. This is the transition the whole test turns on, so its effects are
	// asserted rather than assumed: the stay is back in house, the episode advanced by exactly one, and the
	// previous episode's boundary is gone.
	episodeAfter := reinstate(t, p, f, f.stay)
	status, episodeRead, checkoutAt := stayState(t, p, f.stay)
	if status != "IN_HOUSE" {
		t.Fatalf("after reinstatement the stay is %s, want IN_HOUSE", status)
	}
	if episodeAfter != episodeBefore+1 || episodeRead != episodeAfter {
		t.Fatalf("episode went %d -> %d, want exactly one bump", episodeBefore, episodeAfter)
	}
	if checkoutAt != nil {
		t.Fatalf("reinstatement left the old episode's checkout boundary at %s", checkoutAt)
	}

	// THE GUEST SIGNS IN AGAIN. A reinstated guest is an in-house guest with no valid package -- their old
	// grace belongs to a closed episode -- so they return to the portal and are issued a normal entitlement,
	// which supersedes the stale grace. Without this step the only live entitlement at the next boundary
	// would be that stale grace, and converting a grace into another grace is not something the product
	// should do: it would let repeated reinstatement chain grace periods indefinitely.
	reauthenticate(t, p, f, f.stay, first.NewEntitlementID)

	// SECOND DEPARTURE, in the new episode.
	second, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 6))
	if err != nil {
		t.Fatalf("post-reinstatement checkout: %v", err)
	}
	if !second.GraceCreated {
		t.Fatalf("a reinstated guest who departed again received NO grace (reason %q) -- the episode did not advance for the converter", second.Reason)
	}
	if second.NewEntitlementID == first.NewEntitlementID {
		t.Fatal("the second departure reused the first episode's grace entitlement")
	}

	// EXACTLY ONE GRACE PER EPISODE, TWO EPISODES, TWO GRACES. The unique index is on
	// (stay_id, checkout_episode), so this is the property that would break if the episode were misread.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 2 {
		t.Fatalf("the stay has %d grace entitlements across two episodes, want exactly 2", n)
	}
	if n := count(t, p, `
		SELECT count(DISTINCT checkout_episode) FROM iam_v2.purchases
		 WHERE stay_id=$1 AND trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE')`, f.stay); n != 2 {
		t.Fatalf("the two conversions carry %d distinct episodes, want 2", n)
	}

	// ONE LIVE ENTITLEMENT THROUGHOUT. The second conversion superseded the first grace rather than running
	// beside it; ent_live_stay would have refused the write otherwise, so this also proves the supersession
	// actually happened rather than the insert quietly failing.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("the stay holds %d live entitlements, want exactly 1", n)
	}
	var firstStatus string
	var firstEndsAfter time.Time
	if err := p.QueryRow(ctx,
		`SELECT status, window_ends_at FROM iam_v2.entitlements WHERE id=$1`, first.NewEntitlementID).
		Scan(&firstStatus, &firstEndsAfter); err != nil {
		t.Fatal(err)
	}
	if firstStatus == "ACTIVE" {
		t.Fatal("the first episode's grace is still live alongside the second")
	}
	// AND ITS DEADLINE WAS NEVER REWRITTEN. A superseded grace keeps the window it was given; moving it
	// would falsify the record of what that guest was actually entitled to.
	if !firstEndsAfter.Equal(firstEnds) {
		t.Fatalf("the first grace's deadline was rewritten: %s -> %s", firstEnds, firstEndsAfter)
	}
}

// REPLAYING THE OLD DEPARTURE AFTER A REINSTATEMENT MUST NOT MINT A THIRD GRACE.
//
// The realistic shape: the appliance reconnects, the PMS resends a batch, and the ORIGINAL checkout event --
// the one already consumed by the first episode -- arrives again after the guest has been reinstated. It is
// a real, applied, correctly-scoped GO. The only thing wrong with it is that it is old.
func TestIntegration_ReplayingThePreReinstatementCheckoutCreatesNothing(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	original := checkoutEventFor(t, p, f, f.stay, 5)
	first, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, original)
	if err != nil || !first.GraceCreated {
		t.Fatalf("first checkout: %+v err=%v", first, err)
	}
	reinstate(t, p, f, f.stay)

	// THE OLD EVENT COMES BACK. Whatever the converter does with it, it must not be a new grace for the new
	// episode: that event describes a departure the guest has already been reinstated from.
	replay, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, original)
	if err == nil && replay.GraceCreated {
		t.Fatal("a replayed pre-reinstatement checkout created a grace in the new episode")
	}

	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 1 {
		t.Fatalf("after the replay the stay has %d grace entitlements, want the original 1", n)
	}
	// The stay is still in house: a stale message must not drag it back to CHECKED_OUT.
	if status, _, _ := stayState(t, p, f.stay); status != "IN_HOUSE" {
		t.Fatalf("a replayed old checkout moved the reinstated stay to %s", status)
	}
	// And exactly one live entitlement, still.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("the stay holds %d live entitlements after the replay, want 1", n)
	}
}

// THE INVARIANT IS ENFORCED BY THE DATABASE, NOT BY HOPE.
//
// "One live entitlement per subject" is the sentence every other test leans on. For a PMS room-auth guest the
// subject is the STAY -- grace entitlements carry stay_id and no guest principal or account -- and the
// enforcement is a partial unique index, ent_live_stay. This proves the index actually fires, because an
// invariant that is merely intended is the one that silently stops holding.
//
// It also settles the room-reuse question from the other direction: two stays are two subjects, so two live
// entitlements in one room are legal and expected, while two live entitlements on ONE stay are impossible.
func TestIntegration_SecondLiveEntitlementForOneStayIsRefusedByTheDatabase(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 5))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}

	// Try to give the same stay a second live entitlement directly. No conversion path would do this; the
	// point is that even a direct write cannot.
	_, err = p.Exec(ctx, `
		INSERT INTO iam_v2.entitlements
		  (id,tenant_id,site_id,stay_id,pms_interface_id,service_plan_revision_id,package_revision_id,
		   time_accounting_mode,end_mode,status,activated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6,'VALIDITY_WINDOW','HARD_EXPIRY','ACTIVE',now())`,
		f.tenant, f.site, f.stay, f.iface, f.svcRev, f.gracePkgRev)
	if err == nil {
		t.Fatal("the database accepted a SECOND live entitlement for one stay; ent_live_stay is not protecting the invariant")
	}

	// And the grace is untouched by the refused write.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("after the refused insert the stay holds %d live entitlements, want 1", n)
	}
}

// A SECOND STAY IS A SECOND SUBJECT, AND MAY HOLD ITS OWN LIVE ENTITLEMENT.
//
// The counterpart of the test above, stated as its own claim because the two together are what "per subject"
// means: the index is keyed on stay_id, so a genuinely new Stay -- the same human returning tomorrow, or a
// different guest in the same room -- is a different subject and is entitled in its own right.
func TestIntegration_ANewStayIsANewSubjectAndMayBeLiveBesideAnOldGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 5))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}

	// The guest returns tomorrow under a genuinely new reservation, and signs in. A NEW STAY AND A NEW
	// ENTITLEMENT ARE ACTUALLY CREATED HERE -- that is the transition being asserted, not a precondition.
	returning := secondStayInSameRoom(t, p, f, "412", "RES-RETURN")
	returningEnt := entForStay(t, p, f, returning)

	if returningEnt == res.NewEntitlementID {
		t.Fatal("the returning guest was handed the old grace entitlement")
	}
	var entStay string
	var isGrace bool
	var supersedes *string
	if err := p.QueryRow(ctx,
		`SELECT stay_id::text, is_emergency_grace, supersedes_entitlement_id::text
		   FROM iam_v2.entitlements WHERE id=$1`, returningEnt).Scan(&entStay, &isGrace, &supersedes); err != nil {
		t.Fatal(err)
	}
	if entStay != returning {
		t.Fatalf("the new entitlement belongs to stay %s, want the new stay %s", entStay, returning)
	}
	if isGrace {
		t.Fatal("the returning guest started on a grace entitlement instead of a normal package")
	}
	if supersedes != nil {
		t.Fatalf("a new stay's first entitlement must supersede nothing; it superseded %s", *supersedes)
	}

	// Both live: one per subject, two subjects.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("the departed stay holds %d live entitlements, want 1 (its grace)", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, returning); n != 1 {
		t.Fatalf("the returning stay holds %d live entitlements, want 1", n)
	}
}
