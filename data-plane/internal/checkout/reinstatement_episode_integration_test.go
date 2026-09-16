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

// THE HELPER-DRIVEN REINSTATEMENT TESTS THAT LIVED HERE HAVE MOVED, AND THE HELPER IS GONE.
//
// They performed the reinstatement with a direct UPDATE to iam_v2.stays. That imitates the production path
// instead of exercising it, and it can only be as correct as my model of the engine -- which it was not: the
// helper omitted the lineage re-pin the engine performs, and that omission manufactured a false defect
// report about a replayed checkout corrupting a reinstated stay.
//
// The whole lifecycle now runs through Processor.ProcessNext with the real converter in
// internal/stayengine/reinstatement_lifecycle_integration_test.go: arrival, sign-in, departure, grace,
// genuine reinstatement, re-authentication, second departure, second grace -- every transition applied by
// the code that applies it in production.
//
// What remains in this file are the two claims that are about the DATABASE rather than the event path, and
// are better asserted directly.

func stayState(t *testing.T, p *pgxpool.Pool, stay string) (status string, episode int, checkoutAt *time.Time) {
	t.Helper()
	if err := p.QueryRow(context.Background(),
		`SELECT status, lifecycle_version, effective_checkout_at FROM iam_v2.stays WHERE id=$1`, stay).
		Scan(&status, &episode, &checkoutAt); err != nil {
		t.Fatalf("read the stay: %v", err)
	}
	return
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
